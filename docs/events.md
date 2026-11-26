# Live events

The console daemon watches each registered worktree and records git and configuration
changes as events in the repository's event store, under
`<common-git-dir>/agentflow/events/`. The console's overview, `agentflow events` and the
`agentflow_events` MCP tool all read those events. The contract is the ADR
[Event bus, message store and event stream](knowledge/engineering/decisions/event-driven-platform.md).

Events are recorded only while the daemon runs. Events that belong to no repository,
such as a change to `~/.agentflow/config.yml`, go to one more store, `global`, in the
console's private registry (`~/.agentflow-ui/`). `agentflow events` and the
`agentflow_events` tool read this repository and `global`; where the daemon has not created
the `global` store, they read the repository alone. `agentflow events --all-repos` reads
every repository the daemon serves as well.

## Read events from a terminal

```sh
agentflow events                       # up to 100 events, then the cursor to resume from
agentflow events --type 'git.**'       # only git events
agentflow events --since 'v1:3f2a9c1b7d4e5f60=42'
agentflow events --follow              # start the daemon if needed, print new events until Ctrl-C
agentflow events --all-repos           # every repository the daemon serves, plus global
agentflow events --json                # {repo, events, cursor, more}
```

| Flag | Meaning |
| --- | --- |
| `--since CURSOR` | Print only events after this cursor. Each page ends with the cursor to pass next. |
| `--type PATTERN` | Event type pattern, repeatable. `*` matches one segment, a final `**` the rest. |
| `--topic T` | Topic `REPO/WORKTREE/PATTERN`, repeatable. `REPO` is a repository ID, `global` or `*`. `*` means this repository, or every repository with `--all-repos`. |
| `--limit N` | Most events per page: default 100, at most 1000. Ignored with `--follow`. |
| `--all-repos` | Read every repository the daemon serves, plus `global`, as one list ordered by time. |
| `--follow` | Keep printing. With `--json`, prints one `{event, cursor}` object per line. |

Each line shows the event's time, its repository (a 16-character ID, or `global`), its
sequence number in that repository, its type, its worktree, its source and its payload.
Sequence numbers count per repository, so two repositories repeat them. Use the ID from a
line as `REPO` in `--topic`. A topic that names another repository is accepted only with
`--all-repos`, and only for a repository the daemon serves.

The cursor has one entry per repository, for example
`v1:3f2a9c1b7d4e5f60=42,global=7`. A repository missing from the cursor starts at its
oldest retained event. A cursor older than a repository's retained events is rejected;
start again without `--since`.

The command only reads. It never writes to a store or takes its lock. `--follow` first
starts the daemon the same way as `agentflow ui --no-open`, without opening a browser,
because the daemon runs the watcher. With `--all-repos`, `--follow` reads the list of
served repositories again every 5 seconds. When a repository starts or stops being served,
it rebuilds the combined reader from the cursor it has reached, so no event repeats or
goes missing.

The terminal user owns every repository the daemon serves, so `--all-repos` crosses no
trust boundary. The MCP tool `agentflow_events` does not offer it: agents work in one
repository. It takes `since`, `types`, `topics` and `limit`, reads this repository and
`global`, and returns `{repo, events, cursor, more}`. It has no follow mode: call it again
with the returned `cursor`.

## MCP configuration reload

A running MCP server (`agentflow mcp`) picks up configuration changes without a restart.
It rebuilds its configuration from `.agentflow/config.yml`, `.agentflow/providers/` and the
same two files in `~/.agentflow/`, in two ways:

- **From events.** The server follows this repository's store and the `global` store, as
  a reader, and reloads when an event reports a change to its own worktree's repository
  layer or to the personal layer. A burst of events, such as an editor's save, makes one
  reload, 250 ms after the last event. The server never starts the daemon for this.
- **Before each tool call.** The server compares the size, modification time and file
  identity of those files with the ones it last loaded, and reloads first when they
  differ. This keeps it correct while no daemon runs, when no event arrives.

A reload keeps the approval key and prompt owner of the session. A tool call that is
already running finishes with the configuration it started with; the next call uses the new
one. A confirmation token issued before a reload is checked against the new policy when it is
used. If the new files do not load, the server keeps the old configuration and the next
tool result says so once; it tries again after the next edit. Each reload, and each
failed reload, adds a one-line notice to the next tool result.

A reload does not change what the server told the client when the session started:

- The server instructions and the tool list stay as they were. When a reload changes the
  selected providers, the next tool result asks you to restart the AI session.
- A server that started in configuration recovery mode (the configuration did not load)
  stays in recovery mode. Restart the session after you repair the file.

## Event types

