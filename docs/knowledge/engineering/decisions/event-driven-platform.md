---
type: Decision
title: Event bus, message store and event stream
description: The contracts for agentflow's event envelope, in-process bus, per-repository message store, file watcher and the console's SSE stream.
tags: [architecture, events, webui]
x-agentflow-ref:
  scope: engineering
  id: event-driven-platform
x-agentflow-links:
    - title: "Phase 1 contract: event bus, store and SSE ADR"
      url: https://github.com/ryanhelms/agentflow/issues/14
      ref: null
---
# Event bus, message store and event stream

## Status

Accepted for phase 1 of the ACP platform program. Later phases amend this ADR before they add event types or change a contract. A lane that needs a change stops and asks the coordinator for an amendment; it never changes an exported contract on its own.

## Context

agentflow pulls state on demand. The console refreshes only when you press a button, and nothing watches git or configuration. Phase 1 makes the workspace overview live: a branch switch, a commit or an edit to `.agentflow/config.yml` shows in `agentflow ui` within one second, and `agentflow events --follow` prints the same events.

Five lanes build phase 1 in parallel. They need fixed contracts and separate files. The Go types in `internal/bus`, `internal/bus/store`, `internal/watch` and `internal/webui/projection.go` are the code form of this ADR. Where this text and the code differ, fix the code to match this text.

## Decision

### Patterns used

| Piece | GoF / EIP pattern | Go |
| --- | --- | --- |
| Event | Event Message in an Envelope Wrapper | `bus.Event` |
| Fan-out to subscribers | Publish-Subscribe Channel (GoF Observer) | `bus.Publisher`, `bus.Subscriber` |
| Work items | Point-to-Point Channel, Competing Consumers | `bus.Queue` |
| Validation, sequencing | Pipes and Filters (GoF Chain of Responsibility) | `bus.Interceptor` |
| Audit | Wire Tap | `bus.Tap` |
| Failed handlers | Dead Letter Channel | `bus.DeadLetter`, `bus.Handle` |
| Topic filters | Message Filter | `bus.Topic`, `bus.Pattern` |
| History | Message Store | `store.Store` |
| Resume after reconnect | Durable Subscriber | `bus.Cursor` |
| fsnotify source | Event-Driven Consumer | `watch.Source` in `ModeNotify` |
| Polling source, `events --follow` | Polling Consumer | `watch.Source` in `ModePoll`, `store.Follower` |
| Debounce | Aggregator | lane 1.3, `watch.Batch` |
| Paths to events | Message Translator | `watch.Translator` |
| Console read models | Event Notification with server-side projections | `webui.Projection` |

### Envelope

Every event on the bus, in the store and on the stream is one `bus.Event`. Its JSON form is the store line and the `event` object of a delta frame.

| Field | JSON | Rule |
| --- | --- | --- |
| ID | `id` | UUIDv7 in canonical lowercase form, from `bus.NewID`: 48 bits of Unix milliseconds, then `crypto/rand` bits. Standard library only. |
| Type | `type` | A registered type (below). |
| Source | `source` | `<component>[:<detail>]`: `watch.git`, `watch.config`, `daemon`, `cli`, `mcp:<client>`, `hook:<name>`. |
| Repo | `repo` | `bus.RepoID`: 16 lowercase hex characters, or `global`. Empty only for transient events. |
| Worktree | `worktree` | `bus.WorktreeID`: the console's worktree ID (`main`, or `<dns-label>-<8 hex>` from `inspectWorktree`). Empty for repository-wide events. |
| CorrelationID | `correlation_id` | Groups the events of one operation or causal chain. Optional. |
| CausationID | `causation_id` | ID of the event that directly caused this one. Optional. |
| Seq | `seq` | Position in the repository's store, from 1. Only the store assigns it. 0 (omitted) means not stored. |
| Time | `time` | When the producer saw the change, RFC 3339 in UTC. |
| SchemaVersion | `schema_version` | Envelope version, currently 1. |
| Payload | `payload` | A JSON object, at most 256 KiB. |

`Publish` fills a missing ID, Time, SchemaVersion and Payload (`Event.Normalize`), then checks the rules (`Event.Validate`). It rejects a caller-set Seq.

**RepoID.** The store creates `<common-git-dir>/agentflow/events/repo-id` once, with `O_EXCL` and 8 random bytes in hex. The ID survives moving the clone and leaks no path. `global` is reserved for events that belong to no repository (phase 2 onwards).

