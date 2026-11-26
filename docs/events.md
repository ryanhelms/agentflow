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

An event is published only when a value changed. The first poll after the daemon starts, and
the first poll of a repository, record the current state and publish nothing. An event
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
request uses the tab's `Authorization: Bearer` session, repeated `topic=` parameters and,
on reconnect, `Last-Event-ID`.

| Rule | Result |
| --- | --- |
| No or expired session | 401 |
| A malformed topic, more than 64 topics, or a malformed `Last-Event-ID` | 400 |
| A topic or `Last-Event-ID` naming another repository | 403 |
| More than 8 streams on one console origin | 429 |
| The console has no event store for its repository | 503; the browser keeps retrying |

A stream carries only events, states and cursor entries of the repository the session
belongs to. It may follow every worktree of that repository.

Frames: `snapshot` (replace all state), `delta` (one event and the states it changed;
`null` removes a key), `resync` (discard state; a `snapshot` follows) and a `: ping`
comment every 15 seconds. The `id:` of `snapshot` and `delta` frames is the cursor. The
overview projection's state per worktree (its `worktree` is the `<id>` of `/w/<id>/`) has
these fields: `branch`, `head`, `oid`, `detached`, `upstream` (`name`, `ahead`, `behind`;
absent without an upstream), `changed`, `staged`, `unstaged`, `untracked`, `operation`,
`task` and `config` (`valid`, `problems`).

The browser shares one stream per console origin through a `SharedWorker` loaded from
`/_agentflow/event-worker.js`.

The watcher uses file notifications and falls back to polling per worktree when
notifications are unavailable. Set `AGENTFLOW_WATCH=poll` to force polling.