| Type | When |
| --- | --- |
| `git.head.changed` | `HEAD` changes, or the branch it points to moves (a commit) |
| `git.index.changed` | the index is rewritten (staging) |
| `git.refs.changed` | a ref or `packed-refs` changes |
| `git.worktrees.changed` | a linked worktree is added or removed |
| `git.operation.changed` | a rebase, merge, cherry-pick, revert or bisect starts or ends |
| `config.layer.changed` | a file in `.agentflow/` or `.agentflow/providers/` changes (layer `repo`), or `config.yml` or `providers/*.yml` in your agentflow home changes (layer `personal`, in `global`) |
| `client.settings.changed` | an AI client's settings file changes: in your home directory (scope `user`, in `global`) or in a worktree (scope `repo`) |
| `approval.requested` | an operation starts waiting for a person's approval |
| `approval.resolved` | a waiting or approved operation is approved, cancelled or used |
| `approval.expired` | a waiting or approved operation passes its expiry unused |

## Approvals, personal configuration and client settings

Besides each worktree, the daemon watches a few directories in your own home. It watches
each non-recursively, only when you own it and it is not a symbolic link, and polls
where notifications are unavailable. One exception: a client directory such as `~/.claude`
may be a symbolic link that you own to a directory that you own (a dotfiles repository,
for example). The daemon then watches the real directory and reports its files under the
link's name. A link to a directory of another user is not watched, and the daemon says so
on stderr.

| Where | Files | Event |
| --- | --- | --- |
| the agentflow home (`AGENTFLOW_HOME`, normally `~/.agentflow`) | `config.yml` | `config.layer.changed`, layer `personal` |
| `<home>/providers` | `*.yml`, `*.yaml` | `config.layer.changed`, layer `personal` |
| `<home>/approvals` | one record per operation | `approval.*`, in the record's repository |
| `~/.claude`, `~/.codex`, `~/.cursor`, `~/.gemini`, `~/.grok` | the client's user settings file (`settings.json`, `config.toml` or `mcp.json`) | `client.settings.changed`, scope `user` |
| each worktree root and its `.claude`, `.codex`, `.cursor`, `.gemini`, `.grok` and `.vscode` | the files `agentflow init` writes (`.mcp.json`, `settings.json`, `settings.local.json`, `config.toml`, `mcp.json`) | `client.settings.changed`, scope `repo`, in the worktree |

Events name files, never their content. Paths are relative to the home, the agentflow home
or the worktree, and a payload carries at most 20 of them. The daemon compares file
content, so saving a file without a change reports nothing.

**Approvals.** A record's name carries the repository it belongs to, so each event goes to
that repository only; the daemon ignores records of repositories it does not serve. When
the daemon starts to serve a repository it reads the existing records silently: an
approval that was already waiting shows in the console's approvals list, and its later
changes arrive as events. An approval that is asked for and answered within a fraction of a
second, as `--yes` does, still produces `approval.requested` before its
`approval.resolved`. The events carry the operation, its action and class, the client
that owns the prompt, and the issue and expiry times. They never carry the approval token,
the request fingerprint or the prompt owner's session.

The journal marks an approval expired only when a process next touches it. The daemon does
not wait for that: when an open approval passes its expiry it publishes `approval.expired`
with `observed: true`, and the journal's own later write adds nothing.

**One agentflow home.** The daemon watches the `AGENTFLOW_HOME` it was started with. An
agent that runs with another home writes its approvals there, and they never reach the
console. Start the console from a shell that uses the same home as your agents. The daemon
holds a lock on `console.lock` in the home it watches, and releases it when it stops.
`agentflow doctor` warns (`!`, not a failed check) when the console daemon uses a different
`AGENTFLOW_HOME` from the shell you run it in, and prints nothing about it when no daemon runs.

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

## Events from operations

Operations that run through agentflow's command registry record `op.*` events, from the
CLI and from MCP tools alike. So far these are `policy explain`, `policy status`,
`policy approve` (`agentflow_policy_explain`, `agentflow_approval_status`,
`agentflow_approval_approve`) and `eod post` (`agentflow_eod_post`); the other commands
follow during phase 3. A read, such as `policy explain`, records nothing. A write records:

| Event | When |
| --- | --- |
| `op.requested` | the request passed validation and is about to be gated and run |
| `op.awaiting_approval` | it waits for a confirmation or a shared approval |
| `op.suggested` | the policy only suggests it; nothing ran |
| `op.prepared` | a prepare-only request produced a candidate |
| `op.applied` | it ran |
| `op.failed` | it returned an error or a refusal, with a fixed code such as `invalid_token` |