**Payload evolution.** Payloads only gain fields. Consumers ignore unknown fields. A breaking payload change needs a new type.

### Event types

Names are `<family>.<subject>.<verb>`, lowercase, dot-separated. **New types are added by amending this ADR first**, then `bus.registry` in `internal/bus/types.go`. `Publish` rejects unregistered types.

Phase 1 types:

| Type | Scope | Payload (`internal/bus/types.go`) | Produced when |
| --- | --- | --- | --- |
| `git.head.changed` | worktree | `GitHeadChanged{ref, oid, previous_ref, previous_oid}` | `HEAD` changes, or the branch HEAD points to moves |
| `git.index.changed` | worktree | `{}` | `index` is replaced |
| `git.refs.changed` | repository | `GitRefsChanged{refs}`; empty `refs` means "rescan" | anything under `refs/`, or `packed-refs` |
| `git.worktrees.changed` | repository | `{}` | an entry under `worktrees/` is added or removed |
| `git.operation.changed` | worktree | `GitOperationChanged{operation, previous}` | `rebase-merge/`, `rebase-apply/`, `MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD` or `BISECT_LOG` appears or goes |
| `config.layer.changed` | any | `ConfigLayerChanged{layer, paths}`; phase 1 uses layer `repo` | `.agentflow/*.yml`, `.agentflow/*.yaml` or `.agentflow/providers/*.y{a,}ml` changes |
| `stream.overflow` | any, transient | `StreamOverflow{subscription, dropped}` | a subscription dropped events (see Overflow) |

Reserved families, each added by its phase's amendment: `approval.*` and `hook.*`, `commit.created`, `gh.*` (phase 2); `op.*` (phase 3); `plugin.*` (phase 5); `harness.*` (phase 6); `ai.session.*` (phase 7).

### Bus

`internal/bus` holds the contracts; lane 1.1 adds `bus.New(Options) Bus`.

```go
type Publisher interface {
    Publish(ctx context.Context, e Event) (Event, error)
}
type Subscriber interface {
    Subscribe(opts SubscribeOptions, topics ...Topic) (Subscription, error)
}
type Subscription interface {
    Events() <-chan Event
    Dropped() uint64
    Close() error
}
type Queue interface { // Point-to-Point Channel
    Send(ctx context.Context, e Event) error
    Receive(ctx context.Context) (Event, error)
}
type Bus interface {
    Publisher
    Subscriber
    Queue(name string) Queue
    Close() error
}
type Interceptor interface {
    Intercept(ctx context.Context, e Event) (Event, error)
}
type Tap interface { Tap(ctx context.Context, e Event) }
type DeadLetter interface {
    Dead(ctx context.Context, l Letter)
    Letters() []Letter
}
func Handle(ctx context.Context, sub Subscription, consumer string, h Handler, dl DeadLetter) error
```

**Publish order.** Normalize, Validate, Interceptors in order (the store interceptor assigns Seq), Taps, then delivery to every matching subscription. Publish calls are serialized, so every subscriber sees events in one order, and within a repository that order is Seq order. Interceptors and Taps must not call Publish.

**Topics.** A `bus.Topic` is `{Repo, Worktree, Types}`, written `<repo>/<worktree>/<pattern>`. Repo and Worktree may be `*`. A topic for one worktree also matches its repository's repository-wide events. In a `bus.Pattern`, `*` matches one segment and a final `**` matches one or more, so `git.**` matches every git event and `**` matches everything.

**Bounded buffers and the overflow rule.** Each subscription has a buffer, 256 events by default (`SubscribeOptions.Buffer`). Publish never waits for a subscriber. When a buffer is full:

1. The bus drops the oldest buffered event and appends the new one.
2. It makes sure exactly one `stream.overflow` event waits at the front of that buffer: it adds one, or raises the `dropped` count of the one already waiting. The marker is never dropped itself.
3. A consumer that reads `stream.overflow` has missed events. It resynchronizes from its source of truth or from the store at its last cursor. The SSE stream sends `resync` and a fresh snapshot.

`stream.overflow` is transient: never stored, Seq 0, sent only to the subscription that overflowed.

**Point-to-Point Channel.** `Bus.Queue(name)` carries work items, for example "recompute the overview of worktree W". Each item goes to exactly one receiver. Queue items skip Interceptors, Taps and the store. `Send` waits while the queue (64 items) is full, until its context ends: back-pressure, not dropping.

