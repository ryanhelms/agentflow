# Live events

The console daemon watches each registered worktree and records git and configuration
changes as events in the repository's event store, under
`<common-git-dir>/agentflow/events/`. The console's overview, `agentflow events` and the
`agentflow_events` MCP tool all read those events. The contract is the ADR
[Event bus, message store and event stream](knowledge/engineering/decisions/event-driven-platform.md).

Events are recorded only while the daemon runs. `agentflow events` reads only the
current repository's store; following several repositories with one cursor comes in a
later phase.

## Read events from a terminal

```sh
agentflow events                       # up to 100 events, then the cursor to resume from
agentflow events --type 'git.**'       # only git events
agentflow events --since 'v1:3f2a9c1b7d4e5f60=42'
agentflow events --follow              # start the daemon if needed, print new events until Ctrl-C
agentflow events --json                # {repo, events, cursor, more}
```

| Flag | Meaning |
| --- | --- |
| `--since CURSOR` | Print only events after this cursor. Each page ends with the cursor to pass next. |
| `--type PATTERN` | Event type pattern, repeatable. `*` matches one segment, a final `**` the rest. |
| `--topic T` | Topic `REPO/WORKTREE/PATTERN`, repeatable. `*` as `REPO` means this repository. |
| `--limit N` | Most events per page: default 100, at most 1000. Ignored with `--follow`. |
| `--follow` | Keep printing. With `--json`, prints one `{event, cursor}` object per line. |

The command only reads. It never writes to the store or takes its lock. `--follow` first
starts the daemon the same way as `agentflow ui --no-open`, without opening a browser,
because the daemon runs the watcher.

The MCP tool `agentflow_events` takes `since`, `types`, `topics` and `limit` and returns
`{repo, events, cursor, more}`. It has no follow mode: call it again with the returned
`cursor`.

## Event types

| Type | When |
| --- | --- |
| `git.head.changed` | `HEAD` changes, or the branch it points to moves (a commit) |
| `git.index.changed` | the index is rewritten (staging) |
| `git.refs.changed` | a ref or `packed-refs` changes |
| `git.worktrees.changed` | a linked worktree is added or removed |
| `git.operation.changed` | a rebase, merge, cherry-pick, revert or bisect starts or ends |
| `config.layer.changed` | a file in `.agentflow/` or `.agentflow/providers/` changes |

## Events from hooks

Every `agentflow hook ...` invocation records a `hook.ran` event when it ends: the hook,
the client, the outcome (`passed`, `blocked` or `failed`), a fixed reason code such as
`convention` or `repo_boundary`, and the duration. It never records the command, the
commit message or the hook's output. `pre-bash` runs before every shell command, so it
records only `blocked` and `failed` outcomes.

A hook hands its event to the store as follows. It spends at most 300 ms on this and
prints nothing about it, because git and agents read hook output.

1. With no daemon running, the hook appends the event to the repository's store
   itself, but only when that store already exists. A hook never creates one, so a
   repository where the console has never run gets no `agentflow/events/` directory.
2. While the daemon owns the store, the hook sends the event to the daemon over its
   local notify socket, and the daemon stores and publishes it.
3. If the daemon is starting, stopping or refuses the event, the event is dropped.

The notify socket accepts only `hook.ran` and `commit.created`, only from a process that
proves it can read the daemon's private registry file (`~/.agentflow-ui/`), and only for
repositories and worktrees the daemon serves. On Unix it is a socket file with mode
0600 in that private directory, and the daemon also checks the connecting process's
user ID. On Windows, and when the socket path would be too long, it is a loopback TCP
port on 127.0.0.1 with the same key check.

## The console's event stream

Each worktree's console serves `GET /w/<id>/api/events` as Server-Sent Events. The
request uses the tab's `Authorization: Bearer` session, repeated `topic=` parameters,
up to 16 `tab=<32 hex>` parameters naming the browser's tabs, and, on reconnect,
`Last-Event-ID`.