Every event of one operation has the operation ID (`op_` and 32 hex characters, the
approval ID once the gate issues one) as its correlation ID, so a request, its
confirmation and the resumed call group together. The payload names the operation, its
policy action and class, the client, and a one-line subject. It never carries a confirm
token, a message body, error text or a path.

A command hands its events to the store like a hook: it appends them when the store
exists and no daemon owns it, sends them over the notify socket while the daemon owns it,
and otherwise drops them with one warning on standard error. It never creates a store.
When the daemon has not registered the worktree yet, the event is sent again as a
repository-wide event.

`policy approve` also writes one line to `audit.log` with its operation and operation ID.
Commands whose own code already writes an audit line, such as `eod post`, write no extra
one.

The notify socket accepts only `hook.ran`, `commit.created` and `op.*`, only from a process that
proves it can read the daemon's private registry file (`~/.agentflow-ui/`), and only for
repositories and worktrees the daemon serves. On Unix it is a socket file with mode
0600 in that private directory, and the daemon also checks the connecting process's
user ID. On Windows, and when the socket path would be too long, it is a loopback TCP
port on 127.0.0.1 with the same key check.

## GitHub polling

While the daemon runs, it polls GitHub for every repository it serves that has a GitHub
remote, and records what changed. It signs in like every other GitHub call: the `gh` login,
or `GH_TOKEN` or `GITHUB_TOKEN`. It only reads. A repository without a GitHub remote is
not polled.

| Type | When |
| --- | --- |
| `gh.pr.updated` | an open pull request changed, or one left the open list because it was merged or closed |
| `gh.ci.updated` | the checks of a pull request head changed. Only heads of branches that a served worktree has checked out are watched. |
| `gh.issue.updated` | an issue bound to a served worktree (`agentflow start --worktree` records it) changed |
| `gh.health.changed` | the first poll of a repository finished, or its polling health changed (`ok`, `rate_limited`, `unavailable`, `disabled`). Transient: not stored, so `agentflow events` does not show it; the console's `activity` state follows it. |

An event is published only when a value changed. The first poll after the daemon starts, and
the first poll of a repository, record the current state and publish no `gh.pr`, `gh.ci`
or `gh.issue` event; they publish `gh.health.changed`, so the console leaves `unavailable`.
A repository without a GitHub remote reports `disabled`. An event
belongs to the worktree whose branch is the pull request head, or that is bound to the
issue; it is repository-wide when no served worktree matches. Titles, labels and check
names are cut at 256 bytes, and lists at 20 entries.

**Requests.** Each poll cycle of a repository sends conditional requests (`If-None-Match`),
and GitHub does not count a `304 Not Modified` against the rate limit. A quiet repository
costs these requests per cycle, all answered with 304:

| Request | Count |
| --- | --- |
| open pull requests (50 most recently updated) | 1 |
| check runs and commit status of a tracked head | 2 for each head |
| bound issue | 1 for each issue |

Two more kinds of request happen only when something changed. One GraphQL query asks for the
review decision and merge state of the pull requests that changed in this cycle, never of
the others. One lookup asks how a pull request that left the open list ended.

**Interval.**

| Situation | Time to the next poll |
| --- | --- |
| A tracked check is queued or running, or a tracked pull request changed in the last 10 minutes | 15 seconds |
| Otherwise | 1 minute |
| No console stream is open for the repository | 5 minutes |
| `X-Poll-Interval` from GitHub | a floor for all of these |
| Less than 20% of the rate limit remains | stretched so the rest lasts until it resets |
| `Retry-After`, or no requests left | until GitHub allows, for every repository |
| Failures in a row | 1, 2, 4, ... minutes, at most 15 |

`agentflow events --follow` does not count as a stream here, because it reads the store
files and the daemon cannot see it. Only the console's event streams keep the faster
intervals.

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
The daemon stores `global` events, so a reconnecting stream replays them like repository
events. The store is `~/.agentflow-ui/<host-hash>.events/`, from the OS user's profile
(never `AGENTFLOW_HOME` or `$HOME`), one per host, with the same segments and retention as a
repository store. The daemon refuses it unless it is a directory owned by this OS user that
no one else can read: mode `0700` on Unix, an owner-only DACL inherited from
`~/.agentflow-ui` on Windows. Without it, `global` events still reach the console live
but are not replayed.

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
after 2 seconds. `POST /api/tabs/leave` detaches the tab at once. A tab that the
console no longer lists attaches again. When the worktree has no event stream (the stream
answers 503), the attach answers 503 too, so `agentflow ui` opens a new tab instead of
waiting for one that cannot receive the navigation. A navigation that no tab acknowledges
within 2 seconds also opens a new tab. The older `GET /api/tabs/events` stream is removed.

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