**Wire Tap to audit.** Lane 1.1 provides an audit tap over `policy.Gate.Audit`. It records `op.*` and `approval.*` events, which are actions. Observation families such as `git.*` and `config.*` are not audited. In phase 1 no audited type exists yet, so the tap is wired but records nothing.

**Dead Letter Channel.** `bus.Handle` runs a handler per event; an error or a panic becomes a `bus.Letter{event, consumer, reason, time}` and the consumer carries on. The default channel keeps the last 256 letters in memory. Plugins (phase 5) add a persistent one. Overflow drops are not dead letters; `Subscription.Dropped` counts them.

### Message store

`internal/bus/store` holds the contracts; lane 1.2 adds the file store and `store.Open`.

```go
type Reader interface {
    ReadFrom(ctx context.Context, from bus.Cursor) iter.Seq2[bus.Event, error]
    Head(ctx context.Context) (bus.Cursor, error)
}
type Store interface {
    Reader
    Repo() bus.RepoID
    Append(ctx context.Context, e bus.Event) (bus.Event, error)
    Close() error
}
type Follower interface {
    Reader
    Follow(ctx context.Context, from bus.Cursor) iter.Seq2[bus.Event, error]
}
func Interceptor(stores Stores) bus.Interceptor
// lane 1.2:
func Open(ctx context.Context, commonDir string, role Role) (Store, error)
```

**Cursor.** `bus.Cursor` is `map[RepoID]uint64`: per repository, the Seq of the last event seen. One daemon serves many repositories, each with its own store and sequence, so the cursor is a vector. Its string form is the SSE `id:` and the `Last-Event-ID` header:

```text
v1:<repo>=<seq>,<repo>=<seq>
```

Repositories are sorted, Seq is decimal without leading zeros, zero entries are left out, and the empty cursor is the empty string. At most 64 repositories. The characters are `[0-9a-z:=,]`, safe in headers and URLs. `bus.ParseCursor` rejects anything `Cursor.String` would not produce.

**ReadFrom.** It yields stored events the cursor has not seen. Within a repository the order is Seq; across repositories (a union reader) it is `(time, repo, seq)`. A repository missing from the cursor starts at its oldest retained event. A cursor whose next event is no longer retained yields `store.ErrCursorExpired` first and stops. A partial last line is never yielded.

**Single-writer rule.** At any moment at most one process appends to a repository's store. `store.Role` enforces it with `owner.lock` and `internal/filelock`:

| Role | Who | Behaviour |
| --- | --- | --- |
| `RoleOwner` | the webui daemon, and nothing else | Holds `owner.lock` for its lifetime. On open, truncates a partial last line left by a crash and reads the last Seq. Only writer while it runs. `Open` returns `ErrOwned` if another process owns the store. |
| `RoleAppender` | any process that is not the daemon: CLI, MCP and hook processes (phase 2 onwards) | Each `Append` takes `owner.lock` with `filelock.TryExclusive`, appends, releases. If the lock is held it returns `ErrOwned`; the caller sends the event to the daemon over the local socket. |
| `RoleReader` | `agentflow events` (with or without `--follow`), MCP `agentflow_events` | Never writes, never locks. |

**Dropped events in phase 1.** Until the local socket exists, an appender that gets `ErrOwned` drops the event with a warning on stderr. This is accepted for phase 1, where only the daemon produces events. Events are notifications, and the next snapshot repairs a missed one. Phase 2 lane 2.2 replaces the drop with a send over the socket and adds a test for that path.

**Layout.**

```text
<common-git-dir>/agentflow/events/
  repo-id                     16 hex characters, created once
  owner.lock                  held by the owner
  00000000000000000001.jsonl  segment named by its first Seq, 20 digits
  00000000000000004711.jsonl  active segment: the highest name
```

Each line is one `bus.Event` in JSON, ending in `\n`. One line stays below 1 MiB because payloads are capped at 256 KiB. Events for `global` use the same layout under `~/.agentflow/events/` (phase 2).

**Rotation.** The owner closes the active segment and starts a new one when it reaches 8 MiB or its first event is 24 hours old. A segment is fsynced when it closes and when the store closes, not per line.

**Retention.** When a segment closes and when the owner opens the store, it deletes the oldest closed segments while the store is larger than **50 MB**, and any closed segment whose newest event is older than **7 days**. The active segment is never deleted.