| Rule | Result |
| --- | --- |
| No or expired session | 401 |
| A malformed topic, more than 64 topics, a malformed `Last-Event-ID`, a malformed `tab=` or more than 16 | 400 |
| A topic or `Last-Event-ID` naming another repository | 403 |
| A `global` topic other than `config.**` or `client.**` (for example `global/*/**`) | 403 |
| A `global` topic with a worktree other than `*` | 400 |
| More than 8 streams on one console origin | 429 |
| The console has no event store for its repository | 503; the browser keeps retrying |

A stream carries only events, states and cursor entries of the repository the session
belongs to, plus `global` when it asks for it. It may follow every worktree of that
repository. `global` holds changes to user-level files: the personal configuration layer
and user-level client settings. Every stream receives the repository states such a change
alters (a personal-layer edit updates `overview.config`); only a stream with a
`global/*/config.**` or `global/*/client.**` topic also receives the `global` events
themselves and a `global=<seq>` entry in its cursor. `Last-Event-ID` may carry that entry.

Frames: `snapshot` (replace all state), `delta` (one event and the states it changed;
`null` removes a key), `resync` (discard state; a `snapshot` follows) and a `: ping`
comment every 15 seconds. The `id:` of `snapshot` and `delta` frames is the cursor. The
overview projection's state per worktree (its `worktree` is the `<id>` of `/w/<id>/`) has
these fields: `branch`, `head`, `oid`, `detached`, `upstream` (`name`, `ahead`, `behind`;
absent without an upstream), `changed`, `staged`, `unstaged`, `untracked`, `operation`,
`task` and `config` (`valid`, `problems`).

The other projections (JSON shapes in `internal/webui/projection_states.go`):

| Projection | Key | Follows | State |
| --- | --- | --- | --- |
| `activity` | repository (no `worktree`) | `gh.**`, `commit.created`, `hook.ran`, `git.head.changed`, `git.worktrees.changed` | `pulls` (open, ready to merge first; each with `worktree`, `ci`, `failed`, `depends_on`), `issues` (bound issues with their `worktree`), `recent` (the 20 newest commits and blocked or failed hooks), `github` (`state`: `ok`, `disabled`, `unavailable` or `rate_limited`; `retry_at`) and `handoff` |
| `approvals` | repository | `approval.**` | `open` (pending and approved, oldest first; `expired` once past `expires_at`) and `recent` (consumed, cancelled and expired from the last 24 hours, newest first, at most 10) |
| `tabs` | worktree | `ui.tabs.changed` | `tabs` (`tab`, `attached`) and `navigation` (`id`, `tab`, `screen`, `role`, `target`, or `null`) |

GitHub data comes from the daemon's poller cache, so a snapshot never calls GitHub.
Approvals come from the approval records, not from the events alone. Answering an
approval still uses the console's approve flow.

**Tabs on the stream.** A tab attaches with `POST /w/<id>/api/tabs/attach {"tab": "<32
hex>"}` under its worktree session. It stays attached while a stream of the origin lists
it in `tab=`, and for 10 seconds after the last such stream closes. A navigation appears
in the `tabs` state of the tab's worktree; only the tab whose ID matches acts on it and
acknowledges with `POST /api/tabs/ack`. The daemon clears it on the acknowledgement or
after 2 seconds. `POST /api/tabs/leave` detaches the tab at once. The older
`GET /api/tabs/events` stream still works until the browser switches over.

The browser shares one stream per console origin through a `SharedWorker` loaded from
`/_agentflow/event-worker.js`.

The watcher uses file notifications and falls back to polling per worktree when
notifications are unavailable. Set `AGENTFLOW_WATCH=poll` to force polling.

## Configuration changes and a running MCP server

From phase 2, a running agentflow MCP server picks up edits to `.agentflow/config.yml`,
`.agentflow/providers/`, `~/.agentflow/config.yml` and `~/.agentflow/providers/` without
a restart. A tool call that is already running finishes with the configuration it
started with.

Restart the client session in these cases, because they do not reload:

- The server started in configuration recovery mode. It stays in recovery mode after you
  fix the configuration.
- The change adds or removes MCP tools, or changes the server's instructions (for example
  a different provider selection). The next tool result tells you when this applies.