**Replay limit.** A stream replays at most 1000 events (`store.MaxReplay`) from a cursor. Beyond that it sends `resync` and a snapshot.

**The store directory is never watched.** `watch.Excluded` covers `<common-git-dir>/agentflow/**`, so appending an event can never cause another event. The watcher also never watches `objects/`, `logs/`, `hooks/`, `lfs/`, `modules/` or `*.lock`.

### Watch

`internal/watch` holds the contracts; lane 1.3 adds the sources, the Aggregator, the Translator and the Watcher. Lane 1.3 also adds the only new dependency, `github.com/fsnotify/fsnotify` v1.10.1. This contract PR does not add it: nothing imports it yet, `go mod tidy` would remove an unused requirement, and a blank import only to keep it would be dead code.

```go
type Source interface {
    Mode() Mode // ModeNotify or ModePoll
    Add(dir string) error
    Remove(dir string) error
    Changes() <-chan Change
    Errors() <-chan error
    Close() error
}
type Translator interface {
    Translate(ctx context.Context, b Batch) ([]bus.Event, error)
}
type Watcher interface {
    Add(t Target) error
    Remove(repo bus.RepoID, worktree bus.WorktreeID) error
    Mode(repo bus.RepoID, worktree bus.WorktreeID) (Mode, bool)
    Run(ctx context.Context, pub bus.Publisher) error
}
func Classify(t Target, path string) (bus.Type, bool)
func Excluded(commonDir, path string) bool
func WorktreePaths(t Target) []Path
func RepoPaths(commonDir string) []Path
```

**Event-Driven Consumer and Polling Consumer.** The default Source uses fsnotify (inotify, kqueue, ReadDirectoryChangesW). It watches directories, not files, because git replaces files by renaming a lock file over them. When `Add` fails with `ErrUnsupported` (watch limits such as `ENOSPC` or `EMFILE`, or a file system without notifications), the Watcher moves that Target to a polling Source. `AGENTFLOW_WATCH=poll` forces polling. Polling stats files every 500 ms and walks `refs/` every 2 s, at most 10,000 entries; past that it polls only `refs/heads` and `packed-refs`. An fsnotify queue overflow arrives on `Errors()`, and the Watcher then rescans every Target.

**Aggregator.** Changes are grouped per Target. A Batch is released after 100 ms without a new change, or 400 ms after its first change, whichever comes first. With the projection's own work this keeps the overview within one second.

**What phase 1 watches.**

| Directory | Once per | Signals |
| --- | --- | --- |
| the worktree's git dir | worktree | `HEAD`, `index`, `rebase-merge/`, `rebase-apply/`, `MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`, `BISECT_LOG` |
| `<root>/.agentflow`, `<root>/.agentflow/providers` | worktree | `*.yml`, `*.yaml` (not `.agentflow/bin/`) |
| the common git dir | repository | `packed-refs` |
| `<common>/refs`, recursively | repository | every ref |
| `<common>/worktrees` | repository | entries added or removed |

`watch.Classify` maps a path to its event type. It checks the worktree's git dir before the common dir, because a linked worktree's git dir lies under `<common>/worktrees/`.

**Translator.** It keeps, per Target, HEAD's ref and OID and the current git operation. It fills the `previous_*` fields and skips changes that change nothing. After `git.refs.changed` it resolves HEAD again and also emits `git.head.changed` when the OID moved, because a commit on the current branch writes only the branch ref.

### Projections

Phase 1 events are notifications, not the source of truth. A projection reads git and configuration again when an event may have changed its state. A missed event is repaired by the next one or by a snapshot.

```go
type Projection interface {
    Name() string
    Topics() []bus.Topic
    Keys(e bus.Event) []ProjectionKey
    State(ctx context.Context, key ProjectionKey) (json.RawMessage, error)
}
```

The projector (lane 1.4) subscribes once, caches each key's last state, coalesces work per key, and sends a state only when its JSON changed. A key that no longer exists gets state `null`. Phase 1 has one projection, `overview`, keyed by worktree. Lane 1.4 defines its JSON in `projection_overview.go`: branch, HEAD OID, detached, upstream ahead and behind, staged, unstaged and untracked counts, git operation, bound task, and configuration validity. Changing that shape later needs an amendment.

*Amended after phase 1 integration (#29).* The projector coalesces by draining the events already waiting on its subscription, up to a batch cap, and recomputing each affected key once per pass. It does not use a `bus.Queue`. The `overview` state is:

| Field | Meaning |
| --- | --- |
| `branch` | Branch name; `""` when detached. |
| `head` | Short OID, as `/api/state` `head`; `""` before the first commit. |
| `oid` | Full OID. |
| `detached` | HEAD is detached. |
| `upstream` | `{name, ahead, behind}`; omitted when the branch has no upstream. |
| `changed` | Paths with any change, as `/api/state` `dirty_count`. |
| `staged`, `unstaged`, `untracked` | Counts. |
| `operation` | `""`, `rebase`, `merge`, `cherry-pick`, `revert` or `bisect`. |
| `task` | Bound task, omitted when none. |
| `config` | `{valid, problems}`. |

`GET /api/events` answers 503 when the console has no event store for its repository. The client keeps retrying.

### SSE wire format

`GET /w/<id>/api/events` on each worktree's console origin.

**Request.**

- `Authorization: Bearer <session>`: the existing tab session, checked by the console's normal `authorized` check. The browser reads the stream with `fetch` and a body reader, not `EventSource`, exactly as `static/tab-navigation.js` does today, so the credential never appears in a URL or log.
- `Accept: text/event-stream`.
- `topic=<repo>/<worktree>/<pattern>`, repeated, at most 64. `*` as the repo means "this console's repository".
- `Last-Event-ID: <cursor>`, optional, sent by the client itself.

**Scope.** A stream authenticated on one worktree may follow every worktree of the same repository. This is the existing trust boundary, not a new one: the console key is already shared per clone and origin (one remember key per clone and origin), so that browser can already resume a session for any of those worktrees. A stream never sees another clone or another console host. The server refuses with 403 any topic outside the authenticated repository.

*Amended after phase 1 integration (#29).* A `Last-Event-ID` with an entry for any other repository is also refused with 403, before the stream opens. So the server never sends a `resync` with reason `topics` (`ResyncTopics`) for it; that reason stays reserved.

**Frames.** Every frame is one SSE event whose data is one line of JSON (`internal/webui/projection.go`):

```text
retry: 1000

event: snapshot
id: v1:3f2a9c1b7d4e5f60=42
data: {"cursor":"v1:3f2a9c1b7d4e5f60=42","states":[{"projection":"overview","repo":"3f2a9c1b7d4e5f60","worktree":"main","state":{...}}]}

event: delta
id: v1:3f2a9c1b7d4e5f60=43
data: {"event":{...envelope...},"states":[{"projection":"overview","repo":"3f2a9c1b7d4e5f60","worktree":"main","state":{...}}]}

event: resync
data: {"reason":"overflow"}

: ping
```

| Frame | `id:` | Meaning for the client |
| --- | --- | --- |
| `snapshot` | the cursor the snapshot reflects | Replace the whole store with `states`. |
| `delta` | the cursor after this event | Apply `states` (replace each key; `null` removes it). Pass `event` to timelines. Transient events carry the previous cursor. |
| `resync` | none | Discard state. A `snapshot` follows on the same connection. Reasons: `overflow`, `cursor_expired`, `replay_limit`, `topics`. |
| `: ping` | none | Heartbeat every 15 s. |

**Connect sequence.** The server subscribes to the bus first, then reads the store heads and builds the snapshot, then sends buffered live events the snapshot cursor has not seen. Nothing falls between the snapshot and the first delta.

- Without `Last-Event-ID`: `snapshot`, then deltas.
- With a valid `Last-Event-ID`: the missed events as deltas without states (replay), then a `snapshot`, then live deltas.
- With an expired cursor or more than 1000 missed events: `resync`, `snapshot`, then deltas.
- When the stream's own subscription overflows: `resync` with reason `overflow`, `snapshot`, then deltas.

**Topic multiplexing.** One connection carries every topic a browser needs, across all worktrees of the repository. For example, `*/*/git.**` and `*/*/config.**` follow every worktree.

*Amended after phase 1 integration (#29).* In phase 1, `agentflow events` reads only the current repository's store, and a topic naming another repository is an error. Following many repositories with one cursor (through `store.Union`) moves to phase 2.

**One stream per browser.** Browsers allow about 6 HTTP/1.1 connections per host. Every worktree of a repository shares one console host, and the old tab stream already has to give its connection back every 5 seconds to avoid starving other requests. So the browser opens **one** event stream per console origin, in a `SharedWorker` (`static/event-worker.js`), and every tab of that origin shares it. Where `SharedWorker` is unavailable, each tab opens its own stream. The page posts its session credential to the worker; on 401 the worker asks the pages for a fresh one through the existing `unauthorized` flow. The worker reconnects with `Last-Event-ID` and a backoff from 250 ms to 10 s, and changes its topics by reconnecting with the union of its tabs' topics. The server allows 8 streams per origin and answers 429 beyond that.

### `agentflow events` CLI and MCP

Lane 1.4 adds both, over one `internal/app` method:

- CLI: `agentflow events [--since CURSOR] [--type PATTERN] [--limit N] [--follow] [--json]`. It prints events and, at the end, the cursor to resume from. `--follow` keeps printing until interrupted. It never becomes the store's owner. It first makes sure the daemon is running, through the existing daemon start path in `internal/webui` (`LaunchCLI` and the `ServeChild` serve child) without opening a browser, then follows the store as `RoleReader` (`store.Follower`). The daemon runs the watcher.
- MCP: `agentflow_events` with `since`, `types` and `limit`, returning `{events, cursor}`. It has no follow mode; callers poll with the returned cursor.

Both are read-only and need no confirmation.

### File ownership for phase 1 lanes

A lane changes only the files it owns. Shared files have one owner. A lane that needs another lane's file stops and reports.

| Lane | Owns | Must not touch |
| --- | --- | --- |
| 1.1 Bus | `internal/bus/*.go` (adds `New`, the audit tap in `audit.go`), `internal/bus/bustest/**` | `internal/bus/store/**` |
| 1.2 Store | `internal/bus/store/**` (file store, `Open`, union reader, `storetest`) | `internal/bus/*.go` |
| 1.3 Watcher | `internal/watch/**`, `go.mod`, `go.sum` (fsnotify only) | `internal/webui/**` |
| 1.4 Stream, projections, CLI and MCP | `internal/webui/stream*.go`, `internal/webui/projection*.go`, the event wiring in `internal/webui/daemon.go`, `server.go`, `instance.go` (event runtime start-up in the serve child) and `daemon_picker.go` (the origin-wide `event-worker.js` route) (route, bus, store and watcher start-up), `internal/app/events*.go`, `internal/cli/events*.go`, **`internal/cli/cli.go`, `internal/mcpserver/server.go`, `internal/cli/parity_test.go`**, `adapters/claude-code/skills/events/SKILL.md`, `README.md`, `docs/events.md`, the blueprint capability table | `internal/webui/static/**` |
| 1.5 Browser client | `internal/webui/static/event-worker.js`, the overview part of `internal/webui/static/app.js`, the script tag in `static/index.html`, overview rules in `static/style.css` | any Go file |

Lane 1.4 is the only phase 1 lane that may edit `cli.go`, `mcpserver/server.go` or `parity_test.go`. Exported contract types (`Event`, `Cursor`, `Topic`, `Pattern`, the type registry and payloads, every interface in this ADR, `webui.Projection` and the frame types) change only through an amendment to this ADR. Moving the worktree ID derivation out of `inspectWorktree` in `daemon.go`, so the CLI can compute worktree IDs without a daemon, is lane 1.4's job.

**Security review (lane 1.4, before merge).**

- [ ] No cross-clone or cross-host leakage: a stream only ever carries events, states and cursor entries of the repository its session belongs to.
- [ ] A topic naming another repository, or a `Last-Event-ID` covering one, never widens what the stream sends. Topics outside the authenticated repository get 403.

## Consequences

- The console can show changes as they happen, and later phases add sources (approvals, GitHub, harnesses, AI sessions) without new plumbing.
- Each repository's git dir gains an `agentflow/events/` directory of at most about 50 MB. It is local, never committed, and removed with the clone.
- Events are notifications. Missing one never corrupts state; it only delays an update until the next event or snapshot.
- Only the daemon owns a store. Other processes cannot write events while it runs until the local socket arrives in phase 2 (lane 2.2). Phase 1 needs none: only the daemon produces events, and `events --follow` starts the daemon when it is not running.
- One stream per browser origin keeps the HTTP/1.1 connection pool free for the console's other requests.
- The fsnotify dependency is the only new dependency of the program.
