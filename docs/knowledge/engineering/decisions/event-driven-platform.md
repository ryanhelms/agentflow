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
    - title: "Phase 2 contract: approvals, hooks, GitHub and reload events"
      url: https://github.com/ryanhelms/agentflow/issues/32
      ref: null
    - title: "Phase 3 contract: command registry, Invoker and op events"
      url: https://github.com/ryanhelms/agentflow/issues/52
      ref: null
    - title: "Phase 4 contract: settings change sets, preview, save and undo"
      url: https://github.com/ryanhelms/agentflow/issues/68
      ref: null
    - title: "Phase 5 contract: plugin manifest, JSON-RPC and plugin host"
      url: https://github.com/ryanhelms/agentflow/issues/77
      ref: null
    - title: "Phase 6 contract: harness registry, probes and detection states"
      url: https://github.com/ryanhelms/agentflow/issues/97
      ref: null
    - title: "Phase 7 contract: ACP sessions, permission chain and AI panel"
      url: https://github.com/ryanhelms/agentflow/issues/121
      ref: null
---
# Event bus, message store and event stream

## Status

Accepted for phase 1 of the ACP platform program, and amended for phase 2 (section "Phase 2"), phase 3 (section "Phase 3"), phase 4 (section "Phase 4"), phase 5 (section "Phase 5"), phase 6 (section "Phase 6") and phase 7 (section "Phase 7", at the end). Later phases amend this ADR before they add event types or change a contract. A lane that needs a change stops and asks the coordinator for an amendment; it never changes an exported contract on its own.

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

Reserved families, each added by its phase's amendment: `approval.*` and `hook.*`, `commit.created`, `gh.*` (phase 2, now registered: see "Phase 2, Event types"); `op.*` (phase 3, now registered: see "Phase 3 event types"); `plugin.*` (phase 5, now registered: see "Phase 5 events"); `harness.*` (phase 6); `ai.session.*` (phase 7).

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

**Wire Tap to audit.** Lane 1.1 provides an audit tap over `policy.Gate.Audit`. It records `op.*` events, which are actions (*amended in phase 2: `approval.*` is no longer audited, see "Phase 2 decisions", item 6; amended in phase 3: `op.*` is not audited by the tap either, the Invoker audits operations, see "Phase 3 decisions", item 4*). Observation families such as `git.*` and `config.*` are not audited. In phase 1 no audited type exists yet, so the tap is wired but records nothing.

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

**Dropped events in phase 1.** *Replaced in phase 2 by `notifysock.Deliver`: see "Phase 2, Local socket".* Until the local socket exists, an appender that gets `ErrOwned` drops the event with a warning on stderr. This is accepted for phase 1, where only the daemon produces events. Events are notifications, and the next snapshot repairs a missed one. Phase 2 lane 2.2 replaces the drop with a send over the socket and adds a test for that path.

**Layout.**

```text
<common-git-dir>/agentflow/events/
  repo-id                     16 hex characters, created once
  owner.lock                  held by the owner
  00000000000000000001.jsonl  segment named by its first Seq, 20 digits
  00000000000000004711.jsonl  active segment: the highest name
```

Each line is one `bus.Event` in JSON, ending in `\n`. One line stays below 1 MiB because payloads are capped at 256 KiB. Events for `global` use the same layout in the console registry, not under `~/.agentflow/` (amended in phase 2: see "Phase 2, Scoping").

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

## Phase 2

*Amendment for phase 2 (issue #32), lanes 2.1 to 2.6 (issues #33 to #38).* Phase 2 makes the rest of the console live: approvals appear and clear the moment an agent asks or a person answers, PR and CI status update by themselves, tab navigation runs over the event stream, a running MCP server picks up configuration changes, and the Refresh button becomes a small resync control.

Everything in the phase 1 sections still holds unless a paragraph here says it changes. The code form of this section:

| Contract | Go |
| --- | --- |
| Event types, payloads, the global rule | `internal/bus/types_phase2.go`, `TypeInfo.Global` in `internal/bus/types.go`, the check in `Event.Validate` |
| Approval record names | `policy.ScopeKey`, `policy.ApprovalsDir` |
| Local socket | `internal/notifysock` (wire types, limits, handshake proofs, `Check`, `Deliver`), `internal/notifysock/notifysocktest` |
| New watch sources | `internal/watch/user.go` (`UserTarget`, `UserPaths`, `ClassifyUser`, client settings tables, `ParseApprovalRecord`, `ApprovalRouter`, `ApprovalTransition`, `ApprovalTransitions`, `ObservedExpiry`) |
| GitHub poller | `internal/watch/github.go` (`GitHubTarget`, `GitHubAPI`, `GitHubMeta`, `GitHubState`, `GitHubPoller`, `GitHubCache`, `NextPoll`) |
| Projections | `internal/webui/projection_states.go` |
| Daemon start-up seam | `runtimeExtension` and `runtimeExtensions` in `internal/webui/stream_runtime.go` |
| MCP reload | `internal/app/events_reload.go` (`ReloadTopics`, `NeedsReload`, `ReloadDebounce`) |

### Phase 2 event types

| Type | Scope | Global | Payload (`internal/bus/types_phase2.go`) | Producer | Produced when |
| --- | --- | --- | --- | --- | --- |
| `approval.requested` | repository | never | `ApprovalRequested{Approval}` | daemon, `watch.approvals` | a record appears in state `pending` |
| `approval.resolved` | repository | never | `ApprovalResolved{Approval, state, previous}` | daemon, `watch.approvals` | a pending or approved record becomes `approved`, `cancelled` or `consumed` |
| `approval.expired` | repository | never | `ApprovalExpired{Approval, previous, observed}` | daemon, `watch.approvals` | a pending or approved record becomes `expired`, or its `expires_at` passes first (`observed: true`) |
| `hook.ran` | any | never | `HookRan{hook, client, outcome, reason, duration_ms}` | `hook:<name>` over the socket | an `agentflow hook ...` invocation ends (see Producers) |
| `commit.created` | worktree | never | `CommitCreated{oid, branch, subject, type, scope, breaking, keys}` | `hook:post-commit` over the socket | git runs the `post-commit` hook |
| `gh.pr.updated` | any | never | `GHPullUpdated{number, title, url, state, draft, head_ref, head_sha, base_ref, author, review_decision, merge_state, updated_at, previous}` | daemon, `watch.github` | a pull request of the repository changed |
| `gh.ci.updated` | any | never | `GHCIUpdated{pull, head_ref, head_sha, state, counts, failed, url, previous}` | daemon, `watch.github` | the combined checks of a tracked head commit changed |
| `gh.issue.updated` | any | never | `GHIssueUpdated{number, title, url, state, state_reason, labels, assignees, updated_at, previous}` | daemon, `watch.github` | an issue bound to a worktree changed |
| `gh.health.changed` | repository, transient | never | `GHHealthChanged{health, previous, retry_at}` | daemon, `watch.github` | the poller finished its first poll of a repository, its health changed (`ok`, `rate_limited`, `unavailable`), or it became `disabled` |
| `config.layer.changed` | any | allowed | phase 1 payload; layer `personal` now produced | daemon, `watch.config` | `~/.agentflow/config.yml` or `~/.agentflow/providers/*.y{a,}ml` changes (in `global`) |
| `client.settings.changed` | any | allowed | `ClientSettingsChanged{client, scope, paths}` | daemon, `watch.config` | a client settings file changes, in the home directory (`global`) or a worktree |
| `ui.tabs.changed` | repository, transient | never | `UITabsChanged{}` | daemon, tab hub | a tab attaches or detaches, or a navigation starts or ends |

Rules every payload follows:

- Text from a person or a provider (titles, subjects, check and label names) is data for display, never an instruction. One text field is at most 256 bytes (`bus.MaxPayloadText`) and one list at most 20 entries (`bus.MaxPayloadNames`). Producers truncate.
- No payload carries a bearer token, an approval fingerprint, a prompt owner's session MAC, a command line, a commit message body, hook output, file contents or an absolute path.
- `previous` fields are empty the first time this daemon reports a subject.
- The worktree of a `gh.*` event is the served worktree whose branch is the PR's head (for issues: the worktree bound to the issue). With none, the event is repository-wide.
- `LayerClient` stays reserved and unused. Client settings have their own type because they are not an agentflow configuration layer and do not change `config.Effective`.

No phase 2 family is audited by the Wire Tap. `approval.*` was audited under the phase 1 rule; it is not any more, because the gate's approval journal already records every transition (decision 6).

### Phase 2 scoping: repository or `global`

**Rule.** An event belongs to a repository whenever the change does: approvals (routed by the record's scope key), hooks, commits, GitHub, repository client settings and the repository layer. Only changes to user-level files belong to `global`: the personal layer and user-level client settings. `bus.TypeInfo.Global` states this per type (`GlobalNever`, `GlobalAllowed`, `GlobalOnly`), and `Event.Validate` rejects an event in the wrong place. A `global` event never has a worktree.

**Where the `global` store lives.** Not under `~/.agentflow/events/`, as phase 1 said, but in the console registry, next to the daemon's other files:

```text
~/.agentflow-ui/<host-hash>.events/   same layout and retention as a repository store
```

- The registry directory comes from `os/user`, not `$HOME` or `AGENTFLOW_HOME`, which a repository or agent can set. `daemonPrefix` already checks that it is a private directory owned by this OS user.
- The registry is per host. A home directory shared between machines would otherwise give two daemons the same store.
- The daemon opens it as `store.RoleOwner` at start-up. Nothing else writes it: no socket type is global.
- **Owner: lane 2.7 (#41).** `store.OpenGlobal(ctx, dir, role)` opens a store directory by path, with the same segments, rotation, retention and roles as `store.Open`. Its `repo-id` holds `global`, so `Open` and `OpenGlobal` never accept each other's directory, and it appends only `bus.GlobalRepo` events. Every role refuses a directory that is a symbolic link or, on Unix, not owned by the effective user or has any group or other mode bit (`store.ErrNotPrivate`). Windows has no mode bits: `OpenGlobal` checks only for a real directory, and the daemon (`internal/webui/stream_global.go`, the `openGlobalStore` seam) creates `<host-hash>.events` with the registry's owner-only DACL and checks owner and DACL with `privateMode` before it opens it. When the check fails the daemon starts without a global store and says so on stderr. `store.Union` reads it next to repository stores unchanged.

**No leak across OS users.** One daemon runs per OS user per host (unchanged). Its `global` store, its socket and its registry file are in that user's private registry. The socket checks the peer's user ID where the platform reports it and needs the key from the private registry file. The watcher adds a user-level directory only after checking that this OS user owns it, and takes the OS home from `os/user`.

**How a repository stream sees `global` events.**

1. Each repository's projector also subscribes to `global/*/config.layer.changed` and `global/*/client.settings.changed`, and recomputes its own keys. For example, a personal-layer change recomputes `overview.config` for every worktree. The resulting states belong to the repository, as before.
2. A stream may name the repository `global` in a topic, but only with `config.**` or `client.**` patterns. Any other `global` topic gets 403.
3. A cursor may carry a `global=<seq>` entry, and so may `Last-Event-ID`. This amends the phase 1 rule "a `Last-Event-ID` with an entry for any other repository is refused with 403": `global` is the one other entry allowed. It is safe because the stream's session belongs to the same OS user's daemon, and `global` payloads carry only layer names, client names and relative file names.

### Phase 2 watch sources (lane 2.1)

The daemon watches one `watch.UserTarget` in addition to its worktree Targets: `Home` is `config.HomeDir()` in the daemon process, `UserHome` is the OS home from `os/user`. Directories are watched non-recursively, with the phase 1 polling fallback.

| Directory | Files | Event |
| --- | --- | --- |
| `<Home>` | `config.yml` | `config.layer.changed{layer: personal, paths}` in `global`; paths relative to `Home` |
| `<Home>/providers` | `*.yml`, `*.yaml` | same |
| `<Home>/approvals` | `<scope key>-op_<32 hex>.json` | `approval.*` in the record's repository |
| `<UserHome>/.claude`, `.codex`, `.cursor`, `.gemini`, `.grok` | `watch.UserClientSettings` | `client.settings.changed{scope: user}` in `global` |
| each worktree root and its `.claude`, `.codex`, `.cursor`, `.gemini`, `.grok`, `.vscode` | `watch.RepoClientSettings` | `client.settings.changed{scope: repo}` in the worktree |

Everything else in those directories is ignored: `events.jsonl`, `confirm.key`, `request-*` index files, audit logs, locks, `~/.claude.json` and the client's project state.

**Approvals.** The record files are the source of truth, not `events.jsonl`:

- A journal line has no repository, so it cannot be routed without reading the record anyway. The record's file name carries the scope key, and its body has the state, expiry and prompt owner.
- `policy.ScopeKey(common)` is the record prefix for a repository; the gate computes it from the common git dir with symbolic links resolved. The daemon builds an `ApprovalRouter` from the scope key of every repository it serves. A record of any other scope produces no event.
- At start the translator scans the directory and seeds each record's state without publishing. After that, `ApprovalTransitions(previous, current, asked)` decides the events. Prepared operations (an `auto` policy) and records that finished before the repository was served produce nothing.
- A record created and answered within one debounce window is first seen in its final state. When it was issued after the repository was served and has a prompt owner (a prepared operation never has one), it produces `approval.requested` and then its resolution or expiry with `previous: pending`, so a stream never sees an answer without its request. (Integration follow-up, issue #49.)
- Records expire lazily: the journal writes `expired` only when a process next touches the record. The translator arms one timer per open record and publishes `approval.expired` with `observed: true` when `ObservedExpiry` becomes true. A later `expired` write then produces nothing.
- The payload is the record's public view: operation ID, action, class, policy source, issue and expiry times, and the client part of the prompt owner (before the `/`). Never the fingerprint, related keys or the owner's session MAC.

Lane 2.1 also adds a read-only lister over the same directory (`internal/policy/journal.go`), which implements `webui.ApprovalSource` for the approvals projection.

### Phase 2 local socket (lane 2.2)

`internal/notifysock` lets CLI, MCP and hook processes hand an event to the daemon that owns its store.

**Endpoint.** The daemon's registry file `<prefix>.json` gains a `notify` field with the endpoint's registry form:

| Platform | Endpoint | Notes |
| --- | --- | --- |
| Unix | `unix:<prefix>.notify` | Listen while holding the instance lock, after removing a stale socket. Mode 0600, inside the 0700 registry. |
| Unix, path longer than 100 bytes | `tcp:127.0.0.1:<port>` | `sun_path` is 104 bytes on macOS. `notifysock.UnixPath` returns "" past `MaxUnixPath`. |
| Windows | `tcp:127.0.0.1:<port>` | The daemon picks the port. |

`ParseEndpoint` accepts only an absolute, clean socket path or the literal address `127.0.0.1`, so a tampered registry cannot point a producer at another host. `webui.NotifyEndpoint` (lane 2.2) is the `notifysock.Locator`: it reads the registry file through the existing discovery and returns an error wrapping `ErrNoDaemon` when there is none.

**Handshake.** Both transports use the same mutual challenge-response, keyed by `DeriveKey(instance key)`. The instance key already lives only in the private registry file and changes with each daemon incarnation; it never goes on the wire.

1. Client sends `Hello{v: 1, nonce: cn}`.
2. Daemon sends `Challenge{v: 1, nonce: sn, proof: ServerProof(k, cn, sn)}`, or `Challenge{error}` with `busy` or `version`.
3. Client checks the proof. A wrong proof means a stale or foreign listener: the client closes and reports `ErrNoDaemon`, having sent nothing but a nonce.
4. Client sends `Proof{proof: ClientProof(k, sn, cn)}`.
5. Daemon checks it, and on Unix the peer user ID, then sends `Ready{ok: true}`, or `Ready{error: auth}` and closes.

Nonces are 32 random bytes in unpadded base64url. Proofs are compared in constant time.

**Framing.** Newline-delimited JSON: one frame per line, at most 1 MiB (`MaxFrame`). After the handshake the client sends `Request{id, op, event}` frames and the daemon answers each with `Response{id, ok, seq, error}`, in order. Ops are `publish` and `ping`. A malformed or oversized frame gets `frame` and the connection closes.

**What the daemon accepts.** For each `publish` it applies `notifysock.Check`, then its own ownership checks, then publishes through its bus, whose store interceptor appends as `store.RoleOwner`:

| Code | Refused when |
| --- | --- |
| `forbidden_type` | the type is not a socket type. Phase 2 allows only `hook.ran` and `commit.created`; phase 3 adds `op.**`. Git, configuration, approval and GitHub events come only from the daemon's own sources, so no process can forge them. |
| `forbidden_source` | `source` is not `cli`, `mcp:<client>` or `hook:<name>` |
| `invalid_event` | `Event.Validate` fails, or the event carries a Seq |
| `unknown_repo` | the daemon does not own that repository's store |
| `unknown_worktree` | the worktree is not registered with the daemon |
| `internal` | publishing failed |

**Limits and back-pressure.**

| Limit | Value |
| --- | --- |
| Dial timeout | 200 ms (`DialTimeout`) |
| Handshake, each side | 1 s |
| One `Send` without an earlier deadline | 2 s |
| Writing one frame | 2 s |
| Idle connection | closed after 30 s |
| Connections | 32; the 33rd gets `busy` |
| Requests in flight per connection | 16 |

The daemon reads a connection's next request only after answering the previous one, so a fast producer waits rather than losing events. `Publish` never waits for subscribers (phase 1), so one slow browser cannot hold up a producer.

**Client API.** `notifysock.Dial(ctx, Locator) (*Client, error)` and `(*Client).Send(ctx, bus.Event) error`, which implements `notifysock.Sender`. `Send` returns nil once the daemon has stored and published the event, or an error wrapping `ErrNoDaemon` or `ErrRejected`, or a context error. Lane 2.2 adds both, and `notifysock.Serve` on the daemon side over a `notifysock.Acceptor`.

**Delivery chain.** `notifysock.Deliver` replaces the phase 1 rule "drop on `ErrOwned`" for every producer:

1. Append through a store opened with `store.RoleAppender`. With no daemon owning the store, this succeeds (`Stored`).
2. On `store.ErrOwned`, send over the socket (`Sent`).
3. If the socket reports `ErrNoDaemon` (the daemon is starting or stopping, or predates the socket), rejects the event or times out, drop it and warn once (`Dropped`).

CLI and MCP processes warn on stderr. Hooks pass no warning function, because git and agents read hook output; a dropped hook event is silent. Hooks also never create a store: they run in every repository the plugin reaches, so step 1 applies only when the store directory (its `repo-id`) already exists. Without a store no daemon has owned the repository (the owner creates it), so the socket would refuse the event as `unknown_repo`; the hook drops it silently without dialing. Lane 2.2 adds the test that an owned store sends over the socket, replacing the phase 1 drop test.

**Producers.**

- Every `agentflow hook ...` invocation publishes `hook.ran` when it ends, within a 300 ms budget. `pre-bash` runs before every shell command, so it publishes only `blocked` and `failed` outcomes. `reason` is a fixed code, never text.
- `commit.created` comes from a new `post-commit` git hook: `agentflow hook post-commit` (lane 2.2 implements `hooks.PostCommit`; lane 2.6 adds the CLI dispatch, since it owns `cli.go`). It parses the subject with `internal/commit`. In phase 2 the hook ships **only in this repository's `.githooks/`**. `agentflow init` and `templates/repo/` do not write it into consuming repositories (decision 2). Without the hook, `git.head.changed` still updates the overview.

### Phase 2 GitHub poller (lane 2.3)

One `watch.GitHubPoller` in the daemon polls every served repository that has a GitHub remote. It authenticates exactly as every other GitHub call, through `App.GitHub` (the gh CLI login, or `GH_TOKEN`/`GITHUB_TOKEN`). Lane 2.3 adds `(*gh.Client).GetConditional`, which sends `If-None-Match` and returns `watch.GitHubMeta` on both the HTTP and the gh CLI backends.

**What is polled, per repository.**

| Request | When | Cache |
| --- | --- | --- |
| `GET /repos/{o}/{r}/pulls?state=open&sort=updated&direction=desc&per_page=50` | every poll | ETag |
| `GET /repos/{o}/{r}/pulls?head={o}:{branch}&state=closed&per_page=1` | a branch's known PR left the open list (merged or closed) | none, once |
| GraphQL `reviewDecision` and `mergeStateStatus` for the changed PRs, in one query | the open list changed (a 200, not a 304) | none; GraphQL has no ETag |
| `GET /repos/{o}/{r}/commits/{sha}/check-runs?per_page=100` and `GET .../commits/{sha}/status` | for the head of each open PR whose branch a served worktree has checked out | ETag per SHA |
| `GET /repos/{o}/{r}/issues/{n}` | for each issue bound to a served worktree (github-issues task provider) | ETag |

A 304 does not count against GitHub's primary rate limit for authenticated requests, so an unchanged repository costs almost nothing.

**Change detection.** The poller keeps the last value of every PR, CI state and issue, and publishes only when a value changes. The first successful poll after start seeds the state without `gh.pr`, `gh.ci` or `gh.issue` events. The activity projection reads this state through `watch.GitHubCache`, so a snapshot never calls GitHub.

**Health.** The first poll of a repository and every later health change publish the transient `gh.health.changed`, and so does a repository becoming `disabled` (it has no GitHub remote). The activity projection recomputes on it (`gh.**`). Without it, a quiet repository's activity stayed `unavailable` after the first poll, and a rate limit or recovery showed only with the next data event. A cycle that keeps the same health publishes nothing. (Integration follow-up, issue #49.)

**Interval** (`watch.NextPoll`).

| Situation | Next poll |
| --- | --- |
| A tracked check is queued or in progress, or a tracked PR changed in the last 10 minutes | 15 s |
| Otherwise | 60 s |
| No stream or follower covers the repository | 5 min |
| `X-Poll-Interval` | a floor for all of the above |
| Less than 20% of the primary limit remains | stretched so the rest lasts until `X-RateLimit-Reset` |
| `Retry-After` on a rate-limited response | that long |
| `X-RateLimit-Remaining: 0` | until `X-RateLimit-Reset` |
| Consecutive failures (secondary limits, network, 5xx) | 1, 2, 4 ... minutes, at most 15 |

The poller never retries a rate-limited request earlier than GitHub asked. Lane 2.3 adds a test that counts the requests of one poll cycle against an `httptest` server: one PR list, one check-runs and one status request per tracked head, one request per bound issue, and a GraphQL query only when the PR list changed and only for the changed PRs. Authentication and permission errors set health to `unavailable` and back off. A repository without a GitHub remote is `disabled`. Health and the retry time are part of `GitHubState` and of the activity projection.

### Phase 2 projections (lane 2.4)

| Projection | Key | Topics | Reads | State |
| --- | --- | --- | --- | --- |
| `overview` | worktree | phase 1, plus `global/*/config.layer.changed` | git and configuration | unchanged |
| `activity` | repository | `gh.**`, `commit.created`, `hook.ran`, `git.head.changed`, `git.worktrees.changed` | `watch.GitHubCache`, the repository's store | `ActivityState` |
| `approvals` | repository | `approval.**` | `webui.ApprovalSource` (the journal records) | `ApprovalsState` |
| `tabs` | worktree | `ui.tabs.changed` | the tab hub | `TabsState` |

The JSON shapes are the types in `internal/webui/projection_states.go`. In short:

- `activity`: `pulls` (each with its worktree, CI state, failed checks and the PR it depends on), `issues`, `recent` (at most 20 `commit.created` and blocked or failed `hook.ran` entries, newest first), `github` (`state`, `retry_at`) and the `handoff` note. Agent Commander items, worker questions included, stay on `GET /api/activity` in phase 2 (decision 3).
- `approvals`: an `ApprovalItem` has no resolution time. `recent` is ordered and windowed by the record file's modification time, and the console shows no "answered at".
- `approvals`: `open` (pending and approved, oldest first, with `expired` set when `ObservedExpiry` holds) and `recent` (consumed, cancelled and expired from the last 24 hours, at most 10). Answering still goes through the existing approve flow, which checks the bearer.
- `tabs`: `tabs` (`{tab, attached}`) and `navigation` (`{id, tab, screen, role, target}` or null).

**Tabs on the stream.** `TabsState` replaces `GET /api/tabs/events`:

1. A tab attaches with `POST /w/<id>/api/tabs/attach {tab}` under its own worktree session and Agent Commander capability. A console without an event stream for the worktree (the stream answers 503) refuses the attach with 503, because the tab could never receive a navigation; the browser attaches again once its stream delivers a snapshot.
2. It stays attached while an event stream of the origin lists it in a `tab=<32 hex>` query parameter (at most 16 per stream), plus a 10-second grace (`TabGrace`) for a reconnecting SharedWorker.
3. A navigation appears in the state of the tab's worktree. Only the tab whose ID matches acts on it. The target is already chosen for that tab, so the browser never sees another tab's capability.
4. The tab still acknowledges with `POST /api/tabs/ack` and leaves with `POST /api/tabs/leave`. The hub clears the navigation on the ack or after 2 seconds, and publishes `ui.tabs.changed` each time.
5. A navigation never hangs. With no attached tab it returns at once; when no tab acknowledges within 2 seconds, or the target is not a `/w/<id>/` path of the tab's origin (for example the standalone `/`), it returns "no tab" and the caller opens a new tab.

`GET /api/tabs/events` is removed (issue #49): no client uses it since lane 2.5 (#48).

**Resync control.** The Refresh button becomes "resync": the client drops its state and reconnects without `Last-Event-ID`, which yields a fresh snapshot. No new endpoint is needed.

### Phase 2 multi-repository `agentflow events` (lane 2.6)

- Without flags, `agentflow events` and `--follow` now read the current repository **and `global`**, through `store.Union` of both stores opened as `store.RoleReader`.
- `--all-repos` reads every repository the daemon serves, plus `global`. `webui.ServedRepos(ctx)` (lane 2.6) lists them from the daemon's registry index. While following, the command re-reads that list every 5 seconds and rebuilds the union with the merged cursor when the set changes.
- Topics may then name any served repository or `global`. The cursor is the usual vector, one entry per repository. Printed events show their repository.
- MCP `agentflow_events` stays on the current repository plus `global`: agents work in one repository.

The terminal user owns every repository the daemon serves, so the CLI's wider view does not cross a trust boundary. The console's per-repository stream rule is unchanged.

### Phase 2 MCP configuration reload (lane 2.6)

**Triggers.**

1. **Events.** The MCP server follows `store.Union` of its repository's store and the `global` store, as `store.RoleReader`, on `app.ReloadTopics`. `app.NeedsReload` filters the events: the repository layer of its own worktree, or the personal layer. Bursts are coalesced for 250 ms (`ReloadDebounce`). The MCP server never starts the daemon to get these events.
2. **Before each tool call.** It compares a fingerprint (size, modification time and file identity) of `.agentflow/config.yml`, `.agentflow/providers/*`, `~/.agentflow/config.yml` and `~/.agentflow/providers/*`. When it differs, it reloads before running the call. This keeps the server correct when no daemon runs the watcher.

**Reload.** Build a new `*app.App` with `app.Open`'s rules: `Cfg`, `HasCfg`, `User`, and a gate from `Gate.WithConfiguration`, which keeps the prompt owner and the journal. If the new configuration does not load, keep the old App and add a notice.

**Swap and in-flight calls.** The server holds the App in an `atomic.Pointer[app.App]`. Each tool handler loads it once at its start and uses that App for the whole call, so a call in flight finishes with the configuration it started with. The next call uses the new one. A confirmation token issued before a reload is checked against the new policy when it is used: a tightened policy fails with `ErrChanged` and a fresh preview, and a loosened policy never upgrades an existing prompt (existing gate behaviour). The session writers get the new approval window through `SetApprovalWindow`.

**What does not reload.** The MCP `Instructions` (sent once at initialize) and the registered tool list. When a reload changes the selected providers, the next tool result carries a notice to restart the session. A server in configuration recovery mode stays in recovery mode until restart (decision 7).

**Notices.** A reload, a failed reload and "restart to refresh instructions" are appended to the next tool result, like update notices.

### Phase 2 exported APIs added by the lanes

The lanes added these exported names beyond the contract table at the top of this section. They are part of the phase 2 contract: changing them needs an amendment here.

| Lane | Package | Names |
| --- | --- | --- |
| 2.1 (#33) | `internal/watch` | `Timed` (a Translator with timers: `SetWake`, `NextDeadline`, `Tick`), `UserTranslator` (`NewUserTranslator`, `Serve`, `RepoForScope`), `(*FileWatcher).AddUser`, `CanonicalUser`, `OwnedDir`, `ApprovalPayload`; the console lock: `ConsoleLockName` (`console.lock` in the agentflow home), `HoldConsoleHome`, `ConsoleWatches` and `CheckHome`, which `agentflow doctor` uses for the `AGENTFLOW_HOME` warning (decision 1) |
| 2.1 (#33) | `internal/policy` | `ListJournal(dir, scopeKey, since)`, `ReadJournalRecord(path)`, `JournalRecord` |
| 2.3 (#35) | `internal/watch` | `NewGitHubPoller(GitHubOptions)`, `GitHubOptions{API, Classify, Clock, OnError}`, `GitHubClock`, `GitHubFailure` and its kinds |
| 2.3 (#35) | `internal/gh` | `(*Client).GetConditional(ctx, path, etag, out)`, `Classify(err)` |
| 2.6 / 2.7 (#38, #41) | `internal/webui`, `internal/app` | `webui.GlobalStoreDir()`, `webui.OpenGlobalEventStore(ctx, role)`, and the `app.OpenGlobalEventStore` hook that `internal/webui` sets from `init` so `internal/app` can open the `global` store without importing `webui` |

GitHub poller behaviours that lane 2.3 fixed and the table above does not show:

- The PR list asks for one page of 50 (`per_page=50`) and never pages. A repository with more open PRs keeps only the 50 most recently updated in view; a tracked PR pushed out of view is looked up once.
- The check runs and the combined status are both asked with `per_page=100`.
- A change of a tracked head's checks also triggers the GraphQL enrichment for that PR, because checks decide the merge state.
- A failed GraphQL enrichment is retried in the next cycles, at most 3 times per PR, then waits for the PR's next change.
- An issue that answers 404 is left alone for 10 minutes and does not count as a failure of the repository.
- Two polls of one repository start at least 5 seconds apart, so a burst of target changes is not a burst of requests.

Client settings (integration follow-up, issue #49): a user-level client settings directory (`~/.claude` and the like) may be a symbolic link owned by this OS user to a directory owned by this OS user, as dotfiles managers make it. The watcher watches the real directory and reports its changes under the link's path. A link to a directory of another owner is still refused and reported, and an agentflow home or its subdirectories must still be real directories.

### File ownership for phase 2 lanes

A lane changes only the files it owns. A lane that needs another lane's file, or an exported contract change, stops and reports.

| Lane | Issue | Owns | Must not touch |
| --- | --- | --- | --- |
| 2.1 Watch sources | #33 | `internal/watch/**` except `github*.go`; `internal/policy/journal.go` (new, read-only); `internal/webui/stream_runtime_user.go` (its `runtimeExtension`); the `AGENTFLOW_HOME` mismatch check in `agentflow doctor` (decision 1) | `internal/gh/**`, other `internal/webui` files |
| 2.2 Local socket and hooks | #34 | `internal/notifysock/**`; `internal/hooks/**`; `internal/webui/instance*.go` (`notify` field, `NotifyEndpoint`, `instance_notify*.go` with its `runtimeExtension`); `.githooks/post-commit` | `internal/cli/cli.go` (lane 2.6 adds `hook post-commit`); `templates/repo/**` and `internal/setup/**` (decision 2) |
| 2.3 GitHub poller | #35 | `internal/gh/**`; `internal/watch/github*.go`; `internal/webui/stream_runtime_github.go` (its `runtimeExtension`) | other `internal/watch` files |
| 2.4 Projections and tabs | #36 | `internal/webui/projection_*.go`, `stream.go`, `stream_runtime.go` (projector registration, the `global` store, global subscriptions), `tabs.go`, `workspace.go`, `server.go` (routes) | `internal/webui/static/**`, `instance*.go` |
| 2.5 UI | #37 | `internal/webui/static/**` (`app.js`, `tab-navigation.js`, `event-worker.js`, `style.css`, `index.html`) | any Go file |
| 2.6 MCP reload and events CLI | #38 | `internal/mcpserver/**`; `internal/app/events*.go`; `internal/cli/events*.go`, **`internal/cli/cli.go`, `internal/cli/parity_test.go`**; `internal/webui/served.go` (`ServedRepos`); `adapters/claude-code/skills/events/SKILL.md` | `internal/webui` files other than `served.go` |
| 2.7 Durable global store | #41 | `internal/bus/store` (`OpenGlobal`, `private_*.go`, `global_test.go`); `internal/webui/stream_global.go` and the `openGlobalStore` seam in `stream_runtime.go` | other `stream_runtime.go` code, `instance*.go` |

- Lane 2.6 is the only phase 2 lane that may edit `cli.go`, `mcpserver/server.go` or `parity_test.go`.
- Lanes 2.1, 2.2 and 2.3 start their daemon parts only by registering a `runtimeExtension` from their own file's `init`. None of them edits `stream_runtime.go`.
- `README.md` and `docs/events.md` are shared by section. Each lane writes only the section about its own feature. Lane 2.6 owns the `agentflow events` sections.
- Order: lanes 2.1, 2.2, 2.3 and 2.6 can start at once. Lane 2.4 codes against `notifysocktest`, a fake `ApprovalSource` and a fake `GitHubCache` until 2.1 and 2.3 merge. Lane 2.5 builds against the shapes in `projection_states.go` and the scripted stream fixture.
- Exported contract types change only through an amendment to this ADR: the phase 2 types and payloads, `GlobalRule`, the `notifysock` wire types, codes and limits, `watch.UserTarget`, `ApprovalRouter`, the `GitHub*` types, `NextPoll`, the projection state types, `runtimeExtension`, `ReloadTopics` and `NeedsReload`.

**Security review (lanes 2.1, 2.2 and 2.4, before merge).**

- [ ] An approval record reaches only the stream of the repository its scope key belongs to. A record of an unserved scope produces no event.
- [ ] No payload carries a bearer token, a fingerprint, a session MAC, a command, a message body, hook output, file contents or an absolute path.
- [ ] The socket refuses a client without the registry key, refuses forbidden types and sources, and on Unix refuses another user's peer. A stale listener learns only a random nonce.
- [ ] `ParseEndpoint` never accepts a non-loopback address.
- [ ] The `global` store, the socket and every watched user-level directory belong to the daemon's OS user.
- [ ] A stream may follow `global` only for `config.**` and `client.**`, and a `Last-Event-ID` may name no repository except its own and `global`.

### Phase 2 decisions

*Decided by the coordinator on PR #39. These were the open questions of this amendment.*

1. **`AGENTFLOW_HOME` differs between the daemon and a client.** Decided: the daemon watches only its own home (`config.HomeDir()` in the daemon process). Approval records written under another home never reach it. Lane 2.1 adds a warning to `agentflow doctor` when the current process's home differs from the running daemon's.
2. **The `post-commit` hook.** Decided: phase 2 ships it only in this repository's `.githooks/`. `agentflow init` and `templates/repo/` do not write it into consuming repositories yet, so consumer-facing behaviour does not change. Writing it for consumers stays an owner decision for a later phase.
3. **Agent Commander activity.** Decided: it stays request-driven on `GET /api/activity` in phase 2, worker questions included. The `activity` projection carries none of it.
4. **`pre-bash` events.** Decided: `pre-bash` publishes `hook.ran` only for blocked and failed outcomes. The phase 3 timeline does not need allowed runs.
5. **GraphQL cost.** Decided: keep the GraphQL enrichment (review decision and merge state), only for PRs whose entry in the open list changed. Lane 2.3 adds a test that counts requests per poll cycle (see "Phase 2 GitHub poller").
6. **Double record of approvals.** Decided: the Wire Tap no longer audits `approval.*`, because the gate's journal already records every transition. `bus.AuditedFamilies` is now `["op"]`.
7. **Reload limits.** Decided: a server in configuration recovery mode, and the registered tool list, still need a restart after a configuration change. `docs/events.md` says so.

### Phase 2 consequences

- Approvals, PR and CI status, hook results and commits reach the console without a click.
- Every user-level event lives in one private, per-host `global` store of at most about 50 MB.
- A process that is not the daemon can now add events while the daemon runs, but only `hook.ran` and `commit.created`, and only after proving it can read the daemon's private registry.
- GitHub is polled only for repositories the daemon serves, mostly with free conditional requests, and never faster than GitHub allows.
- A running MCP server follows configuration edits without a restart, except for its instructions, its tool list and recovery mode.
- Consuming repositories see no change in phase 2: the `post-commit` hook stays in this repository.

## Phase 3

*Amendment for phase 3 (issue #52), lanes 3.1 to 3.5.* Phase 3 turns every operation into a registered Command and adds a live Timeline: run `agentflow commit`, `agentflow pr` or an MCP tool from any agent, and each step (requested, waiting for approval, applied or failed) appears in the console, linked to its task, branch and PR.

Everything in the phase 1 and phase 2 sections still holds unless a paragraph here says it changes. The code form of this section:

| Contract | Go |
| --- | --- |
| Operation description | `command.Spec[In, Out]`, `command.CLI[In]`, `command.MCP`, `command.Policy`, `command.Scope`, `command.Detail`, `command.Plan` in `internal/command/spec.go` |
| Type-free view, CLI and MCP derivation | `command.Meta`, `command.Command`, `command.MCPWrap`, `command.CLIEnv`, `(Spec).AddMCP`, `(Spec).RunCLI` in `internal/command/command.go` |
| Registry | `command.Registry`, `command.Default`, `command.Register`, `command.Verb`, `RegisterVerb`, `Dispatch`, `ParityRows`, `Usage` in `internal/command/registry.go` |
| Invoker | `command.Invocation`, `Origin`, `Call`, `Bind`, `Invoke`, `Stage`, `Handler`, `Invoker`, `Options`, `Emitter`, `EmitterSource`, `Auditor`, `Outcome`, `Classify`, `ErrorCode` in `internal/command/invoke.go` |
| Fakes | `internal/command/commandtest`: recording `Emitter` and `Auditor`, a reference `Invoker` for `GateInRun` operations, a buffer `Env` |
| Event types and payload | `internal/bus/types_phase3.go` (`TypeOp*`, `OpPayload`, `OpLink`) |
| Correlation from a token | `policy.OperationIDOf` |
| "Never create a store" check | `store.Exists`, used by hooks and the Emitter |
| Timeline state | `internal/webui/projection_timeline_state.go` |

### Phase 3 patterns

| Piece | GoF / EIP pattern | Go |
| --- | --- | --- |
| One operation | Command (GoF); Command Message (EIP) | `command.Spec`, `command.Invocation` |
| Run order | Template Method | the Invoker's fixed skeleton |
| Validate, gate, extra checks | Chain of Responsibility; Pipes and Filters | `command.Stage` |
| Who runs an Invocation | Invoker (GoF); Point-to-Point (EIP) | `command.Invoker` |
| What happened | Event Message | `op.*` |
| Linking the steps of one operation | Correlation Identifier | `CorrelationID` = operation ID |
| Linking an operation to its trigger | Message History (causation) | `CausationID` |
| Timeline | Event-sourced read model | the `timeline` projection |

### Command Message and Event Message

They are different messages and never take each other's place.

| | Command Message: `command.Invocation` | Event Message: `op.*` |
| --- | --- | --- |
| Says | "run commit with this input" | "commit was requested", "commit was applied" |
| Addressed to | one handler, the Invoker | every subscriber |
| May be refused | yes: validation, the gate, the operation itself | no: it states a fact |
| Carries | the full input, the confirm token | the subject, task, branch, codes and links only (see payload rules) |
| Stored | never | in the repository's message store |
| Sent over the bus or socket | never | yes |

Later phases send Commands to the Invoker, never through the bus. A plugin's `command.invoke` (phase 5) and an AI session's tool call (phase 7) build an `Invocation` with their `Origin` and the triggering event as `Cause`. A subscriber that reacts to an event and wants to change something sends a Command; it never publishes an `op.*` event itself.

### `command.Spec`

```go
type Spec[In, Out any] struct {
    Name     string        // "commit", "commit.prepare", "pr.open": unique, never renamed
    CLIPath  []string      // {"commit"}, {"pr", "mark-ready"}; required with MCP
    CLI      *CLI[In]      // own binding, or nil when a Verb owns CLIPath
    MCP      *MCP          // Tool, Description, Annotations, WriteHost
    Policy   Policy        // Class, Action, Related, Gate, PrepareOnly
    Events   Scope         // ScopeDefault, ScopeWorktree, ScopeRepo, ScopeNone
    Token    func(*In) *string
    DryRun   func(In) bool
    Validate func(In) error
    Describe func(In) Detail // Subject, Task, Branch for the timeline
    Plan     func(ctx context.Context, a *app.App, in In) (Plan, error) // GateByInvoker only
    Run      func(ctx context.Context, a *app.App, in In) (any, error)  // calls one internal/app method
    Links    func(Out) []bus.OpLink
    NativeReads bool         // the app method may return a *native.Request
}
```

- **In** is the MCP tool's input struct. Its `json` and `jsonschema` tags are the tool's input schema, inferred by the MCP SDK exactly as today. The CLI fills the same struct from flags (`CLI.Flags`, `CLI.Args`). Tool names and input fields stay a compatibility contract.
- **Out** is the applied result type. `Classify` uses it to tell an applied result from a refusal. `Out = any` treats every non-Pending value as applied, which suits read-only operations.
- **Run** returns `(any, error)`, exactly what the `internal/app` method returns today: a `*app.Pending`, a result, a refusal map such as `{"status": "invalid"}`, or an error. Nothing wraps it.
- **Policy.Gate** decides who calls the gate:

| Mode | Who gates | Used by |
| --- | --- | --- |
| `GateInRun` | the `internal/app` method calls `a.gate` itself, as today; the Invoker reads the decision from Run's result | every migrated operation in phase 3, so its Pending stays byte-identical |
| `GateByInvoker` | the Invoker calls `Spec.Plan`, then `(*app.App).GateOperation` (lane 3.1), which wraps the same `a.gate` and `prepareWithActions`, and calls Run only on proceed | new operations from phase 4 on |
| `GateNone` | nobody | read-only operations (`Class: policy.Read`) |

- **Audit.** `Policy.AuditByInvoker` makes the Invoker write the operation's audit line. It is set only for operations whose app method writes no `a.audit` line today, and it is required with `GateByInvoker`. A `GateInRun` method that audits itself keeps doing so and the Invoker adds nothing (decision 4).
- **Native reads.** `Spec.NativeReads` marks an operation whose app method may return a `*native.Request` (the client must read through its own provider connection and call again with `native_results`). Such a step publishes nothing (decision 7).
- **Prepare-only.** `Policy.PrepareOnly` marks an operation that only prepares an exact candidate (`commit.prepare`, `push.prepare`, `pr.prepare`). Its outcome is `op.prepared`, `op.awaiting_approval` or `op.suggested`, never `op.applied`. The CLI runner prints its result once, without the confirmation flow.
- **Read-only commands register too**, with `Class: policy.Read`. They are never gated and publish no events, but the registry derives their CLI, tool and usage like any other.

### Registry and per-file registration

Each operation family has one file in `internal/command/ops/` (lane 3.1 creates the package), which registers its Specs from `init`:

```go
func init() {
    command.Register(command.Spec[eodIn, any]{Name: "eod.post", ...})
}
```

- `command.Register` adds to `command.Default` and panics on an invalid or duplicate Spec, so a mistake fails every test of every package that links it.
- `Registry.Add` checks: a valid unique name; an MCP tool needs a CLI path (CLI and MCP parity is enforced at registration, not only by a test); a unique `agentflow_*` tool name; a write names its policy action; a Spec with its own CLI has a usage block; a CLI path that a Spec binds belongs to that Spec alone.
- Several Specs share one CLI command through a **Verb** (`command.RegisterVerb`): `commit` runs `commit`, `commit.prepare`, `commit.plan` or `commit.context` by its flags; `pr` runs `pr.ready`, `pr.prepare` or `pr.open`. A Verb parses its own arguments and reaches each operation through the Invoker, never through the app method directly.
- `internal/cli` and `internal/mcpserver` import `internal/command/ops` for its registrations. `internal/app` never imports `internal/command`.

### Derived surfaces

Lane 3.1 derives these from the registry. After lane 3.1, a new operation touches only its own `ops` file (plus its `internal/app` method, skill and README section):

| Surface | Derived how |
| --- | --- |
| CLI dispatch | `cli.run` first calls `command.Default.Dispatch(args)`, the longest registered path, then falls back to today's switch for commands not migrated yet. `CLIEnv` is implemented by `internal/cli` over `open`, `confirmLoop` and `writeResult`, so output is unchanged. |
| CLI flags and `--yes` | `CLI.Flags` binds flags to In; the runner adds `--yes` when `CLI.Confirm` is set; `CLI.Interspersed` replaces `reorder`. Commands with their own output (interactive commit, text tables, the stderr lines of `start`) use `CLI.Run` or a Verb, and still call the Invoker. |
| MCP tools | `mcpserver.register` calls `AddMCP` for every registered command, with an `MCPWrap` made from today's `addTool` body (current App, write host, notices, `errResult`, `ContextText`). A legacy `add` for a tool name the registry has is skipped, so a lane that migrates a tool does not edit `server.go`. `MCP.WriteHost` replaces the `writerForTool` switch for registered tools. |
| Usage text | Rendered from blocks sorted by section (`Setup`, `Work`, `Runtime`), `Order`, then path. Lane 3.1 moves today's text into a legacy block table in `internal/cli/usage_legacy.go`; a legacy block whose path is registered is skipped. At the switch-over the rendered text must equal today's `usage` byte for byte (a test checks it once). |
| Parity test | `mcpToCLI` becomes the legacy map. `TestEveryMCPToolHasCLICommand` checks the union of `command.Default.ParityRows()` and the legacy map, registry rows winning, so a migrating lane never edits `parity_test.go`. |
| Tool schemas | Before any migration, lane 3.1 adds a golden file of every tool's name, description, annotations and input schema (`internal/mcpserver/testdata/golden/tools.json`). A migration that changes any of them fails. |

The legacy switch cases, `add` calls, input types, legacy usage blocks and legacy parity rows become dead as lanes migrate. One cleanup PR at the end of the phase (lane 3.1, after 3.2 to 3.4 merge) deletes them. Runtime plumbing that is not an operation stays in `cli.go`'s fixed switch: `mcp`, `hook`, `lint-msg`, `keys`, `update`, `version`, `help` and `--native-client`.

### Invoker

`command.Invoke(ctx, inv, a, spec, in, origin)` binds the Spec to its input (`command.Bind`) and runs the resulting `Call`. Lane 3.1 adds `command.NewInvoker(Options) Invoker`, a Template Method with this fixed skeleton. `commandtest.Invoker` is the reference for `GateInRun` operations.

1. **Validate.** `Spec.Validate`. An error returns at once, as is, with no event: the request never became an operation.
2. **Extra stages** (`Options.Stages`, Chain of Responsibility), empty in phase 3. Phase 5 plugins add permission rules here.
3. **Publish `op.requested`.** For a `NativeReads` operation it is published after step 5 instead, with the call's start time, and not at all when the outcome is deferred.
4. **Gate.** `GateByInvoker`: `Spec.Plan`, then `a.GateOperation`; a non-nil Pending ends the run. `GateInRun`: nothing here.
5. **Run.** `Spec.Run`.
6. **Audit.** `Auditor.AuditOperation` with the Outcome, only when `Policy.AuditByInvoker` is set, and never for a dry run, a read or a deferred outcome.
7. **Publish the outcome event**, unless the outcome is deferred.

Rules:

- **The result is never changed.** The Invoker returns Run's value and error unchanged, so `Pending` JSON, CLI output and MCP tool output stay byte for byte what they are; the phase 0 golden tests (`internal/cli/testdata/golden`, `internal/app` golden tests) must pass unchanged after every migration.
- **Events never change the result.** An Emitter or Auditor error is dropped (the Emitter warns once on stderr, as in phase 2). Events are notifications.
- **No events** for a read (`ScopeNone`), a dry run (`Spec.DryRun`), a validation error, or a native read step.
- **Outcome** (`command.Classify[Out]`):

| Run returned | Outcome | Event |
| --- | --- | --- |
| a `*native.Request` error | deferred | none, and no audit line |
| any other error | failed, `code` = `command.ErrorCode(err)` | `op.failed` |
| `*app.Pending` `suggest` | suggested | `op.suggested` |
| `*app.Pending` `needs_confirmation` or `awaiting_approval` | awaiting approval, with the approval's ID and expiry | `op.awaiting_approval` |
| `*app.Pending` `prepared` | prepared | `op.prepared` |
| an `Out`, `*Out` or nil | applied, with `Spec.Links` | `op.applied` |
| any other value | failed, `code` = its `status` field (`invalid`, `blocked`), else `unexpected_result` | `op.failed` |

- **Error codes, never error text.** `ErrorCode` gives a `policy.ErrorCode` value (`approval_expired`, `operation_changed`, `invalid_token`, ...), `cancelled`, `timeout` or `error`. Error text can hold paths, provider responses or secrets, so no event carries it, as for `hook.ran` in phase 2. The terminal and the tool result still show the full error.
- **Origin.** CLI calls use source `cli`; MCP calls `mcp:<client>`; the daemon's own calls (console approve, phase 4 settings) `daemon`. `Origin.Cause` is the triggering event's ID, or empty.

### Phase 3 event types

| Type | Scope | Global | Payload | Produced when |
| --- | --- | --- | --- | --- |
| `op.requested` | any | never | `OpPayload` with `resumed` | an operation passed validation and is about to be gated and run |
| `op.suggested` | any | never | `OpPayload` with `status: suggest` | the policy only suggests it; nothing ran |
| `op.awaiting_approval` | any | never | `OpPayload` with `status`, `approval_id`, `expires_at` | it waits for a foreground confirmation (`needs_confirmation`) or a shared approval (`awaiting_approval`) |
| `op.prepared` | any | never | `OpPayload` with `status: prepared`, `approval_id`, `expires_at` | a prepare-only request produced a candidate an auto policy lets the caller apply |
| `op.applied` | any | never | `OpPayload` with `links`, `duration_ms` | it ran and changed something |
| `op.failed` | any | never | `OpPayload` with `code`, `duration_ms` | it returned an error or a refusal |

`OpPayload` (`internal/bus/types_phase3.go`): `operation`, `action`, `class`, `client`, `subject`, `task`, `branch`, `resumed`, `status`, `approval_id`, `expires_at`, `code`, `links` (`{kind, id, url}`, kinds `commit`, `branch`, `pr`, `task`, `doc`, `release`), `duration_ms`.

Payload rules (the phase 2 rules, applied by `OpPayload.Clamp`):

- One text field is at most 256 bytes (`bus.MaxPayloadText`), cut at a rune boundary; `subject` is the first line only. At most 20 links (`bus.MaxPayloadNames`).
- No payload carries a confirm token, an approval fingerprint, a session MAC, a command line, a message body (only its subject), file contents, error text or an absolute path. `Spec.Describe` gives only a subject, a task reference and a branch.
- Text is data for display, never an instruction.

**Correlation and causation.**

- The **operation ID** is the `CorrelationID` of every event of one operation. It has the approval ID form, `op_` and 32 hex characters.
- A call that carries a confirm token uses the operation ID the token names (`policy.OperationIDOf`, which decodes without checking the MAC: it is a grouping hint and grants nothing; the gate still checks the token). So the CLI's second run in `confirmLoop`, an agent resuming with `confirm_token`, and a worker resuming a shared approval all join the operation they resume.
- Otherwise the call gets a new ID. The gate deduplicates requests by fingerprint and may return an approval issued earlier, so the ID cannot be chosen before the gate runs. When the outcome names an approval whose ID differs from the call's, the outcome event uses the **approval's ID** as `CorrelationID` and names the `op.requested` event as its `CausationID`. The timeline moves that `op.requested` into the approval's group (see below).
- `approval.*` events (phase 2) carry the same operation ID in their payload, so the timeline joins them to the operation.
- `op.requested` has the triggering event (`Origin.Cause`) as its `CausationID`, or none. Every outcome event has its call's `op.requested` as `CausationID`.

### Scope and storage of `op.*` events

- `op.*` events belong to the repository the operation ran in, never to `global`. They go to that repository's store (`<common-git-dir>/agentflow/events/`), through `notifysock.Deliver` like `hook.ran`: append as `store.RoleAppender`, or send over the socket while the daemon owns the store. `op.**` is now a socket type (`notifysock.Allowed`).
- `ScopeWorktree` (the default for writes) tags the event with the console worktree ID of the directory the call ran in (`webui.IdentifyWorktree`). If the daemon refuses it as `unknown_worktree`, the Emitter sends it once more without the worktree. `ScopeRepo` events are repository-wide.
- `ScopeNone` publishes nothing: reads, and writes that belong to no repository (`install`, `login`, `logout`, the personal configuration layer).
- CLI and MCP processes follow the hooks' rule (decision 6): they **never create an event store**. Lane 3.1's production Emitter (`internal/command/deliver`, which may import `internal/webui`) calls `notifysock.Deliver` only when `store.Exists(commonDir)` is true, that is when a daemon has created the store. Then it appends as `store.RoleAppender`, or sends over the socket while the daemon owns the store, or drops the event with one stderr warning. Without a store it drops the event at once, without dialing: the socket would refuse it as `unknown_repo`. So agentflow commands add no files under a consuming repository's `.git`. `store.Exists` is the shared check; `internal/hooks` uses it too. Lane 3.1 adds a test that an operation in a repository without a store leaves `<common-git-dir>/agentflow/` absent.

### Audit

- **No duplicate lines: the number of `audit.log` lines per call stays what it is today.** The `internal/app` method's own `a.audit` line stays. It hashes the exact reviewed summary, which only the method has.
- The Invoker's audit stage writes a line only for operations with `Policy.AuditByInvoker`, which have no audit of their own today (and every `GateByInvoker` operation). The line goes through `policy.Gate.AuditOperation` (lane 3.1): time, client, class, action, operation name, operation ID (`Outcome.OperationID`), outcome kind and code.
- The timeline links to audit lines by operation ID only where the line carries one, that is, for `AuditByInvoker` operations. Adding the operation ID to the app methods' own `a.audit` lines is a later change, not part of the migration.
- Lane 3.1 adds a test that counts `audit.log` lines for one migrated self-auditing operation and one `AuditByInvoker` operation, before and after migration: one line per write each. `internal/command` already checks the rule with the reference Invoker (`TestAuditLineCountUnchanged`).
- The bus Wire Tap no longer audits `op.*` (`bus.AuditedFamilies` is now empty; decision 4).

### Phase 3 timeline projection (lane 3.5)

| Projection | Key | Topics | Reads | State |
| --- | --- | --- | --- | --- |
| `timeline` | repository | `op.**`, `approval.**` | the repository's store, at most `store.MaxReplay` events back | `TimelineState` |

`internal/webui/projection_timeline_state.go` holds the shapes:

- `TimelineState{operations, truncated}`: at most 50 operations (`MaxTimelineOperations`), **newest first by `updated`**, none whose last step is older than 7 days (`TimelineAge`).
- `TimelineOperation`: `id` (the operation ID), `operation`, `action`, `class`, `client`, `worktree`, `subject`, `task`, `branch`, `state` (the latest step: `requested`, `suggested`, `awaiting_approval`, `prepared`, `applied`, `failed`, or from an approval event `approved`, `cancelled`, `expired`), `code`, `approval{state, expires_at}`, `links`, `started`, `updated`, `steps` (oldest first, at most 12, `truncated` when cut).
- `TimelineStep`: `event_id`, `seq`, `type`, `time`, `state`, `code`.

**Grouping by correlation ID.**

1. Group `op.*` events by `CorrelationID`, and `approval.*` events by their payload's operation ID.
2. When an event's `CausationID` names an `op.requested` in another group, move that `op.requested` into the event's group, and drop the group it leaves if it is now empty. This joins a gated operation's first request to its approval's ID.
3. An `approval.*` group without any `op.*` event (an operation run before phase 3, or by an older binary) is shown with `operation` set to the approval's action.
4. The browser filters by worktree, client, state and operation itself; the state carries everything it needs. Links go to the task, the PR and the commit (`links`), and to the audit line by operation ID.

The projection is keyed by repository, so one stream carries every worktree's operations. The client filters to the current worktree by default.

### Migration recipe

A migrated operation keeps its `internal/app` method unchanged (`GateInRun`), moves its MCP input type into its `ops` file, and registers one Spec. Before, `eod post` lived in three places:

```go
// internal/mcpserver/server.go
add(s, "agentflow_eod_post", "Post end-of-day progress comments on tickets.", remote,
    func(ctx context.Context, a *app.App, in eodIn) (any, error) {
        return a.PostEOD(ctx, in.Notes, in.ConfirmToken)
    })
// internal/cli/parity.go: runEOD parses KEY=NOTE pairs and --yes, then
//     confirmLoop(*yes, func(tok string) (any, error) { return a.PostEOD(ctx, notes, tok) })
// internal/cli/cli.go: case "eod" in run, and the usage line
// internal/cli/parity_test.go: "agentflow_eod_post": "eod post"
```

After, one file:

```go
// internal/command/ops/eod.go
type eodIn struct {
    Notes        map[string]string `json:"notes" jsonschema:"map of Jira key to a one-line progress note"`
    ConfirmToken string            `json:"confirm_token,omitempty"`
}

func init() {
    command.Register(command.Spec[eodIn, any]{
        Name:    "eod.post",
        CLIPath: []string{"eod", "post"},
        CLI: &command.CLI[eodIn]{
            Args:         parseNotes, // KEY=NOTE pairs, the same errors as runEOD
            Interspersed: true,
            Confirm:      true,
            Usage:        "  eod post KEY=NOTE [KEY=NOTE...] [--yes]   post end-of-day progress comments",
            Section:      command.SectionWork,
            Order:        2300,
        },
        MCP:      &command.MCP{Tool: "agentflow_eod_post", Description: "Post end-of-day progress comments on tickets.", Annotations: command.RemoteWrite},
        Policy:   command.Policy{Class: policy.Remote, Action: "eod"},
        Token:    func(in *eodIn) *string { return &in.ConfirmToken },
        Describe: func(in eodIn) command.Detail { return command.Detail{Subject: fmt.Sprintf("end-of-day notes on %d tasks", len(in.Notes))} },
        Run: func(ctx context.Context, a *app.App, in eodIn) (any, error) {
            return a.PostEOD(ctx, in.Notes, in.ConfirmToken)
        },
    })
}
```

The legacy `add`, `case` and parity row are skipped from then on and deleted by the cleanup PR. A migration PR must show: the tool-schema golden file and the phase 0 golden tests unchanged; the operation's events in a test with `commandtest` (requested, then the right outcome, for the confirm, apply and refusal paths); and no edit to `cli.go`, `mcpserver/server.go` or `parity_test.go`.

### `TestEveryWriteOperationEmitsEvents` (lane 3.4, end of phase)

In `internal/command/ops`. For every registered Spec:

- A write (`Class` not `read`) has `Events` other than `ScopeNone`, unless it is on a short allow-list with a reason (`install`, `login`, `logout`, `config.save_personal`).
- A gated write names a valid autonomy action (`config.ValidAutonomyAction`), and a write whose MCP input has `confirm_token` sets `Spec.Token`.
- A write whose app method writes no `a.audit` line sets `Policy.AuditByInvoker`, and one that does leaves it unset. A Spec whose MCP input embeds `native_results` sets `NativeReads`.
- With Run replaced by stubs that return each outcome (applied `Out`, Pending of each status, a refusal map, an error), the reference Invoker publishes `op.requested` then the matching outcome event, every event passes `bus.Event.Validate` and `notifysock.Check`, and no payload contains the input's token or a newline in `subject`.
- Every tool the MCP server registers comes from the registry, and every case left in `cli.run`'s switch is a runtime command (`mcp`, `hook`, `lint-msg`, `keys`, `update`, `version`, `help`).

### File ownership for phase 3 lanes

| Lane | Owns | Must not touch |
| --- | --- | --- |
| 3.1 Registry, Invoker, derivation | `internal/command/*.go` (`NewInvoker`, stages), `internal/command/deliver/**`, `internal/command/ops/doc.go`, `internal/command/ops/policy.go` and `ops/eod.go` (the examples: `policy.explain`, `approval.status`, `approval.approve`, `eod.post`); `(*app.App).GateOperation` in a new `internal/app/gate_operation.go`; `policy.Gate.AuditOperation` in a new `internal/policy/audit_operation.go`; **`internal/cli/cli.go`, `internal/cli/usage_legacy.go`, `internal/cli/parity_test.go`, `internal/mcpserver/server.go`** and the tool-schema golden test; the end-of-phase cleanup PR | other `ops` files, `internal/webui/**` |
| 3.2 Commit, PR, push, review | `internal/command/ops/{commit,pr,push,review,status,collisions}*.go`; `internal/app/{commitops,pr,git_publication,review}*.go` only where a method must change | `cli.go`, `server.go`, `parity_test.go` |
| 3.3 Task, transition, comment, ticket, AC, scope | `internal/command/ops/{task,transition,comment,ticket,ac,scope,context,work,standup}*.go`; `internal/app/{task,team,ac,scope}*.go` and `Transition`/`Comment` in `app.go` only where a method must change | same |
| 3.4 Docs, release, notify, setup, worktree, secops | `internal/command/ops/{docs,adr,release,notify,setup,config,worktree,sweep,delegate,promote,secops,events,ui}*.go`, the matching `internal/app` files, and `internal/command/ops/emit_test.go` (`TestEveryWriteOperationEmitsEvents`) | same |
| 3.5 Timeline | `internal/webui/projection_timeline*.go` (the projection, its registration through a `runtimeExtension` in its own file), `internal/webui/static/timeline.*`, the timeline's script tag in `static/index.html` and its rules in `style.css` | `internal/command/**`, other projections |

- Lane 3.1 is the only phase 3 lane that may edit `cli.go`, `mcpserver/server.go` or `parity_test.go`. Lanes 3.2 to 3.4 start after lane 3.1 merges; lane 3.5 can start at once against `TimelineState` and the `commandtest` events.
- Each migrating lane also updates its commands' plugin skills (`adapters/claude-code/skills/<command>/SKILL.md`) and README sections only if their behaviour changes; a pure migration changes neither.
- Exported contract types change only through an amendment to this ADR: everything in the code-form table above.

**Security review (lanes 3.1 and 3.5, before merge).**

- [ ] No `op.*` payload carries a confirm token, a fingerprint, a command line, a message body, error text or an absolute path; `Clamp` is applied to every payload.
- [ ] `policy.OperationIDOf` is used only for correlation, never to authorize; a forged token can at most mislabel the caller's own events.
- [ ] Only `cli` and `mcp:*` sources send `op.*` over the socket; the daemon's own calls publish on its bus.
- [ ] No CLI or MCP process creates `<common-git-dir>/agentflow/`: the Emitter checks `store.Exists` first.
- [ ] The timeline shows only the stream's own repository.

### Phase 3 decisions

*Decided by the coordinator on PR #59.*

1. **Gate in Run for migrations.** Migrated operations keep calling `a.gate` inside their app method (`GateInRun`). Moving every summary computation into `Spec.Plan` would rewrite each operation and risk the byte-identical guarantee for no visible gain. `GateByInvoker` is for new operations.
2. **The approval's ID is the operation ID once the gate issues one.** The gate deduplicates by fingerprint, so the ID cannot be fixed in advance; the timeline joins the first request through its causation link.
3. **Codes, not error text.** `op.failed` carries a fixed code only.
4. **No duplicate audit lines.** `GateInRun` operations already call `a.audit` themselves, so the Invoker does not audit them. It audits only operations marked `Policy.AuditByInvoker`, which have no audit of their own today. The `audit.log` line count per call stays the same. The bus Wire Tap does not audit `op.*` either (`bus.AuditedFamilies` is empty): it runs only in the daemon and would add one line per event.
5. **One cleanup PR.** Lanes leave legacy code dead rather than deleting it, so only lane 3.1 edits the shared files.
6. **CLI and MCP processes never create an event store**, the same rule as hooks. They append only when the store already exists (the daemon created it), otherwise send over the socket, otherwise drop. Agentflow commands add no files under a consuming repository's `.git`.
7. **A native read step publishes nothing.** When Run returns `native_read_required`, no `op.requested`, outcome event or audit line is written. The client's resumed call, with `native_results`, is the one that publishes `op.requested` and its outcome, under that call's operation ID. Limitation: the native read request ID is chosen inside `internal/provider/native` from the read itself, not from the operation, so it cannot carry an operation ID across the round trip. Nothing needs to be joined, because the earlier steps publish nothing; the timeline simply does not show the read round trips. A round trip that ends in a confirm token still joins its later steps through the token (Correlation, above).
8. **A CLI "no" at the prompt** shows on the timeline only through `approval.resolved{state: cancelled}`. The CLI sends no `op.failed` for it.
9. **Commands outside a repository publish nothing.** `global` stays limited to the configuration and client-settings families, as the phase 2 scoping rule says; `op.*` is never global.

*Decided by the coordinator when the lanes were integrated (PR #59).*

10. **A refusal is a map; a success is an `appliedMap`.** The delivery app methods (push, PR, mark-ready) return `map[string]any` both for a success (`{"status": "pushed"}`) and for a refusal (`{"status": "not_ready"}`). Their Specs convert a success into `appliedMap` (`internal/command/ops/delivery.go`), a named type that marshals exactly like the map, so `Classify` reports it as `op.applied`. A refusal stays a plain map and becomes `op.failed` with its `status` as the code. CLI and tool output is unchanged.
11. **Prepare operations audit once, through the Invoker.** `commit --prepare`, `push --prepare`, `pr --prepare`, `pr mark-ready --prepare`, their tools, and `agentflow_commit_context` when it stages paths write no audit line of their own, so their Specs set `Policy.AuditByInvoker`: one line per call, with the operation ID. `worktree go-work` is audited by the Invoker for the same reason. `agentflow_commit_context` with no paths only reads the staged index and counts as a dry run: no events and no line.
12. **Projections join through `projectionExtensions`.** Lane 3.5 added `projectionExtensions []func(*repoEvents) Projection` in `internal/webui/stream_runtime.go`. `repoProjections` appends one projection per entry to the four built-in ones (overview, activity, approvals, tabs) when it builds a repository's projector. The timeline registers itself from `projection_timeline.go`'s `init`. A `runtimeExtension` could not do this: it sees a repository only after its projector starts, and a projector's projections are fixed by then. Later phases add projections the same way, without editing `repoProjections`.
13. **What stays legacy for the cleanup PR.** The lanes left these on the legacy path in `internal/cli` and `internal/mcpserver`; the cleanup PR moves them or records why not:
    - `lint-msg` stays a runtime command (the `commit-msg` hook contract).
    - `agentflow task` with no subcommand still reaches the legacy `runTask`; its usage error must stay.
    - The CLI of `sweep`, `delegate` and `setup apply`: their tools are registered and publish events, but `CLIEnv.Confirm` prints the final result as JSON, while these commands print a table or plan text, and `setup apply` has its own prompt.
    - The CLI of `setup` (plan), `setup providers` and `ui`, which must run without an App.
    - The `events` CLI.
    - `config` and `agentflow_config_apply`, whose input schema (an enum on `operation`, raw JSON values) `command.MCP` cannot carry yet.
    - Writes with no Spec at all: `init`, `remove`, `pin`, `install`, `login`, `logout`, `setup login`, `setup logout`, `release init`, `release sync` and `doctor --fix`. `TestEveryWriteOperationEmitsEvents` lists each with its reason and fails when an entry is no longer true.
    - `transition --github-event` and `transition --shipped` stay direct app calls: CI modes with no tool, which publish no events.

### Phase 3 consequences

- Every operation is one Spec in one file. The CLI, the MCP tool list, the usage text and the parity check follow from the registry, so phases 4 to 11 add operations in parallel lanes without touching shared files.
- Every write, from any client, leaves `op.*` events in its repository's store, so the console shows a live timeline and `agentflow events` shows the history.
- Output is unchanged: the golden tests and the tool-schema golden file pin it.
- `audit.log` keeps one line per write, as today. Operations without an audit of their own today get one, with the operation ID.
- Consuming repositories get no new files under `.git` from agentflow commands; only a running daemon creates an event store.

## Phase 4

*Amendment for phase 4 (issue #68), lanes 4.1 to 4.3 (issues #69, #70 and #71).* Phase 4 adds a Settings page where every configuration option has a structured control. Edits collect in one draft. The page checks the draft live and previews its effect and diff. One Save bar saves everything together, and Undo reverses the last save. Edits made outside the page (a terminal, an agent, `git pull`) show up live, with a warning on each field they conflict with.

Everything in the phase 1 to 3 sections still holds unless a paragraph here says it changes. Phase 4 adds **no event type**: it uses `config.layer.changed` (phases 1 and 2) and `op.*` (phase 3). The code form of this section:

| Contract | Go |
| --- | --- |
| Field metadata, layers, widgets, catalog entries | `internal/config/field_meta.go` (`FieldLayer`, `Widget`, `Applies`, `Loosening`, `Picker`, `FieldMeta`, `CatalogField`, `Group`) |
| Field IDs and paths | `internal/config/field_path.go` (`FieldID`, `ParseFieldID`, `FormatFieldPath`, `ParseFieldPath`, `MatchFieldPath`) |
| Change set, diagnostics, preview, save, undo, journal, field values, `Catalog` | `internal/settings/types.go` |
| Settings projection state | `internal/webui/projection_settings_state.go` (`SettingsState`) |
| Custom tool input schema (added by lane 4.2) | `command.MCP.InputSchema` in `internal/command/spec.go` |
| Confirmation that `auto` cannot skip (added by lane 4.2) | `app.OperationGate.RequireConfirmation` in `internal/app/gate_operation.go` |

### Phase 4 patterns

| Piece | GoF / EIP pattern | Go |
| --- | --- | --- |
| One save across two layers | Unit of Work | `settings.ChangeSet` |
| Save, undo, preview, fields | Command (GoF); Command Message (EIP) | the `config.*` Specs |
| Live checks | Chain of Responsibility (Pipes and Filters) | the check chain (below) |
| How each layer is written | Strategy | one writer per target: personal file, repository worktree, repository publication |
| Undo | Memento | `settings.JournalRecord` |
| Outside edits | Optimistic Offline Lock, per field | `Edit.Expect`, `ChangeSet.Base` |
| Live values | Event Notification with a server-side projection | `config.layer.changed` → the `settings` projection |
| Controls | Factory Method (in the browser): metadata to control | `FieldMeta.Widget` |
| `KEY=VALUE` to an edit | Message Translator | the `config set` Verb |
| Linking a save, its events and its journal record | Correlation Identifier | the operation ID |

### Phase 4 field IDs

A field is the unit the page draws one control for, a ChangeSet edits and the projection reports. Its ID is `<layer>:<path>`.

- **Layer** is `personal` (`~/.agentflow/config.yml`, `config.User`) or `repo` (`<worktree>/.agentflow/config.yml`, `config.Repo` plus the provider role sections). These are `bus.ConfigLayer` values. Provider definition files (`.agentflow/providers/*.yml`, `~/.agentflow/providers/*.yml`) and the other registered files (`releases.yml`, `flow-map.yml`, task templates and so on) are not settings fields. They keep the document editor on the Configuration page.
- **Path** is the YAML keys from the layer file's root, joined by `.`. Inside a key, `\.` is a dot and `\\` a backslash: the `setting` grammar `agentflow_config` already uses. In a catalog pattern a segment `*` matches any map key or list index. A key that is a literal `*` cannot be addressed.
- Examples: `repo:policy.max_autonomy.local`, `personal:autonomy.actions.console\.publish`, `repo:task_management.provider`, `repo:task_management.settings.repository`.
- A provider's settings field also has a `provider`. ID and provider together are unique in a catalog. Only the role's selected provider's fields are active.
- The CLI accepts a path without its layer when exactly one layer has that path. Otherwise it fails and names both IDs (for example `worktree.root`).

### Phase 4 Field metadata (lane 4.1)

`config.CatalogField` is one field: `id`, `layer`, `path`, `provider`, `schema` (the `config.Field` node, unchanged), `meta` (`config.FieldMeta`) and `columns` (a `FieldMeta` per property, for a `list` item or a `kv-table` value that is an object). `config.Field` itself does not change, so the `agentflow_config` list output and its golden files stay the same.

`FieldMeta` (JSON):

```json
{
  "label": "Local actions",
  "description": "Highest autonomy for local actions such as commit, in every repository.",
  "group": "autonomy",
  "order": 100,
  "widget": "select",
  "applies": "live",
  "sensitive": false,
  "loosens": "level",
  "advanced": false,
  "min": null, "max": null, "step": 0,
  "min_duration": "", "max_duration": "",
  "picker": {"role": "", "multiple": false, "kind": "", "base": "", "glob": ""},
  "placeholder": ""
}
```

- **Label, description, group** are display text. A description says what the setting does, never a value.
- **Widget** must suit the schema:

| Schema node | Widgets allowed |
| --- | --- |
| `boolean` | `toggle` |
| `string` with `enum` | `select`, `provider-picker` |
| `string` | `text`, `duration`, `branch-picker`, `file-picker`, `credential-ref` |
| `integer` | `stepper` |
| `array` of `string` | `chips`, `branch-picker` with `picker.multiple` |
| `array` of `object` | `list` (with `columns`) |
| `object` with `additional_properties` | `kv-table` (with `columns` when the value is an object) |

A nullable node takes the same widgets. The control then also offers "inherit", which is an `unset` edit.

- **Applies:** `live` (running daemons and MCP servers reload on `config.layer.changed`), `next-run` (read when a command starts), `restart` (running MCP sessions keep the old value: provider selections, because they change the tool list and instructions), `new-worktrees` (`worktree.*`).
- **Sensitive:** the value never reaches the browser, an event, an audit line or a log. A preview shows only that it changed (`Value.redacted`, `Value.digest`). No field in today's schema needs it: credentials are environment variable names (`credential-ref`). It exists for provider definitions.
- **Loosens** marks fields where a change loosens policy (next section).
- **Picker:** `provider-picker` names its `role`; `branch-picker` may be `multiple`; `file-picker` has `kind` (`file`, `dir`), `base` (`repo`: relative to the worktree root, or `home`) and an optional `glob`.

**Catalog (lane 4.1).** `config.Catalog() []config.CatalogField` and `config.Groups() []config.Group`, built from compiled types only:

1. Walk `SchemaFor(config.User{}, "yaml")` as `personal` and `SchemaFor(config.Repo{}, "yaml")` as `repo`. An object with fixed properties is not a field: its properties are. A scalar, an array or a map is one field, and the walk stops there.
2. For each role in `ProviderCatalog()`: `repo:<role path>.provider` (a `provider-picker`), and each compiled provider's settings from `ProviderFieldSchemas()` under `repo:<role path>.settings`, with `provider` set. A nested role such as `devops.<x>` has the path `devops.<x>`. `task_management` also has `repo:task_management.templates.*` from `TaskTemplateSettings`.
3. Leave out format markers, with a reason in the table: the top-level `agentflow` version and each role's `version`.
4. Take each field's `FieldMeta` from one table, `internal/config/field_meta_table.go`, keyed by layer, path pattern and provider.

Initial groups, in order: `providers`, `autonomy`, `policy`, `commit`, `worktrees`, `integrations` (Jira, Confluence, Atlassian e-mail), `updates`, `init`. Each provider role's settings form a group `providers.<role>`. Lane 4.1 may add groups; the browser draws whatever `Groups()` returns.

**Repository catalog (lane 4.2).** A provider defined in a provider definition file has no compiled schema. `consolecfg` builds the worktree's `settings.Catalog` from `config.Catalog()` plus the definitions' settings, with derived metadata: the key as label, the schema type's first widget, group `providers.<role>`, `applies: restart`. `Catalog.Revision()` changes whenever its fields would.

**`TestEveryConfigFieldHasControl`** (lane 4.1, `internal/config/schema_coverage_test.go`) fails, naming the ID, when:

- a compiled field has no table entry, or a table entry matches no field;
- a label, description, group or `applies` is empty, or the group is not in `Groups()`;
- the widget does not suit the schema node (table above), a `stepper` has no `min`, a `duration` has no limits where the code checks some (`approval_window`, `policy.max_approval_window`: 1m to 1h), a `provider-picker` names no role, or a `list` or object `kv-table` lacks a `columns` entry for a property;
- a field the policy treats as loosening (next section) has no `loosens`.

### Phase 4 change set

`settings.ChangeSet` (JSON) is an ordered list of edits across both layers, saved together or not at all:

```json
{
  "edits": [
    {"id": "repo:task_management.provider", "op": "set", "value": "github-issues", "expect": "jira"},
    {"id": "repo:task_management.settings.repository", "op": "set", "value": "acme/widgets", "expect": null},
    {"id": "personal:autonomy.local", "op": "set", "value": "auto", "expect": "confirm"},
    {"id": "personal:approval_window", "op": "unset"}
  ],
  "base": {"personal": "sha256:9f…", "repo": "sha256:41…"},
  "repo": {"mode": "draft", "title": "chore: use GitHub issues for tasks", "task_ref": ""}
}
```

- **`op`:** `set` writes `value` at the path. `unset` removes the path, so the default or the other layer applies.
- **Order:** edits apply in order, so a provider selection comes before its settings. Two edits of one ID, or of an ID and a path inside it, are a `syntax` error.
- **Paths inside a field:** an edit may address a map key or list index inside a `kv-table`, `chips` or `list` field (`repo:policy.max_autonomy.actions.commit`). The field is still the unit of metadata and of the projection.
- **`expect`:** the layer value the client last saw. Absent: no check (the CLI). JSON `null`: "was not set". When the current layer value differs, the edit gets a `conflict` diagnostic.
- **`base`:** the layer revisions the client last saw (`sha256:<hex>` of the file bytes, or `absent`). A stale base alone is not an error: only the edits whose `expect` no longer holds conflict, and the rest merge with the outside edit.
- **`repo.mode`:** how repository edits are saved: `draft` (default) or `direct` through the existing console publication (`consolepublish`, the same flow and rules as `agentflow_config_apply`), or `worktree`. `title` is the conventional title for `draft` and `direct`; an empty title takes the preview's suggestion.
- **Limits:** at most 200 edits (`MaxEdits`), 64 KiB per value (`MaxValueBytes`), 1 MiB per request (`MaxBytes`).
- A ChangeSet is a Command Message: it is never published or stored.

### Phase 4 diagnostics and the check chain

`settings.Diagnostic`: `{severity, id, edit, code, message}`. `severity` is `error`, `warning` or `info`; only an `error` blocks a save. `id` is the field, or empty for the ChangeSet or a whole file. `edit` is the index in `edits`, or -1. `message` is text for display: it never holds a secret value, a file's contents or an absolute path.

Lane 4.2 runs the checks as a chain. Each link adds diagnostics; a link after an `error` on the same edit skips that edit:

| Order | Link | Codes |
| --- | --- | --- |
| 1 | ChangeSet syntax and limits | `syntax` |
| 2 | Catalog lookup under the provider selected so far | `unknown`, `layer`, `inactive` (warning) |
| 3 | Schema: type, enum, `nonnegative`, required | `type`, `enum`, `range`, `required` |
| 4 | Field rules: `min`/`max`, duration limits, autonomy action names, release-layer constraints | `range`, `reference`, `locked` |
| 5 | References: provider IDs, branches, files, environment variables | `reference` (warning), `credential` (warning when a referenced variable is unset) |
| 6 | Secrets: a value that looks like a credential (`consolecfg`'s credential patterns) outside a `credential-ref` field | `credential` (error) |
| 7 | Whole files: the edited layer files load (`config.InspectRepo`, the `config.User` parser) | `config` |
| 8 | Policy: a personal level above the repository ceiling; a loosening edit | `capped` (warning), `loosens` (info) |
| 9 | Concurrency: `expect` against the current file | `conflict` |
| 10 | Effect: restart or new worktrees needed; a no-op edit | `applies` (info), `unchanged` (info) |

The repository target adds `protected` (error: `worktree` mode on a protected branch). Undo adds `published` (below).

### Phase 4 preview

`config.preview` writes nothing and returns `settings.Preview`:

```json
{
  "status": "ok",
  "base": {"personal": "sha256:9f…", "repo": "sha256:41…"},
  "changes": [
    {"id": "personal:autonomy.local", "edit": 2, "applies": "live", "loosens": true,
     "before": {"json": "confirm", "set": true}, "after": {"json": "auto", "set": true},
     "effective": {"id": "personal:autonomy.local", "before": {"json": "confirm", "set": true},
                   "after": {"json": "confirm", "set": true}, "source_before": "personal", "source_after": "personal"}}
  ],
  "effects": [],
  "files": [
    {"layer": "repo", "path": ".agentflow/config.yml", "before_revision": "sha256:41…", "after_revision": "sha256:7c…", "diff": "--- a/.agentflow/config.yml\n+++ b/…"},
    {"layer": "personal", "path": "~/.agentflow/config.yml", "before_revision": "sha256:9f…", "after_revision": "sha256:e2…"}
  ],
  "diagnostics": [
    {"severity": "warning", "id": "personal:autonomy.local", "edit": 2, "code": "capped", "message": "The repository ceiling for local actions is confirm, so auto has no effect here."},
    {"severity": "info", "id": "personal:autonomy.local", "edit": 2, "code": "loosens", "message": "Allows local actions without confirmation."}
  ],
  "confirm": [{"id": "personal:autonomy.local", "reason": "loosens", "message": "Allows local actions without confirmation."}],
  "repo": {"mode": "draft", "title": "chore: use GitHub issues for tasks", "destination": "acme/widgets@main", "ci": "none"},
  "restart": true
}
```

- **`status`:** `ok`; `invalid` (an error other than `conflict`); `conflict` (only conflicts); `unchanged` (nothing would change).
- **`changes`:** one entry per edited field: the layer value before and after, its `applies`, `loosens`, and `effective` when its effective value changes.
- **`effects`:** other fields whose effective value changes: a personal level capped by a new ceiling, provider settings that become active or inactive.
- **`files`:** the layer files the save writes, with revisions. Only the repository file has a `diff`. **Personal YAML never leaves the disk**, on any surface: the personal layer is shown as field changes only.
- **`confirm`:** the edits that need an explicit confirmation, and why.
- **`base`:** the revisions the preview used. The save binds them.

**Loosening.** An edit loosens policy when it raises an effective autonomy level (`suggest` < `confirm` < `auto`), lengthens an approval window, turns on `policy.allow_auto_pr_publication` or `policy.coordinator_merge`, or removes a protected branch. The comparison uses effective values, so an `unset` can loosen too. The fields: `personal:autonomy.local`, `.remote`, `.actions`, `personal:approval_window`, `repo:policy.max_autonomy.local`, `.remote`, `.actions`, `repo:policy.max_approval_window`, `repo:policy.allow_auto_pr_publication`, `repo:policy.coordinator_merge`, `repo:policy.protected_branches`.

### Phase 4 save

`config.set` saves a ChangeSet. It is gated once for the whole change and writes all of it or none of it.

**Gate.** The Spec uses `GateInRun` (decision 1). Its Run computes the preview, then gates through `a.GateOperation` with the resolved class and action:

| Edits | Class | Action | Related |
| --- | --- | --- | --- |
| personal only | `local` | `console.settings` | |
| repository, `worktree` mode (with or without personal) | `local` | `console.settings` | |
| repository, `draft` or `direct` | `remote` | `console.publish` | `console.settings` when personal edits are included |

In every row, `console.autonomy` is also related when the save changes a personal approval setting (`autonomy.*`, `approval_window`) or loosens policy, so the strictest rule wins and `console.autonomy: suggest` (personal or as the repository ceiling) cannot be bypassed. `console.autonomy` therefore covers both classes (`local remote`).

- `console.settings` is a new autonomy action (`local remote`, "Settings saves require confirmation."). Adding a key is backwards compatible; lane 4.2 adds it to `autonomyCatalog`.
- **A save always needs a confirmation**, whatever the policy level: `OperationGate.RequireConfirmation` (lane 4.2) turns a `proceed` without a token into `needs_confirmation`. The CLI asks (or takes `--yes`), an MCP prompt owner shows the summary, and the browser shows the Save review. This keeps today's rule that `console.*` writes are always reviewed.
- **The summary** bound into the approval is: the operation, the `base` revisions, every change (ID, layer value before and after; sensitive values as digests), the `confirm` list, the repository plan (mode, title, destination, head, the source baseline `consolepublish` uses today) and the file revisions after. Any change to these between preview and save makes the token fail with `operation_changed`, and the caller previews again.
- A refusal is a result, not an error: Run returns the `Preview` with `status` `invalid`, `conflict` or `unchanged`. `Classify` reports it as `op.failed` with that status as `code`. An applied save returns `settings.SaveResult` (`op.applied`).
- `Policy.AuditByInvoker` is set: the app layer writes no audit line of its own for `config.set`, so the Invoker writes one, with the operation ID.

**Writing (Strategy per target).**

1. Under the personal file lock (the lock `replacePersonal` uses today, `config.yml.autonomy.lock` in the agentflow home), check every layer file's revision against the bound `base`. Any difference fails the save with `operation_changed`: nothing is written.
2. Compute every new file from the current bytes with comment-preserving YAML node edits (`consolecfg`), and check them once more with the whole-file link of the chain.
3. **Repository, `draft` or `direct`:** publish through `consolepublish.PublishTo`, with the same destination, baseline, title and CI rules as `agentflow_config_apply` today. The worktree is not touched; the change takes effect where the pull request merges. If it fails, nothing is written and the save fails.
4. **Repository, `worktree`:** write `.agentflow/config.yml` in the current worktree: temporary file in the same directory, `fsync`, rename. Refused with `protected` when the worktree's branch is protected (`policy.protected_branches`, default `main` and `master`). The developer commits the change with their task.
5. **Personal:** write `~/.agentflow/config.yml` the same way, mode 0600. Running daemons and MCP servers pick it up from `config.layer.changed` (phase 2): it applies at once, with no restart, except fields with `applies: restart`.
6. Write the journal record (next section).

Steps 3 and 4 run before step 5, so a repository failure leaves nothing changed. The one partial case is a personal write that fails after a publication succeeded: the result has `status: partial`, the publication, and the error, and the journal records only the repository part.

**Who saves from where.** CLI calls have origin `cli`, MCP calls `mcp:<client>`. The console calls the same Spec through the daemon's Invoker with origin `daemon`, so a browser save is an `op.*` operation on the timeline like any other. A save outside any repository changes only the personal layer and publishes no events (phase 3 decision 9).

### Phase 4 undo journal

The journal is a Memento of the last save, one per scope.

- **Where:** `<agentflow home>/settings/<policy.ScopeKey(scope)>.json`, where scope is the gate's repository scope (the key approvals use), or `<agentflow home>/settings/personal.json` outside a repository. The directory has mode 0700 and the file 0600. The watcher does not watch this directory.
- **What:** `settings.JournalRecord`: `version` (1), `operation_id`, `time`, `client`, `edits` (`{id, before, after}`, layer values; JSON `null` means not set), `before` and `after` revisions, `publication` (mode, URL, number, branch, state) and `undo_of`. A save replaces the record. Values are stored as in the layer files, so the journal holds nothing a layer file does not.
- **What undo does:** `config.undo` builds the inverse ChangeSet (each edit back to `before`, or `unset`), with `expect` set to each edit's `after`, and saves it through the same preview, gate and writers as `config.set`. The repository part uses the mode in the request, or the record's mode.
- **Conflicts:** a field whose current value is no longer the saved `after` conflicts. Undo refuses (`status: conflict`) unless the request sets `skip_conflicts`; then it reverts the other fields only.
- **Published repository changes:** while the record's pull request is open, its fields cannot be undone: they get `published` (error) and the projection links the PR ("close it to discard the change"). The projection learns the PR's state from `gh.pr.updated` and `watch.GitHubCache`; `config.undo` asks GitHub itself. After the PR merges and the worktree has the merged values, undo opens an inverse pull request.
- **Redo:** the undo writes its own record, with `undo_of` set to the save's operation ID. A second undo therefore redoes the save. The console labels the button "Redo" for such a record.
- There is no expiry: undo always means the last save in this scope.

### Phase 4 outside edits and conflicts

- **Detection, no polling.** A change to either layer file reaches the console as `config.layer.changed` (repository layer in the worktree; personal layer in `global`). The `settings` projection recomputes the worktree's state, with new `revisions` and `values`.
- **In the browser** (lane 4.3): when a new state arrives, the client compares each draft edit's `expect` with the field's new layer value. A difference marks the field "changed outside this page", with the new value and two choices: keep my edit (its `expect` becomes the new value) or take theirs (drop the edit). Fields changed outside and not in the draft just update, with the live "changed" highlight from phase 2.
- **On the server:** the preview and the save check `expect` again, so a client that missed an event still cannot overwrite a newer value without seeing a `conflict`.
- A change of the field catalog (a new provider definition, a new binary) shows as a new `catalog_revision`; the client reads the catalog again with `config.fields`.

### Phase 4 settings projection (lane 4.2)

| Projection | Key | Topics | Reads | State |
| --- | --- | --- | --- | --- |
| `settings` | worktree | `config.layer.changed` (own repository), `global/*/config.layer.changed`, `git.head.changed`, `gh.pr.updated`, `op.applied` and `op.failed` whose `operation` starts with `config.` | the worktree's layer files, its `settings.Catalog`, the scope's journal, `watch.GitHubCache` for the journal's pull request | `SettingsState` |

`SettingsState` (`internal/webui/projection_settings_state.go`):

```json
{
  "revisions": {"personal": "sha256:9f…", "repo": "sha256:41…"},
  "catalog_revision": "sha256:0d…",
  "values": [
    {"id": "personal:autonomy.local", "layer": {"json": "confirm", "set": true},
     "effective": {"json": "confirm", "set": true}, "source": "personal", "active": true},
    {"id": "repo:task_management.settings.repository", "provider": "github-issues",
     "layer": {"json": "acme/widgets", "set": true}, "effective": {"json": "acme/widgets", "set": true},
     "source": "repo", "active": true}
  ],
  "problems": [],
  "last_save": {"operation_id": "op_…", "time": "2026-11-26T10:00:00Z", "client": "repository-console",
                "ids": ["personal:autonomy.local"], "undoable": true},
  "branch": "issue-12-ab12cd34",
  "protected": false
}
```

- `values` holds every field that is set in its layer or active, sorted by ID, then provider. A value larger than 4 KiB (`MaxStateValue`) is sent with `too_large: true` and no JSON; the client reads it with `config.fields`.
- `source` is `default`, `personal`, `repo` or `definition`. `capped_by` names the repository ceiling that lowers a personal level.
- `last_save` has IDs only, no values. `undoable` is false while the repository part waits in an open pull request, or when every field changed since.
- The projection is computed from files, never from event payloads, so a missed event is repaired by the next one or by a snapshot (as the timeline).
- It registers through `projectionExtensions` from its own file's `init` (phase 3 decision 12).
- Sensitive values are redacted here as everywhere.

**Requests and the stream.** Every change of state the page shows arrives over the stream: values, revisions, problems, the journal, the branch. The page sends requests only for the user's own actions, and their answers concern only that request: read the catalog (once, and again when `catalog_revision` changes), preview the draft (live checks, debounced by 250 ms), save, undo. Nothing polls.

### Phase 4 commands

Lane 4.2 adds these Specs in `internal/command/ops/config_settings.go`:

| Spec | CLI | MCP tool | Class, action | Gate | Events | Result |
| --- | --- | --- | --- | --- | --- | --- |
| `config.fields` | `config fields [--group G] [--search Q] [--modified] [--json]` | `agentflow_config_fields` (read-only) | `read` | none | none | `settings.View` |
| `config.preview` | `config set --dry-run ...` (the `config set` Verb) | `agentflow_config_preview` (read-only) | `read` | none | none | `settings.Preview` |
| `config.set` | `config set ...` (the `config set` Verb) | `agentflow_config_set` | `remote`, `console.settings` (resolved per save, see "Gate") | in Run, always confirmed | worktree | `settings.SaveResult` |
| `config.undo` | `config undo [--skip-conflicts] [--mode M] [--title T] [--yes] [--json]` | `agentflow_config_undo` | as `config.set` | in Run, always confirmed | worktree | `settings.SaveResult` |

- `config.set` and `config.undo` link their publication (`links` kind `pr`) on `op.applied`.
- The static class of `config.set` and `config.undo` is `remote`, the strictest a save can be, so `TestEveryWriteOperationEmitsEvents` and the timeline see a valid write. The class and action the gate uses are the resolved ones in the table under "Gate".
- **Tool inputs.** `agentflow_config_fields`: `settings.FieldsRequest` (`group`, `ids`, `query`, `modified`). `agentflow_config_preview`: `settings.ChangeSet`. `agentflow_config_set`: `settings.ChangeSet` plus `confirm_token`. `agentflow_config_undo`: `settings.UndoRequest` plus `confirm_token`. `Edit.value` and `expect` accept any JSON type, so these tools set `command.MCP.InputSchema` (below).

**`agentflow config set A=1 B=2`.**

```text
agentflow config set [--dry-run] [--unset KEY]... [--mode draft|direct|worktree]
                     [--title T] [--task REF] [--yes] [--json] KEY=VALUE...
```

- `KEY` is an ID, or a path when exactly one layer has it. `VALUE` is taken literally for a `string` field (no quotes needed), as a comma-separated list for a `chips` field unless it starts with `[`, and as JSON otherwise (`true`, `3`, `{"a": "b"}`).
- Edits apply in argument order; `--unset` edits follow the `KEY=VALUE` edits, in their own order.
- No `expect` is sent, so the CLI never conflicts per field. The bound revisions still stop a write over a file that changed between the confirmation and the save.
- `--dry-run` runs `config.preview` and prints it. Without it the confirmation flow runs as for every write: the summary lists the loosening edits first.
- Text output is one line per change (`ID: before → after`), then the diagnostics. `--json` prints the result.

**Old subcommands keep working.** `agentflow config OP --input FILE` keeps its operations. `fields`, `set` and `undo` are new words: none of them is an old operation, so the registry's longest-path dispatch sends them to the new Specs and everything else to the legacy command.

**`agentflow_config` and `agentflow_config_apply` stay as they are.** Their names, input fields, enums and output are a compatibility contract. Phase 4 resolves what blocked their migration in phase 3:

- Lane 4.2 adds `InputSchema func() (*jsonschema.Schema, error)` to `command.MCP`. When it is set, `AddMCP` puts its result in `mcp.Tool.InputSchema`, and the SDK still decodes the call into `In`. Registration fails when it returns an error.
- Lane 4.2 then moves both tools into Specs (`config.inspect`, `config.apply`) in `internal/command/ops/config.go`. Their schemas are built by today's code, moved verbatim from `internal/mcpserver/configuration.go`. **`internal/mcpserver/testdata/golden/tools.json` must not change for these two tools**, and the CLI golden files for `config` must not change. The CLI stays `config OP --input FILE` through a `config` Verb; parity rows stay `config` and `config publish`.
- This migration is the last step of lane 4.2, and may be its own PR. If it would change either schema or any golden output, the lane stops and reports instead.

### Phase 4 amendments to the phase 3 contract

All additive. Lane 4.2 makes them.

| Contract | Change |
| --- | --- |
| `command.MCP` | `InputSchema func() (*jsonschema.Schema, error)`; nil keeps the inferred schema |
| `app.OperationGate` | `RequireConfirmation bool`: a `proceed` decision without a token becomes `needs_confirmation`, with the same token flow |
| `config` autonomy catalog | new action `console.settings` (`local remote`) |
| `TestEveryWriteOperationEmitsEvents` allow-list | `config publish` and `config save_personal` leave the legacy list when `config.apply` is registered |

### Phase 4 console endpoints (lane 4.2)

In `internal/webui/settings.go`, under the console's existing session, origin and request-header checks. Each calls the Spec through the daemon's Invoker; the handlers only decode and encode.

| Route | Body | Answer |
| --- | --- | --- |
| `GET /api/settings/catalog` | | `settings.View` with every field and no values (`values` come from the stream) |
| `POST /api/settings/preview` | `settings.ChangeSet` | `settings.Preview` |
| `POST /api/settings/save` | `{"change_set": ChangeSet, "approval": "<token>"}` | a `needs_confirmation` Pending with its token as `approval`, or `settings.SaveResult` |
| `POST /api/settings/undo` | `{"undo": UndoRequest, "approval": "<token>"}` | as save |

The Save review is the confirmation: the browser saves without `approval`, shows the summary it gets back next to the preview, and saves again with `approval` when the user confirms. A loosening edit needs its own tick in the review before Confirm is enabled.

### Phase 4 Settings page (lane 4.3)

- One page, `#settings`, with the groups from the catalog in order, a search box (label, description, ID) and a "modified" filter (fields in the draft, or set in a layer).
- One control per field from `meta.widget`, through a widget registry (Factory Method) in `static/settings-widgets.js`. `typed-editor.js` stays the fallback for the document editor on the Configuration page and must keep working there.
- Each control shows the field's source (default, personal, repository, definition), "inherit" for an `unset`, the repository ceiling when it caps a personal level, and the field's diagnostics from the last preview.
- **The Save bar** appears with the first edit and stays until the draft is saved or discarded: number of changes, errors and warnings, Discard, Review and save. The review shows the preview's changes, effects, the repository diff, the repository mode and title, and the loosening ticks.
- After a save, the bar offers Undo (or Redo) from `last_save`, and the publication link.
- Conflict warnings come from the stream, as "Phase 4 outside edits and conflicts" says.
- The Providers and Autonomy pages become links into the Settings page's `providers` and `autonomy` groups (decision 5). The Configuration page stays for the other registered files.
- The draft is kept per worktree in the browser's storage, as the document editor keeps staged edits, so a reload does not lose it.

### File ownership for phase 4 lanes

A lane changes only the files it owns. A lane that needs another lane's file, or a contract change, stops and reports.

| Lane | Issue | Owns | Must not touch |
| --- | --- | --- | --- |
| 4.1 Field metadata | #69 | `internal/config/schema.go`, `internal/config/catalog.go` (`Catalog`, `Groups`, the walk), `internal/config/field_meta_table.go`, their tests, `internal/config/schema_coverage_test.go` (`TestEveryConfigFieldHasControl`) | `internal/config/field_meta.go` and `field_path.go` (contract), `config.go`, `providers.go`, `autonomy.go`, everything outside `internal/config` |
| 4.2 Settings backend | #70 | `internal/settings/**` except `types.go` and `doc.go`; `internal/configconsole/**`; `internal/consolecfg/**` (Go files); `internal/consolepublish/**` where publication needs it; `internal/command/ops/config*.go`; `command.MCP.InputSchema` in `internal/command/spec.go` and `command.go`; `internal/app/gate_operation.go` and the policy code `RequireConfirmation` needs; `console.settings` in `internal/config/autonomy.go`; `internal/webui/settings.go`, `internal/webui/projection_settings.go` and their tests, and the four route lines in `internal/webui/server.go`; `adapters/claude-code/skills/config/SKILL.md`; README and `docs/repository-console.md` sections on settings commands and tools; golden files through `-update` only | `internal/webui/static/**`, the lane 4.1 files, `internal/cli/cli.go`, `internal/mcpserver/server.go`, `internal/cli/parity_test.go` |
| 4.3 Settings UI | #71 | `internal/webui/static/settings*.js`, `static/settings.css`, `static/typed-editor.*`, the Settings nav entry, page section and script tags in `static/index.html`, the Providers and Autonomy page retirement in `static/app.js`, settings rules in `static/style.css`, `internal/webui/settings_browser_test.go`, `internal/webui/testdata/settings/**`, `docs/design/ui/settings.md`, the README section on the Settings page | any other Go file |

- Before lane 4.1 merges, lane 4.2 codes against the `settings.Catalog` interface with a fake catalog. After it merges, production wires `config.Catalog()`.
- Lane 4.3 builds against the shapes in `internal/settings/types.go` and `SettingsState`, with a scripted stream fixture and fake endpoints in its browser test, until 4.2 merges.
- No phase 4 lane edits `cli.go`, `mcpserver/server.go` or `parity_test.go`: the new commands come from the registry.
- Exported contract types change only through an amendment: everything in the code-form table at the top of this section.

**Security review (lanes 4.2 and 4.3, before merge).**

- [ ] Personal YAML never leaves the disk: no preview, projection, event, audit line or log carries it. Personal changes are shown as field values only.
- [ ] Sensitive values are redacted in previews, the projection, the journal summary and every error message.
- [ ] Every save and undo needs a confirmation bound to the exact changes and revisions, under every autonomy level. A loosening edit is listed in the confirmation and ticked in the Save review.
- [ ] A secret pasted into a field that is not a `credential-ref` is refused (`credential` error).
- [ ] The console endpoints keep the session, origin and request-header checks; the stream shows only its own repository's and the user's own personal values.
- [ ] Journal files and the personal file stay mode 0600 in a directory owned by the user.
- [ ] Repository publication keeps every rule of `agentflow_config_apply`: conventional title, destination, baseline, CI impact.

### Phase 4 decisions

*Decided by the architect in the contract PR. The coordinator confirms or changes them before the lanes start.*

1. **`GateInRun` for `config.set` and `config.undo`.** Their class and action depend on the ChangeSet (personal, worktree or pull request), and a refusal must return the preview with its diagnostics. Run gates through `a.GateOperation` with the resolved class and action, and returns a refusal as a result that `Classify` already reports as `op.failed`. `GateByInvoker` would need three more fields on `command.Plan`. The static class is `remote`, the strictest case.
2. **No new event types.** `config.layer.changed` already reports every layer file change, and `op.*` reports every save and undo. The projection reads files, so it needs no new payload.
3. **Repository edits: a pull request by default, the worktree on request.** Writing the file into the worktree *and* opening a pull request would leave the same change twice: once uncommitted in the checkout, once in the PR, and a later pull would refuse to overwrite it. So `draft` (default) and `direct` publish through the existing flow without touching the worktree, and `worktree` writes the file for the developer to commit with their task.
4. **Settings fields are the two `config.yml` layers only.** Provider definitions, release units, the flow map, task templates and the other registered files keep the Configuration page's document editor. Phase 5 plugin settings join the catalog as a new source.
5. **The Providers and Autonomy pages are retired into Settings.** Their nav entries open the matching Settings groups. Their HTTP routes stay until the phase 11 cleanup, because nothing else changes in phase 4.
6. **Field metadata lives in a table, not in struct tags,** so lane 4.1 changes no configuration type, and `config.Field` (the schema node in `agentflow_config` output and its golden files) does not change.
7. **Every save is confirmed.** Today's console writes are always reviewed (`console.*`); the Save review keeps that, and `RequireConfirmation` makes it hold under `auto`.
8. **Undo is a save.** It goes through the same checks, gate and writers, so it cannot bypass anything a save checks, and undoing an undo redoes.
9. **The CLI sends no `expect`.** A terminal user sees the current values in the confirmation summary; the bound revisions still stop a lost update.

**Open questions for the coordinator.**

- Decision 3 reads "written to the worktree and published as a PR" in the plan as two targets rather than one save doing both. Confirm, or ask for both at once (and accept the duplicate change).
- Decision 5 removes two pages from the navigation. Confirm, or keep them next to Settings until phase 11.
- Lane 4.2 is large. If its diff passes about 1,500 lines, split the `agentflow_config` migration (the last step) into its own PR.

### Phase 4 consequences

- Every option in both `config.yml` layers has a control, a label and a description, and `TestEveryConfigFieldHasControl` keeps it so.
- One save changes both layers together, under one confirmation, and can be undone.
- The console, the CLI (`agentflow config set A=1 B=2`) and agents (`agentflow_config_set`) save through the same Spec, so every save is on the timeline.
- `agentflow_config` and `agentflow_config_apply` keep their inputs and outputs; they move onto the registry once `command.MCP.InputSchema` exists.
- Outside edits reach the page within the phase 1 latency, and no client polls.

## Phase 5

*Amendment for phase 5 (issue #77), lanes 5.1 to 5.4 (issues #78 to #81).* Phase 5 adds a plugin host. `agentflow plugin list/add/remove` manage plugins installed from a local path, a Plugins page shows them live, and each plugin's own settings appear on the Settings page. Two sample plugins show the two code-free and code tiers: a YAML plugin that turns `gh.ci.updated` failures into a notification, and a small out-of-process plugin that adds timeline annotations.

**Plugins are untrusted code.** Every rule in this section follows from that: a plugin gets only what its granted permissions allow, never a secret, a confirm token or the parent's environment, and it can make policy stricter but never looser.

Everything in the phase 1 to 4 sections still holds unless a paragraph here says it changes. The code form of this section:

| Contract | Go |
| --- | --- |
| Event types and payloads | `internal/bus/types_phase5.go` (`TypePlugin*`, `PluginState`, `PluginCode*`, `PluginLifecycleChanged`, `PluginDeliveryFailed`, `PluginAnnotationAdded`) |
| JSON-RPC wire types, codes, limits, `Conn`, `Handler`, `LaunchSpec`, `Process`, environment allowlist | `internal/jsonrpc/types.go` |
| Manifest, tiers, extension points, permissions and allow-lists, settings, registry entry, install layout, `Source`, validation codes | `internal/plugin/manifest/types.go` |
| `Instance`, `Env`, `Factory`, `Builtin`, invoke and publish requests and results, limits, lifecycle `Transitions`, `Status`, `Layers` | `internal/plugin/types.go` |
| JSON-RPC methods and their params | `internal/plugin/rpc.go` |
| Plugins projection state | `internal/webui/projection_plugins_state.go` (`PluginsState`) |
| Timeline annotations | `TimelineOperation.Annotations`, `TimelineAnnotation` in `internal/webui/projection_timeline_state.go` |
| JSON invocation of a command (added by lane 5.2) | `command.JSONCommand` in `internal/command/invoke_json.go` |
| Registered Invoker stages (added by lane 5.2) | `command.RegisterStage`, `command.RegisteredStages` in `internal/command/stages.go` |
| Autonomy ceiling (added by lane 5.2) | `(*policy.Gate).WithCeiling`, `(*app.App).WithAutonomyCeiling` |

### Phase 5 patterns

| Piece | GoF / EIP pattern | Go |
| --- | --- | --- |
| One Factory per tier, chosen by the registry | Abstract Factory | `plugin.Factory`, the tier registry |
| A process plugin inside the host | Proxy (GoF); Channel Adapter (EIP) | the process tier's `Instance` over `jsonrpc.Conn` |
| What a plugin may touch | Facade | `plugin.Env` |
| Lifecycle | State | `bus.PluginState`, `plugin.Transitions` |
| Which events reach a plugin | Message Filter; Selective Consumer | the plugin's subscription, checked against its grant |
| Declarative `when` | Message Filter | `manifest.Condition` |
| Declarative actions | Interpreter (small), Message Translator (templates) | the declarative tier |
| Failed delivery | Dead Letter Channel (persistent) | `<home>/plugins/deadletter.jsonl` |
| Malformed frames | Invalid Message Channel | protocol error: the connection closes, the plugin restarts |
| Plugin acts | Command Message to the Invoker | `command.invoke`, `command.JSONCommand` |
| Permission rules | Chain of Responsibility (an Invoker stage) | the permission_rule Stage |
| Plugin settings in Settings | a new catalog source | `manifest.CatalogFields` |
| Back-pressure per plugin | Event-Driven Consumer with acknowledgement | `event.deliver` is a request |

### Phase 5 tiers

| Tier | Runs | Code from the plugin | Implemented by |
| --- | --- | --- | --- |
| `declarative` | YAML rules interpreted in the host | none | lane 5.2 |
| `process` | an executable from the plugin directory, JSON-RPC on stdio | yes, in its own process | lane 5.2, over lane 5.1 |
| `builtin` | Go compiled into agentflow, `plugin.RegisterBuiltin` from `init` | agentflow's own | lane 5.2 (registry only; phase 5 ships none) |

There is no Go `plugin` package tier. The host never switches on the tier outside the Factory registry. Every tier is driven through the same `plugin.Instance` and gets the same `plugin.Env`, with the same permission checks. Builtins start disabled like any plugin.

Plugins run **only in the console daemon**. With no daemon running, no handler runs. Permission rules are the exception: they run in every process that invokes a command (see "Phase 5 permission rules").

### Phase 5 extension points

| Point | Phase 5 | Meaning |
| --- | --- | --- |
| `handler` | **implemented**, both tiers | React to events. Declarative: rules with `on`, `when`, `actions`. Process: `on` only declares what the process may subscribe to. |
| `permission_rule` | **implemented**, declarative only | Deny operations it matches. Never allow. |
| `harness` | reserved, phase 6 | An ACP harness definition. Phase 6 amends this ADR. |
| `provider` | reserved | A task, knowledge or notification provider. |
| `source` | reserved | Publish new event types. Needs per-plugin type registration. |
| `sink` | reserved | Durable delivery to an outside target (Durable Subscriber with its own cursor). A handler that neither invokes nor publishes covers the simple case today. |
| `widget` | reserved | A declarative console panel. Never plugin HTML or JavaScript in the console. |

`manifest.Validate` refuses a manifest that uses a reserved point (code `reserved`). A phase that implements one amends this ADR and `manifest.PointPhase`.

### Phase 5 manifest

A plugin is a directory with `agentflow-plugin.yml`, or (declarative only) a single YAML file. Decoding is strict: an unknown key is a `syntax` problem. The Go form is `manifest.Manifest`. The CI sample:

```yaml
schema: 1
id: ci-notify
name: CI failure notifications
version: 0.1.0
description: Sends a notification when the checks of a branch you work on start failing.
tier: declarative
permissions:
  subscribe: [gh.ci.updated]
  invoke: [notify]
extensions:
  handler:
    - name: ci-failed
      on: [gh.ci.updated]
      when:
        - {field: payload.state, equals: failure}
        - {field: payload.previous, not_equals: failure}
      actions:
        - invoke:
            command: notify
            input:
              signal: "{{settings.signal}}"
              operation_id: "ci-notify-{{event.payload.head_sha}}"
              title: "CI failed on {{event.payload.head_ref}}"
              body: "Failed checks: {{event.payload.failed}}"
settings:
  - key: signal
    type: string
    default: ci.failed
    label: Notification signal
    description: The notification signal to send. Route it in the notifications provider settings.
```

A process plugin sets `tier: process` and `exec: {command: timeline-hints, args: []}`. `exec.command` is relative to the plugin directory, without `..`; it is never looked up on PATH. On Windows a missing extensionless command also tries `.exe`.

Rules (`manifest.Validate`, lane 5.3; codes in `manifest.Code*`):

- `id`: 2 to 40 characters, `[a-z][a-z0-9-]*[a-z0-9]`. Reserved: `agentflow`, `builtin`, `daemon`, `cli`, `mcp`, and IDs starting with `agentflow-`.
- `version`: semantic version without `v`. `tier`: `declarative` or `process`; `builtin` is refused in a file.
- Every `handler.on` pattern, `invoke.command` and annotate action is covered by `permissions` (`ungranted`). Every permission is inside the allow-lists of "Phase 5 subscriptions and publishing" and "Phase 5 command invocation" (`permission`).
- A process plugin's handlers have no `when` or `actions`, and it has no permission rules (`rule`).
- Conditions have exactly one operator; actions exactly one kind; `annotate` only in handlers whose `on` patterns are all `op.*`.
- Templates use only `{{event.<field>}}` and `{{settings.<key>}}`.
- Limits (`manifest.Max*`): manifest 256 KiB, 256 files, 16 MiB per installed tree, 32 plugins installed, 32 handlers, 32 rules, 16 conditions, 4 actions, 32 settings, 16 patterns per list.

### Phase 5 install

**No downloads.** `plugin add` takes a local directory or a local YAML file. It refuses a URL (`http:`, `https:`, `git@`, `ssh:`, `file:` and anything with `://`), never runs `npx`, `uvx`, `pip`, `go install`, `git` or a package manager, and never fetches anything. A process plugin's executable is built by the person before they add it.

**Layout** under the agentflow home (`config.HomeDir()`):

```text
<home>/plugins/                    0700
  installed.json                   0600  the registry (manifest.Registry), replaced atomically under installed.json.lock
  deadletter.jsonl                 0600  the persistent Dead Letter Channel
  <id>/
    <first 16 hex of the hash>/    the installed copy: directories 0500, files 0400, executables 0500
    data/                          0700  the plugin's HOME, TMPDIR and only writable place; plugin.log
```

**What `plugin add PATH` does** (lane 5.3, `internal/plugin/install`):

1. Read the tree: regular files only. A symbolic link, a device, a socket, a path outside PATH, more than 256 files or 16 MiB is refused. A single YAML file is read as the manifest.
2. Parse and validate the manifest.
3. Hash it (`HashDir`): for each file sorted by its slash path, the line `<path>\x00<x or ->\x00<sha256 hex of the content>\n` (`x` for an executable bit); the hash is `sha256:` and the hex SHA-256 of all lines. The manifest file is part of the tree.
4. Gate it (below). The confirmation summary lists the ID, name, version, tier, hash, every extension, every permission, `exec.command`, and an upgrade's previous version and hash. A token is bound to that summary, so a file that changes between preview and apply fails with `operation_changed`.
5. Copy the tree into `<id>/<hash prefix>/` with read-only modes, re-hash the copy, and write the registry entry with the source path, the time and the parsed manifest. Remove the previous version's copy after the registry write.
6. The plugin is installed **disabled**. Enabling is a settings change, `personal:plugins.<id>.enabled`, from the Settings page or `agentflow config set plugins.<id>.enabled=true`: a loosening edit, ticked in the Save review.

Adding an installed ID with a different hash is an upgrade: same flow, the old version keeps running until the registry changes. Re-adding the same hash is a no-op.

**Verification at start.** Before every start the host calls `Source.Verify`: hash the installed copy again and parse its manifest. A difference moves the plugin to `failed` with `hash_mismatch` or `manifest_invalid`. Only `plugin add` again recovers it.

**Remove.** `plugin remove ID` deletes the registry entry, the installed copy, the data directory and the personal `plugins.<id>` block, under one confirmation. The daemon stops the plugin when it sees the registry change.

**How the daemon notices.** Lane 5.3 adds `<home>/plugins/installed.json` to the user watch (`internal/watch/user.go`): a change publishes `config.layer.changed{layer: personal, paths: ["plugins/installed.json"]}` in `global`. The host reloads on that event and on every personal-layer change (enabling, settings).

**A repository never installs or enables a plugin.** Only the personal layer and the person's own `plugin add` do. A cloned repository runs no plugin code. A repository may only turn plugins off for itself (`repo:plugins.disabled`).

### Phase 5 JSON-RPC framing and launcher (lane 5.1)

`internal/jsonrpc` is new shared code. `internal/provider/codexhost/stdio.go` stays as it is: it is a client of a different host with its own deny-replies, and moving it is not worth the risk. The plugin host (phase 5), the harness probe (phase 6) and the ACP client (phase 7) use `internal/jsonrpc`.

**Framing.** JSON-RPC 2.0. One JSON object per line, `\n`-terminated, written atomically, at most 1 MiB (`DefaultMaxFrame`; ACP may raise it through `Limits`). `jsonrpc: "2.0"` is required. Request IDs are a number or a string, echoed exactly. A frame that is not JSON is `-32700`; one that is not a valid request, notification or response is `-32600`. An oversized or malformed frame, or a response to an ID never sent, closes the connection (`ErrFrameTooLarge`, `ErrProtocol`): the Invalid Message Channel is the connection itself.

**Conn.** `NewConn(r, w, h, limits) Conn`: both sides send requests and notifications. `Call` waits for the answer until its context or `CallTimeout` (10 s) ends, then discards a late answer. The peer may have 4 requests in flight (`MaxInFlight`); one more gets `CodeBusy`. A Handler error of type `*jsonrpc.Error` is sent as is; any other becomes `-32603` "internal error" and its text is never sent. Error `data` is `{"code": "<fixed code>"}`.

**Error codes.** Standard `-32700`, `-32600`, `-32601`, `-32602`, `-32603`; agentflow `-32001` forbidden, `-32002` rate limited, `-32003` busy, `-32004` timeout, `-32005` shutting down.

**Launcher.** `Launch(ctx, LaunchSpec) (Process, error)`:

- `Path` is absolute and must resolve, symbolic links included, to a regular executable file inside `Root` (the installed copy). It never searches PATH.
- The environment is exactly `LaunchSpec.Env`, normally from `ScrubbedEnv`; a name `EnvAllowed` refuses fails the launch.
- Stdin and stdout carry the connection. Stderr goes to `LaunchSpec.Stderr`, at most 64 KiB kept (the plugin host writes it to `data/plugin.log`). It never reaches an event, the stream or a terminal.
- On Unix the child gets its own process group (`Setpgid`) and `Stop` and `Kill` signal the group. On Windows only the child is killed: job objects are new platform code, left out of phase 5 (decision 9).
- `Stop` closes stdin, waits `ShutdownGrace` (2 s), then kills.

**Scrubbed environment** (`ScrubbedEnv(parent, data, extra)`):

| Variable | Value |
| --- | --- |
| `LANG`, `LC_ALL`, `LC_CTYPE`, `LC_MESSAGES`, `TZ` | inherited when set |
| `SystemRoot`, `SystemDrive`, `COMSPEC`, `PATHEXT`, `WINDIR`, `NUMBER_OF_PROCESSORS`, `PROCESSOR_ARCHITECTURE` (Windows) | inherited when set |
| `PATH` | fixed: `/usr/bin:/bin:/usr/sbin:/sbin` on Unix, `%SystemRoot%\System32;%SystemRoot%` on Windows |
| `HOME`, `TMPDIR`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `XDG_DATA_HOME`; `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `TEMP`, `TMP` | under the plugin's data directory |
| `AGENTFLOW_PLUGIN_ID`, `AGENTFLOW_PLUGIN_DIR`, `AGENTFLOW_PLUGIN_DATA`, `AGENTFLOW_PLUGIN_PROTOCOL` | set by the host |

Nothing else passes: no `GH_TOKEN`, `GITHUB_TOKEN`, Jira or Confluence token, cloud keys, proxy credentials, `SSH_AUTH_SOCK`, client session IDs (`CLAUDE_CODE_SESSION_ID`, `CODEX_THREAD_ID`) or `AGENTFLOW_*` variable of the daemon. HOME points at the data directory so a library that reads `~/.netrc`, `~/.config/gh` or a cloud CLI's files finds nothing. This is not a sandbox: the process still runs as the user and can read what the user can (decision 8).

### Phase 5 JSON-RPC methods

Protocol version 1 (`manifest.ProtocolVersion`). Go shapes in `internal/plugin/rpc.go` and `types.go`.

| Direction | Method | Kind | Params → result |
| --- | --- | --- | --- |
| host → plugin | `initialize` | request | `{protocol_version, agentflow, plugin: {id, version}, settings, permissions, limits}` → `{protocol_version}` |
| host → plugin | `event.deliver` | request | `{event: <envelope>}` → `{}` |
| host → plugin | `settings.changed` | notification | `{settings}` |
| host → plugin | `ping` | request | `{}` → `{}` |
| host → plugin | `shutdown` | request | `{}` → `{}` |
| plugin → host | `subscribe` | request | `{types: [pattern]}` → `{types: [accepted]}` |
| plugin → host | `command.invoke` | request | `InvokeRequest` → `InvokeResult` |
| plugin → host | `event.publish` | request | `PublishRequest` → `{id}` |
| plugin → host | `log` | notification | `{level, message}` |

- `initialize` is the first message. A different `protocol_version` in the answer fails the start with `protocol`. A plugin request before `initialize` has been answered gets `-32001` with code `not_ready`.
- `event.deliver` carries one event, the full envelope as stored. The host sends the next only after the answer, so a slow plugin holds only its own queue (back-pressure). An error answer or a timeout sends the event to the Dead Letter Channel.
- `subscribe` replaces the plugin's subscription set. A pattern must equal a granted pattern or be a type a granted pattern matches; others are refused (`not_granted`).
- `log` writes one line (at most 4 KiB) to `data/plugin.log`, rotated at 1 MiB with one old file kept. Never to an event.
- Refusals are `-32001` (forbidden) with a `plugin.Refuse*` code, `-32002` for a rate limit.

### Phase 5 lifecycle

`bus.PluginState` and `plugin.Transitions` (GoF State). A declarative or builtin plugin has no process, so it never enters `degraded` or `backoff`.

```text
         plugin add                 enabled
 (none) ───────────► installed ─────────────► starting ──initialize ok──► running ◄──pong──┐
                         ▲                      │   ▲                      │   │            │
               disabled  │        fails         │   │ retry_at             │   └─missed 2──► degraded
                         │   ┌──────────────────┘   │                      │                 │
 stopped ◄── stopping ◄──┼───┤                  backoff ◄── exit, crash, protocol, timeout ──┘
                         │   └─5 restarts in 10 min, hash_mismatch, manifest_invalid─► failed
                         └── plugin remove (from any state, through stopping) ─► removed
```

| From | Event | To | Code |
| --- | --- | --- | --- |
| none | registry lists it, not enabled | `installed` | |
| none, `installed`, `stopped`, `failed` | enabled, verify passes | `starting` | `enabled` |
| any | verify fails | `failed` | `hash_mismatch`, `manifest_invalid` |
| `starting` | `initialize` answered with the right version | `running` | |
| `starting` | launch fails, `initialize` times out (5 s) or answers a wrong version | `backoff` (or `failed` for a wrong version) | `launch`, `timeout`, `protocol` |
| `running` | 2 missed pings (every 30 s, 5 s timeout) or a delivery timeout | `degraded` | `ping`, `timeout` |
| `degraded` | a ping answered | `running` | |
| `running`, `degraded` | 4 missed pings, exit, signal, protocol error | `backoff` | `ping`, `exited`, `crashed`, `protocol` |
| `backoff` | `retry_at` reached | `starting` | |
| `backoff` | the 5th restart within 10 minutes | `failed` | `crash_loop` |
| any running state | disabled, or the daemon stops | `stopping` → `stopped` | `disabled` |
| any | removed from the registry | `stopping` → `removed` | |

- **Backoff:** 1 s, doubling, at most 5 minutes. The restart counter resets after 10 minutes in `running`.
- **`failed` is sticky:** the plugin starts again only after its settings, its enabled flag or its installed hash change.
- **Stopping:** `shutdown`, then close stdin, then kill the process group after 2 s.
- Every transition publishes `plugin.lifecycle.changed`.

**Crash isolation.**

- Each plugin has its own bus subscription (256-event buffer, the phase 1 overflow rule) and its own goroutine running `bus.Handle`, so a slow or dead plugin never holds up Publish, another plugin or a projection.
- A process plugin is a separate OS process; its crash is an exit the host observes.
- The declarative interpreter runs each event under `recover` and a 100 ms budget (actions excluded); a panic is a `crashed` dead letter, and 5 in 10 minutes fail the plugin.
- An overflow (`stream.overflow` on the plugin's subscription) is delivered to the plugin as the marker event itself, counted in `dropped`, and is not a dead letter.

### Phase 5 events

| Type | Scope | Global | Transient | Payload | Produced when |
| --- | --- | --- | --- | --- | --- |
| `plugin.lifecycle.changed` | repository-wide | only | no | `PluginLifecycleChanged{plugin, version, tier, state, previous, code, restarts, retry_at}` | a plugin changes state (table above) |
| `plugin.delivery.failed` | repository-wide | only | yes | `PluginDeliveryFailed{plugin, event_id, event_type, event_repo, code}` | an event went to the Dead Letter Channel |
| `plugin.annotation.added` | any | never | no | `PluginAnnotationAdded{plugin, operation_id, text, url, level}` | a plugin publishes a note on an operation |

- Only the daemon's plugin host produces them, on its own bus. None is a socket type (`notifysock.Allowed` is unchanged), so no other process can forge one.
- Source: `plugin.host` for lifecycle and delivery events; `plugin:<id>` for an annotation.
- Payloads follow the phase 2 rules: codes, not text; no path, token or plugin error message. Annotation text is plugin text: clamped to one line of 256 bytes, an `https` URL only, shown as plain text.
- Lifecycle events go to the `global` store: plugins are installed per user. Streams do **not** gain a `global/*/plugin.**` topic (the phase 2 rule 2 is unchanged): the `plugins` projection subscribes to them and sends its state in the repository's own frames, as the overview does for personal-layer changes.
- No phase 5 type is audited by the Wire Tap.

### Phase 5 subscriptions and publishing

**What a plugin may receive** (`manifest.SubscribableFamilies`, `SubscribableTypes`): `git.**`, `config.**`, `gh.**`, `op.**`, `commit.**`, `hook.**`, and `plugin.annotation.added`. Never:

- `approval.*`: a plugin must not see a pending approval or race a person's answer;
- `client.*`, `ui.*`, `plugin.lifecycle.*`, `plugin.delivery.*`;
- any `global` event (the personal layer's changes reach a plugin only as `settings.changed`).

The host subscribes per plugin to `<repo>/*/<pattern>` for every served repository that does not list the plugin in `repo:plugins.disabled`, and adds or drops repositories as the daemon attaches them or the repository layer changes.

**What a plugin may publish** (`manifest.PublishableTypes`): `plugin.annotation.added` only. Never `op.*` (only the Invoker states what an operation did), `approval.*`, `plugin.lifecycle.*` or a type a daemon source owns. The host sets the source (`plugin:<id>`), the payload's `plugin`, the repository and worktree (from `cause`, or `repo` and `worktree`, which must be served and not disable the plugin), the `CausationID` (`cause`), and validates the payload (`operation_id` of the `op_` form, `level` `info` or `warning`, `url` `https` or empty).

**Rate limits** per plugin: 60 publishes a minute (burst 20).

### Phase 5 command invocation

A plugin changes things only by sending a Command to the Invoker, never through the bus. `command.invoke` (process) and the declarative `invoke` action take the same path:

1. **Allowed?** The command is in `manifest.InvocableCommands` and in the plugin's `permissions.invoke`, else `not_allowed` or `not_granted`. Phase 5 allows the writes `notify` and `comment`, and the reads `ship.status`, `context`, `ac.checklist` and `review.threads` (decision 4). Never `approval.*`, `policy.*`, `config.*`, `plugin.*`, `setup.*`, `promote`, `delegate`, `push`, `commit` or `pr.*`.
2. **Rate:** 10 invocations a minute (burst 5), each bounded by 60 s.
3. **Where:** the worktree named by `repo` and `worktree`, else the `cause` event's worktree, else the repository's `main` worktree. It must be served by the daemon and its repository must not disable the plugin (`unknown_worktree`, `repo_disabled`).
4. **Input:** decoded strictly into the Spec's In (`command.JSONCommand`, lane 5.2: unknown fields refused). An input with `confirm_token` is refused (`confirm_token`).
5. **App and gate:** `app.Open(dir, "plugin:<id>")`, then `WithAutonomyCeiling(confirm)`: every rule the gate resolves is capped at `confirm`, so `auto` never applies a plugin's write without a person (decision 5). `suggest` stays `suggest`.
6. **Invoke** with `Origin{Source: "plugin:<id>", Client: "plugin:<id>", Cause: <cause>}` through a daemon Invoker whose Emitter publishes on the daemon's bus, with `command.AuditLog{}`. The call appears on the timeline like any other operation, with client `plugin:<id>`.
7. **Result to the plugin** (`InvokeResult`): `applied` with the result (at most 256 KiB, else `truncated`), `failed` with a code, `suggested` with a code, or `awaiting_approval` with the approval ID and expiry. A `needs_confirmation` Pending is reported as `awaiting_approval` and **its token is dropped**: the record stays pending in the journal and the console's approvals panel shows it. Nobody holds its bearer token, so neither the panel nor `agentflow policy approve` can answer it; a person answers it with `agentflow plugin approve` (see "Phase 5 approving a plugin's write"). No error text reaches the plugin.
8. **Resume:** the host resumes an approved write itself (below), so a plugin need not. A process plugin may still call `command.invoke` with `resume: <approval_id>`, which passes the ID as the confirm token. The gate proceeds only when a person approved that record and the plugin's own client requested it; while it is pending the host answers `awaiting_approval` without running the call, and otherwise the plugin gets `failed` with `approval_expired`, `approval_consumed` or `operation_changed`.

A plugin can never confirm its own operation: it never holds a token, it cannot invoke `approval.*`, and it cannot see `approval.*` events.

### Phase 5 approving a plugin's write

*Amendment (issue #91). The owner chose this path.*

- **The record.** The gate stores the requesting client in every approval record (`requested_by`, for example `plugin:ci-notify`). A `plugin:` client can redeem, and read with `Status`, only the records it requested (`ErrNotRequester`). The `plugin:` prefix is reserved for the plugin host: `policy.NewGate` and `app.Open` refuse it (`ErrReservedClient`), so `agentflow mcp --client plugin:x`, the hooks and any other named client cannot ask or redeem as a plugin; only `policy.NewPluginGate` and `app.OpenPlugin`, which the host calls, make a plugin's gate. When a plugin's fresh call is held, the host writes a `PluginRequest` into the record once (`plugin.DescribeHeld`, `Gate.DescribePluginRequest`): the plugin, the command, the Spec's `Detail` (subject, task, branch) and `Summary`, the confirmation summary the command's gate returned (`app.Pending.Summary`, the same one a person sees running the command: for `notify` the notification and its destinations, for `comment` the ticket and the body). Every string is cleaned of control and bidi characters (one line for the `Detail` fields; newlines and tabs kept in the summary), and any value under a key that names a secret (`token`, `secret`, `password`, `credential`, `authorization`, `api_key`, `private_key`, `webhook`, `cookie` and similar) is replaced by `(redacted)`. A summary over 256 KiB is not stored, and `Omitted` says why. The record file is mode 0600, and a journal record may be up to 576 KiB. The command's raw input is never stored.
- **The command.** `agentflow plugin approve OP_ID [--decline] [--json]` (Spec `plugin.approve`, `local`, autonomy action `plugin.approve`, mandatory confirmation; gate in Run through `GateOperation` with `RequireConfirmation`, its expiry capped at the request's; `AuditByInvoker`, `Events: ScopeNone`). CLI only, no MCP tool (decision 3 applies). It refuses agent sessions (`app.AgentEnvironment`) and a non-terminal standard input, ignores `--yes`, and the pre-bash never-list blocks agents from running it in every mode, `--convention-only` included. It shows the plugin, the command, the `Detail`, the expiry and the whole stored summary (indented JSON, never cut, however long), asks y/N, then calls `Gate.ApprovePluginRequest` (or `DeclinePluginRequest`): only a pending record whose `requested_by` starts with `plugin:`; any other is refused (`ErrNotPluginRequest`), and so is a call from a `plugin:` client. Approval (not decline) also needs a host-written request (`ErrNoHostRequest`) for a write in `manifest.InvocableWrites` (`notify`, `comment`) whose gate action is that command (`ErrNotPluginWrite`), with its summary (`ErrNoRequestSummary`): a person never approves what they were not shown. It finds records by the scope of the repository it runs in.
- **No bearer.** `ApprovePluginRequest` is the one path that answers a record without its bearer token. The person at the terminal holds the per-user secret the token derives from, so the token would add nothing; the person-only checks are what keep it a person's act.
- **The resume.** The host keeps each held call in memory (plugin, command, exact input, cause, target; at most 256). It subscribes itself to `approval.resolved` and `approval.expired` for every repository; plugins still never receive `approval.*`. On each event naming a held call it reads the record through the plugin's own gate: `approved` claims the call and resumes it once with the approval ID, `pending` keeps it, anything else (cancelled, expired, consumed) drops it. A call is claimed before it runs, so a repeated event cannot run it twice, and the gate consumes the approval in any case. Before it runs a call the host checks the plugin again: still running as the installed version and hash that asked, the command still in `manifest.InvocableCommands` and in its `permissions.invoke`, and the repository not disabling it; otherwise it drops the call. If the plugin resumes the call itself, the first of the two to claim it runs it: a plugin that loses gets `failed` with code `resumed_by_host`, and nothing runs or is published for it (no `op.failed` for a used approval). When the host's approval subscription overflows (256 events), it settles every held call instead of losing the dropped answers.
- **Limits.** Held calls do not survive a daemon restart: an approval granted after one runs nothing, and the plugin asks again on its next event. The approval window (5 minutes by default) bounds how long a write waits.
- **Stopping.** `Stop` returns after the host's resumes and the process plugins' `command.invoke` requests have ended. It waits at most 10 s, then cancels the approved writes still running and waits 2 s more. Nothing else cancels an approved write that has started: not the daemon's context ending, nor the plugin's connection closing. So it also runs on, within its 60 s, when only that plugin crashes or is disabled. Once `Stop` has started, no resume starts and a `command.invoke` request is refused with `not_ready`.
- **The UI.** `ApprovalItem` carries `requested_by` (always present; empty for records written before it existed). The Approvals panel and the Plugins page show `agentflow plugin approve <operation ID>` for items whose value starts with `plugin:`, and no Approve button for them.

### Phase 5 permission rules

A `permission_rule` (declarative plugins only) denies matching operations. It **can only make policy stricter**:

- The host turns each enabled plugin's rules into one Invoker Stage (lane 5.2), registered with `command.RegisterStage` from `internal/plugin`'s `init`. `NewInvoker` runs `command.RegisteredStages()` after `Options.Stages` on every call, so CLI, MCP and daemon Invokers all apply them without editing `cli.go` or `mcpserver/server.go`.
- The Stage either calls `next` with the Call unchanged, or stops the chain with an error (`plugin.DeniedError{Plugin, Rule, Message}`, error code `plugin_denied`). It never returns a result of its own, never changes the Call, and never skips the gate: the gate runs after the stages (`GateByInvoker`) or inside Run (`GateInRun`).
- A rule matches by `operation`, `class`, `action`, `origin` and `branch` globs (`manifest.RuleMatch`). Every non-empty list must match.
- `branch` is the branch the call writes. An operation that names a branch (`Detail.Branch`) uses it. The actions `commit`, `push`, `pr_open`, `pr_ready_for_review` and `finish` use the current branch when they name none: commit commits on it, push publishes it, a PR's head is it, and finish deletes it. When one of them cannot determine its branch (detached HEAD, no repository), a `branch` condition matches, so the rule denies. Any other operation writes no branch, and a `branch` condition never matches it, not even `*`.
- With no plugin enabled in the personal layer there are no rules and the Stage does not read the registry, so a broken `installed.json` blocks nothing. With a plugin enabled, a registry that cannot be found or read, or an unreadable personal layer, denies every non-exempt operation and names the file to fix (fail closed).
- Rules never apply to `config.*`, `plugin.*`, `approval.*` and `policy.*` operations, so a person can always disable a plugin or answer an approval, whatever a rule says.
- The Stage reads the registry and the personal layer once per call through a fingerprint cache (the phase 2 MCP reload fingerprint rule), so a new rule applies to the next call without a restart.
- A repository's `plugins.disabled` turns off handlers only. A repository cannot drop a person's permission rules: that would loosen.
- A deny stops the call before `op.requested`, like a validation error (the phase 3 skeleton, step 2), so it publishes no event. The caller sees the rule's message, which is plugin text for display.
- Out-of-process permission rules are reserved: a rule that can crash or time out would have to fail open (loosen) or fail closed (block everything) (decision 6). Phase 7's `PermissionHandler` chain will consult the same rules.

### Phase 5 declarative handlers

For each event matching a handler's `on`:

1. Evaluate `when` (all conditions). Field paths: `type`, `repo`, `worktree`, `source`, `correlation_id`, `payload.<key>[.<key>]`. A missing field is null. Operators: `equals`, `not_equals`, `in`, `exists`.
2. Run `actions` in order. `invoke` takes the path of "Phase 5 command invocation" with the event as `cause`. `annotate` publishes `plugin.annotation.added` with the event's `CorrelationID` as `operation_id`.
3. Templates: `{{event.<path>}}` and `{{settings.<key>}}`. A string that is one placeholder keeps the value's JSON type; otherwise values are inserted as text (a list as comma-separated text). At most 64 KiB per string. There are no functions, conditionals or loops.
4. An action that fails ends the handler for that event, and the event goes to the Dead Letter Channel with code `action`. An `awaiting_approval` result is not a failure.

### Phase 5 settings

Each installed plugin adds fields to the Settings catalog (phase 4 decision 4: "a new source"), built by `manifest.CatalogFields` (lane 5.3) and joined in `consolecfg.RepoCatalog`:

| ID | Widget | Meta |
| --- | --- | --- |
| `personal:plugins.<id>.enabled` | `toggle`, default false | label "Enabled", group `plugins.<id>`, `applies: live`, **`loosens: enable`** |
| `personal:plugins.<id>.settings.<key>` | from the setting's `widget`, else by type: `toggle`, `select` (with `enum`), `text`, `stepper`, `chips` | label, description, `min`, `max`, `min_duration`, `max_duration`, `advanced` from the manifest; `applies` from the manifest (`live` default, or `restart`) |
| `repo:plugins.disabled` (compiled) | `chips` | group `plugins`, label "Plugins turned off here", **`loosens: shrink`** |

- Groups: a compiled group `plugins` ("Plugins", after `init`), and one group `plugins.<id>` per installed plugin, labelled with its name. `settings.View.Groups` comes from the catalog, so the Settings page draws them with no browser change.
- The compiled walk leaves out `personal:plugins` (the plugins add those fields), with a reason in the exclusion table. `TestEveryConfigFieldHasControl` covers `repo:plugins.disabled`.
- A plugin's setting widget must suit its type as compiled fields do (phase 4 table). `credential-ref`, `file-picker`, `branch-picker`, `provider-picker`, `kv-table`, `list` and `sensitive` are refused in phase 5: no secret or path reaches a plugin (decision 7).
- Lane 5.3 adds `config.User.Plugins map[string]UserPlugin` (`enabled`, `settings`) and `config.Repo.Plugins` (`disabled`). Both are optional keys: adding them is backwards compatible.
- The host gives a plugin its effective settings (manifest defaults, then the personal values that pass validation) in `initialize` and in `settings.changed` after a personal-layer change. A `restart` setting restarts the plugin instead.
- Enabling a plugin is a loosening edit: the Save review lists it and needs its tick; `console.autonomy` is related, as for every loosening save.

### Phase 5 limits

| Limit | Value |
| --- | --- |
| Frame | 1 MiB |
| `initialize` | 5 s |
| `event.deliver` | 10 s, one in flight per plugin |
| Ping | every 30 s, 5 s timeout; 2 missed: `degraded`; 4 missed: restart |
| `shutdown` | 2 s, then kill (process group on Unix) |
| Writes in flight when the daemon stops | 10 s, then cancel, then 2 s to record the outcome |
| Plugin requests in flight | 4 |
| `command.invoke` | 10 a minute, burst 5; 60 s each |
| `event.publish` | 60 a minute, burst 20 |
| Subscription buffer | 256 events, phase 1 overflow rule |
| Invoke result | 256 KiB |
| Declarative work per event | 100 ms, actions excluded |
| Restart backoff | 1 s doubling to 5 min; 5 restarts in 10 min: `failed` |
| Log file | 1 MiB, one old file kept |
| Dead letters kept | 1000 |
| Installed plugins | 32 |

Memory and CPU are not limited in phase 5: the standard library cannot set a child's resource limits portably, and per-OS limits are new platform code (decision 9).

### Phase 5 Dead Letter Channel

The host's persistent Dead Letter Channel (`bus.DeadLetter`, as phase 1 promised) is `<home>/plugins/deadletter.jsonl`, mode 0600, the last 1000 letters, rewritten atomically when it passes 1 MiB. A rewrite keeps only the newest letters that fit in 256 KiB, so rewrites stay rare. A stored letter's payload over 4 KiB becomes `{"dropped_bytes": N}` (its event ID and type still name it), and its reason is cut to 1 KiB.

- A letter is `bus.Letter{event, consumer: "plugin:<id>", reason: <code>, time}`. The reason is a code (`timeout`, `crashed`, `exited`, `protocol`, `action`, `rpc_error`), never the plugin's own error text.
- An event goes there when its delivery returns an error, times out, or is in flight when the plugin dies.
- Each letter also publishes the transient `plugin.delivery.failed` in `global`.
- Phase 5 never redelivers. `plugin list --json` and the Plugins page show the letters.
- The file holds envelopes that already sit in the repositories' stores, in the user's private home.

### Phase 5 plugins projection

| Projection | Key | Topics | Reads | State |
| --- | --- | --- | --- | --- |
| `plugins` | repository | `global/*/plugin.lifecycle.changed`, `global/*/plugin.delivery.failed`, `global/*/config.layer.changed`, the repository's `config.layer.changed` | host `Status()` and letters, the registry, the personal layer, the repository's `plugins.disabled` | `PluginsState` |

`internal/webui/projection_plugins_state.go` holds the shapes: `host`, `plugins` (each with ID, name, description, version, tier, hash, install time, `enabled`, `disabled_here`, state, code, since, restarts, `retry_at`, counters, extensions, permissions, `settings_group`) and `dead_letters` (newest first, at most 20, without payloads).

- It registers through `projectionExtensions` from `projection_plugins.go` (phase 3 decision 12). The host starts from a `runtimeExtension` in `stream_runtime_plugins.go` and is reached by the projection through that file, so neither edits `stream_runtime.go`.
- Counters are as of the last plugin event; the host publishes a lifecycle event on every state and health change.
- Every change the page shows arrives over the stream. The page sends no request.

### Phase 5 timeline annotations

- The `timeline` projection also subscribes to `plugin.annotation.added` and attaches each annotation to the operation whose ID is its `operation_id`, at most 5 per operation (`MaxTimelineAnnotations`), oldest first. An annotation for an operation outside the state is not shown.
- `TimelineAnnotation{event_id, plugin, text, url, level, time}`.
- The browser shows the text as plain text and the URL as a plain link, labelled with the plugin ID. Never HTML.

### Phase 5 commands

Lane 5.3 adds these Specs in `internal/command/ops/plugin.go`. No lane edits `cli.go`, `mcpserver/server.go` or `parity_test.go`.

| Spec | CLI | MCP tool | Class, action | Gate | Events | Result |
| --- | --- | --- | --- | --- | --- | --- |
| `plugin.list` | `plugin list [--json]` | `agentflow_plugin_list` (read-only) | `read` | none | none | `{plugins: [...]}` |
| `plugin.add` | `plugin add PATH [--yes] [--json]` | none (decision 3) | `local`, `plugin.install` | in Run, always confirmed | none | the registry entry |
| `plugin.remove` | `plugin remove ID [--yes] [--json]` | none (decision 3) | `local`, `plugin.install` | in Run, always confirmed | none | `{id, removed}` |
| `plugin.approve` | `plugin approve OP_ID [--decline] [--yes] [--json]` | none (decision 3) | `local`, `plugin.approve` | in Run, always confirmed | none | `{operation_id, plugin, command, state, expires_at, next}` |

- `plugin.list` result entries: `id`, `name`, `version`, `tier`, `hash`, `hash_ok` (re-hashed now), `enabled`, `state` and `code` (the newest `plugin.lifecycle.changed` in the `global` store, read as `store.RoleReader`; empty when there is none), `extensions`, `permissions`, `installed_at`. Text output is one line per plugin. It never prints the source path in `--json` output that an MCP client gets.
- `plugin.add` and `plugin.remove` gate through `a.GateOperation` with `RequireConfirmation` (phase 4): `auto` cannot skip the confirmation. `plugin.install` is a new autonomy action (`local`, "Installing or removing plugins requires confirmation."), added to the autonomy catalog by lane 5.3. Adding a key is backwards compatible.
- They set `Policy.AuditByInvoker`, and `Events: ScopeNone`: they change the user's home, not a repository (phase 3, "ScopeNone"). `TestEveryWriteOperationEmitsEvents` lists them with that reason. The Plugins page learns of them from `plugin.lifecycle.changed`.
- The plugin skill is `adapters/claude-code/skills/plugin/SKILL.md`.

### Phase 5 sample plugins

| Sample | Path | Tier | Does |
| --- | --- | --- | --- |
| `ci-notify` | `examples/plugins/ci-notify/agentflow-plugin.yml` | declarative | On `gh.ci.updated` with `state: failure` and a different `previous`, invokes `notify` with the configured signal, `operation_id` `ci-notify-<head sha>` (so retries deduplicate) and the failed checks. With no route for the signal, `notify` reports "disabled" and nothing is sent; with one, the plugin's write waits for a person (the confirm ceiling). |
| `timeline-hints` | `examples/plugins/timeline-hints/` (`main.go`, `agentflow-plugin.yml`, `.gitignore` for the built binary) | process | Subscribes to `op.failed` and publishes a `plugin.annotation.added` with a fixed hint per code (`operation_changed`: "The candidate changed after review. Prepare it again."; `approval_expired`, `invalid_token`, `blocked`, ...). Standard library only, about 150 lines, speaking the raw protocol so authors see it. Built with `go build -o timeline-hints .` before `plugin add`. |

**Phase demo.**

1. `agentflow plugin add examples/plugins/ci-notify`, confirm; enable it on the Settings page (tick the loosening).
2. A failing CI run on a served branch shows `op.requested` and its outcome from client `plugin:ci-notify` on the timeline.
3. Build and add `timeline-hints`, enable it; a failed operation (for example `commit --yes` with a stale token) shows its hint on the timeline.
4. The Plugins page shows both as `running`. Killing the `timeline-hints` process shows `backoff`, then `running` again.

### Phase 5 Plugins page (lane 5.4)

- One page, `#plugins`: one card per plugin, from `PluginsState` only.
- Each card: name, version, tier, state with its code and the retry time, enabled and "turned off in this repository", what it adds (handlers with their events and the commands they invoke or annotations they add; permission rules with their deny message), what it may do (permissions), counters, and its dead letters.
- **Enable** opens the Settings group `plugins.<id>`. The page has no enable, install or remove endpoint of its own: install and remove show the CLI command to copy (decision 3).
- Plugin text (names, descriptions, deny messages) is shown as text, never HTML.
- The timeline shows annotations under their operation (lane 5.4 owns that rendering in `static/timeline.*`).

### Phase 5 amendments to earlier contracts

All additive.

| Contract | Change | Lane |
| --- | --- | --- |
| `bus` registry | `plugin.*` types registered; `plugin` leaves `ReservedFamilies` | this PR |
| `command` | `JSONCommand` (Spec decodes a JSON input strictly and invokes); `RegisterStage`, `RegisteredStages`, run by `NewInvoker` after `Options.Stages` on every call | 5.2 |
| `policy.Gate` | `WithCeiling(level)`: `Explain` caps every resolved level at the ceiling | 5.2 |
| `app.App` | `WithAutonomyCeiling(level)`: a copy whose gate has the ceiling | 5.2 |
| `config.User`, `config.Repo` | optional `plugins` sections | 5.3 |
| autonomy catalog | new action `plugin.install` (`local`) | 5.3 |
| settings catalog | plugin fields and groups; `View.Groups` from the catalog | 5.3 |
| user watch | `plugins/installed.json` as a personal `config.layer.changed` | 5.3 |
| timeline state | `Annotations` | this PR (type), 5.2 (projection) |
| `TestEveryWriteOperationEmitsEvents` allow-list | `plugin.add`, `plugin.remove` (user home, no repository) | 5.3 |

### File ownership for phase 5 lanes

A lane changes only the files it owns. A lane that needs another lane's file, or a contract change, stops and reports.

| Lane | Issue | Owns | Must not touch |
| --- | --- | --- | --- |
| 5.1 JSON-RPC and launcher | #78 | `internal/jsonrpc/**` except `types.go` and `doc.go`; `internal/jsonrpc/jsonrpctest/**` | `internal/provider/codexhost/**`, `internal/plugin/**`, every other package, `go.mod` |
| 5.2 Plugin host | #79 | `internal/plugin/*.go` except `types.go`, `rpc.go`, `doc.go`; `internal/plugin/plugintest/**`; `internal/command/invoke_json.go`, `internal/command/stages.go` and the registered-stages call in `internal/command/invoker.go`; `internal/policy/ceiling.go` and the clamp in `Gate.Explain` (`policy.go`); `internal/app/plugin_gate.go`; `internal/webui/stream_runtime_plugins.go`, `internal/webui/projection_plugins.go`, the annotation part of `internal/webui/projection_timeline.go`, and their tests | `internal/plugin/manifest/**`, `internal/plugin/install/**`, `internal/jsonrpc/**`, `internal/webui/static/**`, `internal/config/**`, `internal/consolecfg/**`, `internal/settings/**`, `cli.go`, `server.go`, `parity_test.go` |
| 5.3 Commands, samples, docs | #80 | `internal/plugin/manifest/**` except `types.go` and `doc.go`; `internal/plugin/install/**`; `internal/app/plugins.go`; `internal/command/ops/plugin.go`; `internal/config/plugins.go`, the `Plugins` fields in `config.go`, `plugin.install` in `autonomy.go`, the `repo:plugins.disabled` entry in `field_meta_table.go`, the `plugins` group and `personal:plugins` exclusion in `catalog.go`; `internal/consolecfg/catalog.go`; `internal/settings/view.go`; `internal/watch/user.go` (`installed.json`); the plugin rows in `internal/command/ops/emit_test.go`; `examples/plugins/**`; `docs/plugins.md`; `adapters/claude-code/skills/plugin/SKILL.md`; README sections on plugins; the blueprint capability table; golden files through `-update` only | `internal/plugin/*.go`, `internal/jsonrpc/**`, `internal/webui/**`, `cli.go`, `server.go`, `parity_test.go` |
| 5.4 Plugins page | #81 | `internal/webui/static/plugins.*`; the Plugins nav entry, page section and script tag in `static/index.html`; plugins rules in `static/style.css`; annotation rendering in `static/timeline.*`; `internal/webui/plugins_browser_test.go`; `internal/webui/testdata/plugins/**`; `docs/design/ui/plugins.md`; the README section on the Plugins page | every other Go file and static file |

- All four lanes can start at once.
- Lane 5.2 codes against `jsonrpc.Conn` (with `jsonrpctest.Pipe` once 5.1 merges, a fake before), a fake `manifest.Source` and a fake `plugin.Layers`. It wires `jsonrpc.Launch`, `manifest.NewFileSource` and the production `Layers` in its last commit, after lanes 5.1 and 5.3 merge.
- Lane 5.3 should merge its manifest package early; it may be its own PR.
- Lane 5.4 builds against `PluginsState` and `TimelineAnnotation` with a scripted stream fixture until 5.2 merges.
- `README.md` is shared by section: 5.3 writes the commands, plugins and security sections, 5.4 the Plugins page section.
- Exported contract types change only through an amendment: everything in the code-form table at the top of this section.

### Phase 5 security

**Threat model.** A plugin's author may be hostile, and a plugin's input (event payloads: PR titles, check names) may be hostile. The person who runs `plugin add` is trusted, and reviews what the confirmation shows.

**Security review (lanes 5.1, 5.2 and 5.3, before merge).**

- [ ] `plugin add` reads only a local path; no code path downloads, clones, or runs a package manager. Symbolic links and files outside the tree are refused.
- [ ] The installed copy is hashed at install and verified before every start; a changed copy never runs.
- [ ] `plugin add` and `plugin remove` always ask, under every autonomy level, with every permission in the summary. No MCP tool installs or removes a plugin.
- [ ] A repository can never install, enable or loosen anything for a plugin; it can only turn handlers off for itself.
- [ ] A child's environment is the scrubbed list only; a test proves tokens, keys and session IDs of the daemon do not reach it. HOME and TMPDIR point into the plugin's data directory.
- [ ] Launch never searches PATH and refuses an executable outside the installed copy.
- [ ] A plugin never receives `approval.*`, `client.*`, `ui.*` or `global` events, and never publishes anything but `plugin.annotation.added`.
- [ ] A plugin never sees or sends a confirm token; its writes run under a `confirm` ceiling and wait for a person; it cannot invoke `approval.*`, `policy.*`, `config.*` or `plugin.*`.
- [ ] A permission rule can only deny; it never applies to `config.*`, `plugin.*`, `approval.*` or `policy.*`.
- [ ] No event, projection, letter or log line outside `data/plugin.log` carries a plugin's own text other than display fields that are clamped (annotation text, names, deny messages), and the console shows those as text.
- [ ] A crashing, hanging, flooding or malformed plugin cannot block the bus, the daemon or another plugin (tests for each).
- [ ] Settings reaching a plugin hold no secret or path (`credential-ref`, pickers and `sensitive` refused).

### Phase 5 decisions

*Decided by the architect in the contract PR. The coordinator confirms or changes them before the lanes start.*

1. **Plugins are per user and run in the daemon.** Installed in the agentflow home and enabled in the personal layer. A repository can only turn handlers off. No repository-provided plugin in phase 5: a clone must never run code.
2. **Installed disabled; enabling is a Settings change.** `plugin add` installs, and `personal:plugins.<id>.enabled` (a loosening toggle) starts it. This reuses the phase 4 Save review instead of a second enable command.
3. **`plugin add`, `plugin remove` and `plugin approve` are person-only CLI commands.** They refuse agent sessions and non-terminals, ignore `--yes`, and the pre-bash never-list blocks agents from running them in every mode. (Originally: **`plugin add` and `plugin remove` are CLI only.**) Changes the approved plan's "commands and MCP tools" reading.** `agentflow_plugin_list` is the only plugin MCP tool. Installing code is the terminal user's act: an agent that wrote a plugin should not also be the one asking to install it. Parity holds: every tool has a CLI command; CLI-only commands already exist (`install`, `login`).
4. **A short invocable allow-list.** Writes `notify` and `comment`; reads `ship.status`, `context`, `ac.checklist`, `review.threads`. Widening it is an amendment.
5. **A `confirm` ceiling on every plugin write.** Under `auto` a plugin's write still waits for a person. The CI sample therefore asks once per failing head commit when a route exists. A per-plugin autonomy setting (a loosening field) is a possible later amendment.
6. **Permission rules are declarative only.** An out-of-process rule must fail open (loosening) or closed (a crashed plugin blocks every command); neither is acceptable.
7. **No secrets reach plugins in phase 5.** No `credential-ref` settings, no inherited environment. A plugin that needs a secret is out of scope until an amendment defines secret delivery.
8. **The scrubbed environment is not a sandbox.** A process plugin runs as the user and can read the user's files and use the network. The confirmation, the hash and the docs say so plainly. OS sandboxing would need platform code or a dependency.
9. **No memory or CPU limits, and no Windows job objects.** Resource limits need cgroups or job objects. `golang.org/x/sys` is already a dependency, so this needs no new module, but it is new platform code per OS. Timeouts, rate limits, bounded queues and process-group kill (Unix) are the phase 5 limits. Flagged in case the owner wants job objects on Windows.
10. **Lifecycle events live in `global`, and streams gain no `global` plugin topic.** The `plugins` projection carries the state in each repository's frames, as the overview does for personal-layer changes.
11. **The sample "notification" is the `notify` command.** It sends through the configured notification routes and is gated like any write. A console-only toast is not a phase 5 event type.

**Open questions for the coordinator.**

- Decision 3 drops MCP tools for add and remove. Confirm, or ask for `agentflow_plugin_add` and `agentflow_plugin_remove` (confirmed by the prompt owner like every write).
- Decision 5 makes the CI sample ask a person per failure when a route exists. Confirm, or add `personal:plugins.<id>.autonomy` (loosening) now.
- Decision 9: accept no Windows job objects in phase 5.

### Phase 5 consequences

- Plugins extend agentflow without changing its code, in three tiers behind one interface, and a broken plugin only breaks itself.
- No plugin can loosen policy, confirm its own write, see an approval or read the daemon's secrets from its environment.
- Every plugin action is an ordinary operation on the timeline, with client `plugin:<id>`, and every plugin change is an ordinary Settings save.
- Phases 6 and 7 reuse `internal/jsonrpc` and the extension-point list (`harness`, the permission rules in the ACP chain).
- Consuming repositories see no change unless a person installs and enables a plugin.

## Phase 6

*Amendment for phase 6 (issue #97), lanes 6.1 to 6.4 (issues #100 to #103).* Phase 6 detects the ACP harnesses installed on this machine. `agentflow ai harnesses` and `agentflow ai doctor` list each one with its state and the exact fix, and a Harnesses page shows the same list live as harnesses are installed or removed. Phase 6 never installs anything and never starts a session: the only things it may run are an installed harness's version command, one ACP `initialize` request, and the harness's own sign-in status command where agentflow has a verified one.

**Harness binaries are the person's programs, but agentflow chooses when to start them.** Every rule in this section follows from that: agentflow starts only a binary that the person put on their own PATH or named in their own personal settings, only with arguments from agentflow's own data or the person's, with a scrubbed environment (the person's real home only for the sign-in status command) and a timeout, and never a package runner, a download or a binary a repository put in reach.

Everything in the phase 1 to 5 sections still holds unless a paragraph here says it changes.

**No contract Go files in this PR.** Unlike phases 1 to 5, the phase 6 contract PR is documentation only. The Go declarations below are the contract. Each contract file belongs to exactly one lane, and that lane lands it first, verbatim, as a small PR of its own (as lane 5.3 merged its manifest package early). Until it merges, the other lanes code against the declarations here.

| Contract | Go | First PR of lane |
| --- | --- | --- |
| `Harness`, `Definition` use, states, codes, `Report`, `Found`, `ProbeRecord`, `Store`, `Prober`, limits | `internal/harness/types.go`, `internal/harness/doc.go` | 6.1 |
| Harness definition (registry overlay, personal override, plugin extension) | `internal/harness/harnessdef/types.go` | 6.1 |
| ACP v1 `initialize` types, methods, error codes | `internal/acp/types.go`, `internal/acp/doc.go` | 6.2 |
| Event types and payloads | `internal/bus/types_phase6.go`; `harness` leaves `ReservedFamilies` in `internal/bus/types.go` | 6.2 |
| Harnesses projection state | `internal/webui/projection_harnesses_state.go` (`HarnessesState`) | 6.2 |
| Harness launch helpers (added by lane 6.1) | `jsonrpc.HarnessEnv`, `jsonrpc.Run` in `internal/jsonrpc/harness.go` | 6.1 |

### Phase 6 patterns

| Piece | GoF / EIP pattern | Go |
| --- | --- | --- |
| Definition layers: registry, agentflow overlay, plugins, personal overrides | Composite; later layers win field by field | `harness.Registry` |
| Registry JSON to a definition | Message Translator | `internal/harness/acpregistry` |
| Detection states and their fixes | State (a decision table, not objects); Strategy per distribution kind (`npx`, `uvx`, `binary`) for install text | `harness.Resolve`, `harness.Fix` |
| `initialize` over stdio | Proxy (GoF); Request-Reply with a timeout (EIP) | `harness.Prober` over `jsonrpc.Conn` |
| Probe results reused until the binary changes | Idempotent Receiver keyed by binary identity | `harness.Store` (`cache.json`) |
| Daemon detection | Polling Consumer, plus an event-driven wake on focus and personal-layer changes | `internal/watch/harness.go` |
| Agent-supplied text (names, auth methods) | Content Filter | `harness.Sanitize*` |
| A frame that is not JSON-RPC | Invalid Message Channel (the connection) | phase 5 rule, code `protocol` |
| Live page | Event Notification with a server-side projection | `harness.*` → the `harnesses` projection |

### Phase 6 pinned ACP registry copy (lane 6.1)

**Source.** The official registry is the repository `agentclientprotocol/registry` (<https://github.com/agentclientprotocol/registry>), Apache-2.0. Its CDN index (`https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json`) has no commit, so agentflow pins the repository's per-agent files instead.

| What | Value |
| --- | --- |
| Pinned commit | `4273056f03b93f8ce41e9276256ea47b057b794c` (committed 2026-11-26T23:54:34Z, branch `main`) |
| Files copied, verbatim | `<id>/agent.json` for every top-level directory that has one (46 at this commit), `quarantine.json`, `agent.schema.json`, `LICENSE` |
| Files never copied | `icon.svg` (SVG in the console is a risk), workflows, protocol-matrix snapshots, the docs |
| Where | `internal/harness/acpregistry/v1/agents/<id>.json`, `.../v1/quarantine.json`, `.../v1/agent.schema.json`, `.../v1/LICENSE`, and `.../v1/SOURCE.json` |
| Refresh | `scripts/refresh-acp-registry.sh COMMIT` |
| At runtime | embedded with `//go:embed`; **never fetched** |

`SOURCE.json`:

```json
{
  "repository": "https://github.com/agentclientprotocol/registry",
  "commit": "4273056f03b93f8ce41e9276256ea47b057b794c",
  "committed_at": "2026-11-26T23:54:34Z",
  "agents": 46,
  "spec": {
    "repository": "https://github.com/agentclientprotocol/agent-client-protocol",
    "tag": "schema-v1.35.0",
    "commit": "065b57951b6b2a91ac5eddd249e145d1f6d54545"
  }
}
```

**The refresh script** (`scripts/refresh-acp-registry.sh`, bash 3.2, shellcheck-clean, `curl` only, no `jq`):

1. Takes a full 40-hex commit. It refuses a branch name, a tag or a short SHA, so a refresh is always a reviewed pin.
2. Reads the tree at that commit from `https://api.github.com/repos/agentclientprotocol/registry/git/trees/<commit>?recursive=1` and downloads each listed file from `https://raw.githubusercontent.com/agentclientprotocol/registry/<commit>/<path>` into a new temporary directory.
3. Replaces `internal/harness/acpregistry/v1/` with the files above and writes `SOURCE.json` (with `committed_at` from the commit API and the count).
4. Runs `go test ./internal/harness/acpregistry/` so a schema change fails the refresh before review.

It is run by a maintainer, by hand, before a release. No workflow runs it, and nothing in agentflow fetches the registry at runtime.

**Go shape** (`acpregistry.Agent`, decoded with unknown fields refused, from `agent.schema.json` at the pinned commit):

```go
type Agent struct {
    ID          string       `json:"id"`          // ^[a-z][a-z0-9-]*$
    Name        string       `json:"name"`
    Version     string       `json:"version"`     // X.Y.Z
    Description string       `json:"description"`
    Repository  string       `json:"repository,omitempty"`
    Website     string       `json:"website,omitempty"`
    Authors     []string     `json:"authors,omitempty"`
    License     string       `json:"license,omitempty"`
    LicenseURL  string       `json:"license_url,omitempty"`
    Icon        string       `json:"icon,omitempty"`    // decoded, never used
    Distribution Distribution `json:"distribution"`
    Preview     *Preview     `json:"preview,omitempty"` // decoded, never used: preview builds are unverified
}

type Distribution struct {
    Binary map[string]BinaryTarget `json:"binary,omitempty"` // keys: darwin-aarch64, darwin-x86_64, linux-aarch64, linux-aarch64-musl, linux-x86_64, linux-x86_64-musl, windows-aarch64, windows-x86_64
    NPX    *Package                `json:"npx,omitempty"`
    UVX    *Package                `json:"uvx,omitempty"`
}

type BinaryTarget struct {
    Archive string            `json:"archive"`          // https URL
    SHA256  string            `json:"sha256,omitempty"` // 64 hex
    Cmd     string            `json:"cmd"`              // e.g. "./goose"
    Args    []string          `json:"args,omitempty"`
    Env     map[string]string `json:"env,omitempty"`
}

type Package struct {
    Package string            `json:"package"` // name with optional version
    Args    []string          `json:"args,omitempty"`
    Env     map[string]string `json:"env,omitempty"`
}

type Preview struct {
    Version      string       `json:"version"`
    Distribution Distribution `json:"distribution"`
}
```

- **Registry text is data.** Name and description are display text: one line, control and bidirectional characters removed, at most 64 and 256 bytes. URLs must be `https`. A package name must match `^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*(@[0-9A-Za-z.+-]+)?$` (`npx`) or `^[A-Za-z0-9][A-Za-z0-9._-]*(\[[A-Za-z0-9,._-]+\])?([=@<>~!]=?[0-9A-Za-z.*+-]+)?$` (`uvx`). Args must match `^[A-Za-z0-9._=:/@+,-]{1,128}$`, at most 16. A value that fails is dropped with a `registry` problem, never shown or used.
- **Registry `env` is never applied** to a probe. An entry that sets one gets the note `registry_env_ignored`.
- **Quarantine.** At the pinned commit `quarantine.json` is a map of ID to reason. The registry's own CI skips those agents when it verifies agents and updates versions (`verify_agents.py`, `update_versions.py`), but `build_registry.py` still publishes them. agentflow keeps quarantined agents in the catalog, flags them (`quarantined: true`), and shows the note "The ACP registry has quarantined this agent: <reason>." The reason is registry text, clamped like a description.
- `TestPinnedRegistry` (lane 6.1) decodes every file strictly, checks `SOURCE.json` against the files, and fails when an agent fails the rules above.

### Phase 6 ACP `initialize` slice (lane 6.2)

`internal/acp` is the package phase 7 lane 7.1 owns. Lane 6.2 creates the smallest part phase 6 needs; lane 7.1 extends it and must keep these names, fields and behaviour. Shapes are from the ACP v1 schema at `schema-v1.35.0` (commit `065b57951b6b2a91ac5eddd249e145d1f6d54545`), checked against `schema/v1/schema.json` and `docs/protocol/v1/initialization.mdx` and `authentication.mdx` there.

```go
package acp

// ProtocolVersion is the only ACP major version agentflow speaks.
// On the wire it is an integer (uint16).
const ProtocolVersion = 1

// Methods used in phase 6. Phase 7 adds the session methods.
const MethodInitialize = "initialize"

// ACP error codes. They overlap internal/jsonrpc's agentflow codes
// (-32002 is "resource not found" in ACP and "rate limited" in jsonrpc):
// an error from an ACP peer is always read by these meanings, never by the
// jsonrpc.Code* constants, which agentflow sends only to its own plugins.
const (
    CodeAuthRequired     = -32000
    CodeResourceNotFound = -32002
    CodeRequestCancelled = -32800
)

type InitializeRequest struct {
    ProtocolVersion    int                `json:"protocolVersion"`
    ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
    ClientInfo         *Implementation    `json:"clientInfo,omitempty"`
}

type ClientCapabilities struct {
    FS       FileSystemCapabilities `json:"fs"`
    Terminal bool                   `json:"terminal"`
    Auth     AuthCapabilities       `json:"auth"`
}

type FileSystemCapabilities struct {
    ReadTextFile  bool `json:"readTextFile"`
    WriteTextFile bool `json:"writeTextFile"`
}

type AuthCapabilities struct {
    Terminal bool `json:"terminal"`
}

type Implementation struct {
    Name    string `json:"name"`
    Title   string `json:"title,omitempty"`
    Version string `json:"version"`
}

type InitializeResponse struct {
    ProtocolVersion   int               `json:"protocolVersion"`
    AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
    AuthMethods       []AuthMethod      `json:"authMethods"`
    AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
}

type AgentCapabilities struct {
    LoadSession         bool               `json:"loadSession"`
    PromptCapabilities  PromptCapabilities `json:"promptCapabilities"`
    MCPCapabilities     MCPCapabilities    `json:"mcpCapabilities"`
    SessionCapabilities json.RawMessage    `json:"sessionCapabilities,omitempty"` // kept raw; phase 7 decodes it
    Auth                AgentAuthCapabilities `json:"auth"`
}

type PromptCapabilities struct {
    Image           bool `json:"image"`
    Audio           bool `json:"audio"`
    EmbeddedContext bool `json:"embeddedContext"`
}

type MCPCapabilities struct {
    HTTP bool `json:"http"`
    SSE  bool `json:"sse"`
}

type AgentAuthCapabilities struct {
    Logout *struct{} `json:"logout,omitempty"`
}

// AuthMethod is one advertised sign-in method. Type "" means "agent".
type AuthMethod struct {
    ID          string            `json:"id"`
    Name        string            `json:"name"`
    Description string            `json:"description,omitempty"`
    Type        string            `json:"type,omitempty"` // "", "agent" or "terminal"
    Args        []string          `json:"args,omitempty"` // terminal only
    Env         map[string]string `json:"env,omitempty"`  // terminal only
}

// Initialize sends initialize on c and decodes the answer as the ACP schema
// says: unknown fields are ignored (ACP is extensible); protocolVersion is
// required and must be an integer from 0 to 65535, else ErrProtocol; a
// capability object, agentInfo or clientInfo that fails to decode becomes
// its zero value ("default on error"); an authMethods item that fails to
// decode is dropped ("skip invalid items"). A peer error is returned as
// *jsonrpc.Error. Lane 7.1 keeps this rule for every ACP message.
func Initialize(ctx context.Context, c jsonrpc.Conn, req InitializeRequest) (InitializeResponse, error)
```

**The probe's request is fixed:**

```json
{"protocolVersion": 1,
 "clientCapabilities": {"fs": {"readTextFile": false, "writeTextFile": false}, "terminal": false, "auth": {"terminal": true}},
 "clientInfo": {"name": "agentflow", "title": "agentflow", "version": "<agentflow version>"}}
```

`auth.terminal` is true so that agents list their terminal sign-in methods. The spec allows that when the client "can reproduce the configured Agent invocation in an interactive terminal". agentflow does it by showing the person the exact command to run. agentflow itself never runs a terminal sign-in and never calls `authenticate`.

**The fake agent** (`internal/acp/acptest`, lane 6.2; lane 7.1 adds session scripts):

- `acptest.Script{Initialize *acp.InitializeResponse; InitializeError *jsonrpc.Error; Delay time.Duration; ExitBefore bool; Garbage bool; Version string}`.
- `acptest.Install(tb, dir, name string, s Script) (args []string)` puts an executable `name` in `dir` and returns any arguments the definition must pass. On Unix it writes a `/bin/sh` wrapper that runs the test binary in the `acptest-agent` child mode (`jsonrpctest.RegisterChild`), with the script encoded in the wrapper. On Windows it copies the test binary to `name.exe` and returns the child-mode arguments. A test binary using it calls `jsonrpctest.RunChild` from `TestMain`.
- `cmd/fake-acp` (`internal/acp/acptest/cmd/fake-acp/main.go`): a standalone fake for the phase demo. It takes `--mode=ok|auth|v2|hang|exit|garbage` and prints `fake-acp 1.2.3` for `--version`. It is only built by hand for the demo.

### Phase 6 harness definitions (lane 6.1)

A **definition** says how to find and start one harness. It is the same shape in every layer (`internal/harness/harnessdef`, a leaf package that `config`, `plugin/manifest` and `harness` all import):

```go
package harnessdef

type Kind string

const (
    KindNative  Kind = "native"  // the product's own CLI speaks ACP (gemini --acp, goose acp)
    KindAdapter Kind = "adapter" // a separate program bridges a CLI to ACP (claude-agent-acp)
)

// Definition is how agentflow finds and probes one harness. Empty fields
// inherit from the layer below.
type Definition struct {
    Name string `yaml:"name,omitempty" json:"name,omitempty"`
    Kind Kind   `yaml:"kind,omitempty" json:"kind,omitempty"`
    // Command is the executable: a bare name looked up on PATH, or (personal
    // layer only) an absolute path.
    Command string   `yaml:"command,omitempty" json:"command,omitempty"`
    // Args start the ACP server on stdio.
    Args []string `yaml:"args,omitempty" json:"args,omitempty"`
    // VersionArgs print the version. Empty means ["--version"]; NoVersion
    // means never run a version command.
    VersionArgs []string `yaml:"version_args,omitempty" json:"version_args,omitempty"`
    NoVersion   bool     `yaml:"no_version,omitempty" json:"no_version,omitempty"`
    // BaseName and BaseCommands name the CLI an adapter bridges. When the
    // adapter is missing and a base command is found, the state is
    // adapter_available.
    BaseName     string   `yaml:"base_name,omitempty" json:"base_name,omitempty"`
    BaseCommands []string `yaml:"base_commands,omitempty" json:"base_commands,omitempty"`
    // MinVersion is the oldest version agentflow accepts (X.Y.Z).
    MinVersion string `yaml:"min_version,omitempty" json:"min_version,omitempty"`

    // Sign-in check ("Phase 6 sign-in check"). Only the agentflow overlay
    // and the personal layer may set these; Validate refuses them in a
    // plugin definition.
    //
    // StatusFrom is "self" (the found command) or "base" (the adapter's
    // found base CLI); empty means no check. StatusArgs are its arguments.
    StatusFrom string   `yaml:"status_from,omitempty" json:"status_from,omitempty"`
    StatusArgs []string `yaml:"status_args,omitempty" json:"status_args,omitempty"`
    // StatusSignedIn and StatusSignedOut are exit codes. Empty means [0]
    // and [1]. StatusSignedOutLine, when set, must also appear as one
    // whole trimmed line of output for a signed-out verdict.
    StatusSignedIn      []int  `yaml:"status_signed_in,omitempty" json:"status_signed_in,omitempty"`
    StatusSignedOut     []int  `yaml:"status_signed_out,omitempty" json:"status_signed_out,omitempty"`
    StatusSignedOutLine string `yaml:"status_signed_out_line,omitempty" json:"status_signed_out_line,omitempty"`
    // CredentialEnv names variables that also sign the harness in; with one
    // set, a signed-out answer is "unknown". ConfigEnv names directory
    // variables passed through to the status command.
    CredentialEnv []string `yaml:"credential_env,omitempty" json:"credential_env,omitempty"`
    ConfigEnv     []string `yaml:"config_env,omitempty" json:"config_env,omitempty"`
    // StatusSource cites the vendor documentation or source (overlay only).
    StatusSource string `yaml:"status_source,omitempty" json:"status_source,omitempty"`
}

// Layer is the layer a definition comes from: "registry", "agentflow",
// "plugin" or "personal" (constants LayerRegistry ... LayerPersonal).
type Layer string

// Validate checks d for the layer it comes from.
func Validate(id string, d Definition, layer Layer) error
```

Flat fields (no nested `base` object) keep each override one row in a `kv-table` (below).

**Layers**, lowest first (Composite: a non-empty field of a higher layer replaces the field below; lists replace, they do not merge):

| Layer | `Source` | Where | May |
| --- | --- | --- | --- |
| Registry | `registry` | the pinned copy | name, description, version, distribution |
| agentflow overlay | `agentflow` | `internal/harness/overlay.yml`, embedded | set every field for a registry ID, from the pinned registry entries only |
| Plugins | `plugin:<id>` | `extensions.harness` of an enabled declarative plugin | add **new** IDs only; bare command names only; no `status_*`, `credential_env` or `config_env` |
| Personal | `personal` | `personal:ai.harnesses.overrides.<id>` | change any field of any ID, or add an ID; absolute command paths allowed |

A repository has **no** harness layer: `repo` config has no `ai` keys in phase 6, and a repository's provider definitions, plugins or client settings cannot add or change a definition.

**Defaults when no layer sets a field:**

- `command`: for a `binary` distribution, the base name of the current platform's `cmd` (`./dist-package/cursor-agent` → `cursor-agent`); for `uvx`, the package name without version or extras; for `npx`, the last path segment of the package without its version (`@agentclientprotocol/codex-acp@2.12.1` → `codex-acp`). The overlay corrects the ones where the npm bin name differs (`@google/gemini-cli` → `gemini`).
- `args`: the current platform's `binary` args, else `npx` args, else `uvx` args.
- `kind`: `native`. The overlay marks adapters.

**The agentflow overlay** (`overlay.yml`) is agentflow data, reviewed like code, built only from the pinned entries. Lane 6.1 writes at least these, checked against the pinned files:

| ID | kind | command | args | base |
| --- | --- | --- | --- | --- |
| `claude-acp` | adapter | `claude-agent-acp` | | Claude Code, `claude` |
| `codex-acp` | adapter | `codex-acp` | | Codex, `codex` |
| `gemini` | native | `gemini` | `--acp` (registry) | |
| `github-copilot-cli` | native | `copilot` | `--acp` (registry) | |
| `goose` | native | `goose` (default) | `acp` (registry) | |
| `opencode` | native | `opencode` (default) | `acp` (registry) | |
| `cursor` | native | `cursor-agent` (default) | `acp` (registry) | |

The lane checks every other pinned entry: where the default command is wrong, or the entry is an adapter, it adds a row. `TestOverlayMatchesRegistry` fails when an overlay ID is not in the pinned copy.

**Validation** (`harnessdef.Validate`; problems are reported with their layer and ID, and the definition is left out, never half-applied):

- ID `^[a-z][a-z0-9-]{0,63}$`. A plugin ID that equals a registry or overlay ID is refused (`id_taken`).
- `command`: a bare name `^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`, or an absolute path in the personal layer. Refused everywhere (`launcher`), compared without case and without `.exe`: `npx`, `npm`, `pnpm`, `pnpx`, `yarn`, `bun`, `bunx`, `deno`, `uv`, `uvx`, `pip`, `pip3`, `pipx`, `python`, `python3`, `node`, `sh`, `bash`, `zsh`, `fish`, `env`, `sudo`, `doas`, `curl`, `wget`, `cmd`, `powershell`, `pwsh`. Phase 6 never runs a package runner, an interpreter, a shell or a downloader. ("Phase 6 security review amendments", item 6, adds a version-suffix rule and more names.)
- `args`, `version_args`, `base_commands`: at most 16 items, each matching the registry arg rule above (`args`).
- `min_version`: `X.Y.Z` (`version`). `name`: one line, at most 64 bytes.
- A definition with no command after layering is listed with its registry data only, as `not_installed`, and never probed.
- `status_from` is `self` or `base` (`base` only for kind `adapter`); `status_signed_out_line` is one line of at most 128 bytes; `credential_env` and `config_env` names match `^[A-Z_][A-Z0-9_]{0,63}$`, and `config_env` refuses the launch-safety names listed under "Sign-in guidance" and every name `credential_env` or the generic credential suffixes match (`status`). An overlay entry with a status command must have `status_source` (`TestOverlayMatchesRegistry`).

**Plugins.** Phase 6 implements the `harness` extension point for **declarative plugins only**: `extensions.harness` is a list of `{id, ...Definition}`. A process plugin's harness entry is refused (`rule`). The entries are data: listed in `plugin add`'s confirmation summary with their command and args, and active only while the plugin is enabled (the phase 5 loosening tick). A repository's `plugins.disabled` does not hide them: harnesses are per user, not per repository. `manifest.PointPhase[PointHarness]` becomes 6.

### Phase 6 finding a harness on PATH (lane 6.1)

The finder looks up each definition's `command`, then its `base_commands`, in the PATH of the process that runs detection: the CLI's own PATH, or the daemon's. Every PATH entry and every found file passes these checks first. A skipped entry is reported once in `Report.SkippedPath` with its reason, and `ai doctor` lists it.

| Check | Skip reason |
| --- | --- |
| Empty, `.` or relative entry | `relative` |
| Entry does not exist or is not a directory | `missing` (not reported) |
| Entry, after resolving links, is inside a worktree of a repository agentflow is working in: for the CLI, any worktree of the repository that contains its working directory; for the daemon, any worktree of every repository it serves (catches direnv and a repository's `bin/`) | `inside_repository` |
| Entry has a `node_modules` path segment (a project's `node_modules/.bin`) | `node_modules` |
| Unix: the directory is writable by group or others without the sticky bit, or owned by neither the user nor root | `unsafe_permissions` |
| Unix: the found file, after resolving links, is writable by group or others, or owned by neither the user nor root | `unsafe_permissions` (that file only) |
| Windows: the found file is `.cmd` or `.bat` (`cmd.exe` would re-parse the arguments) | `shim` (that file only) |
| In a test binary: the entry is outside the test's temporary directory (below) | `test_guard`, and detection fails |

- The rule is not "any directory with a `.git` above it". Homebrew (`/opt/homebrew/.git`), nvm, asdf and pyenv are git clones, and their `bin` directories are where most harnesses live. The threat is a repository the person is working in, so the check is against those repositories' worktrees.
- A personal absolute `command` skips the PATH walk and the `inside_repository` and `node_modules` checks (the person named that file), but not the permission checks.
- **The agentflow home.** agentflow ignores a personal home inside a repository, with a warning, and uses the account's own `~/.agentflow` instead; that general rule is fixed in its own issue (#105), not in phase 6. Phase 6 reads personal overrides and plugin definitions only from a home that rule accepts, and in addition from one owned by the user and not writable by group or others; otherwise both layers are ignored and `ai doctor` reports `home_unsafe`.
- **Identity.** A found harness has an identity: the PATH path, the resolved target, its size, modification time, and device and inode on Unix, plus a hash of the effective `args` and `version_args`. Any change is a new identity, and old probe results no longer apply.

### Phase 6 harness launch profile (lanes 6.1 and 6.2)

Probes run through the lane 5.1 launcher with a profile for PATH binaries. Lane 6.1 adds `internal/jsonrpc/harness.go` and `LaunchSpec.DataDir string` (additive, no change to existing behaviour). `DataDir`, when set, must be an absolute path to an existing directory, and `Dir` must then be inside `Root` or `DataDir` (links resolved); nothing is derived from the environment (neither `AGENTFLOW_PLUGIN_DATA` nor the parent of `HOME`). When it is empty, `Launch` and `Run` behave as before. `Run` keeps the first `maxStdout` bytes of stdout (its argument).

```go
// HarnessEnv is ScrubbedEnv(parent, data, nil) with PATH set to pathDirs
// followed by the fixed system PATH. pathDirs must be absolute.
func HarnessEnv(parent []string, data string, pathDirs []string) ([]string, error)

// RunResult is a finished child that does not speak JSON-RPC.
type RunResult struct {
    Stdout   []byte // at most maxStdout bytes, the first ones
    ExitCode int    // -1 when killed
    TimedOut bool
}

// Run starts s like Launch (same path, root, directory and environment
// checks, same process group) without a connection, waits for it to exit
// or ctx to end, and kills the group when ctx ends.
func Run(ctx context.Context, s LaunchSpec, maxStdout int) (RunResult, error)
```

| Profile field | Value |
| --- | --- |
| `Path` | the found file as found on PATH (not resolved) |
| `Root` | the deepest directory that contains both the PATH entry and the resolved target. For `/opt/homebrew/bin/claude-agent-acp` → `/opt/homebrew/lib/node_modules/...`, it is `/opt/homebrew`. |
| `Dir` | the harness's data directory `<home>/harnesses/<id>/data` (0700) |
| `Env` | `HarnessEnv(os.Environ(), data, [PATH entry the file was found in])`: no tokens, keys, `AGENTFLOW_*` or session IDs; `HOME` and `TMPDIR` under the data directory. The found directory comes first in PATH so a `#!/usr/bin/env node` script finds the `node` installed next to it. |
| `Args` | the definition's `args` (initialize) or `version_args` (version) |
| Stderr | at most 64 KiB, appended to `<home>/harnesses/<id>/probe.log` (rotated at 1 MiB, one old file kept). Never in an event, the stream, a result or a terminal. |

**Not the person's HOME.** A probe sees an empty home, so it cannot read the harness's own sign-in. That is enough for `initialize`, which needs no sign-in. It is also why the probe cannot tell whether a harness is signed in; the separate sign-in check does that ("Phase 6 sign-in check"). Phase 7 decides the session environment in its own contract.

**Limits** (`harness.Default*`):

| Limit | Value |
| --- | --- |
| Version command | 3 s, first 4 KiB of stdout; the version is the first `v?X.Y.Z[-pre]` in it |
| `initialize` | `personal:ai.harnesses.probe_timeout` (5 s default, 1 s to 30 s) |
| After the answer | `Stop`: close stdin, 2 s grace, kill the process group |
| Probes at once | 2 per process |
| Same identity probed again | not within 30 s (the cooldown); a request inside it gets the cached result |
| Frame | 1 MiB (`jsonrpc.DefaultMaxFrame`) |
| Requests from the agent during a probe | answered `-32601` (method not found); nothing else is sent to it |

**What is never run:** `npx`, `uvx` or any refused launcher; a download; an install; an adapter that is only `adapter_available` (its base CLI is never started either; a found adapter's base CLI is started only for its overlay or personal status command); a definition that failed validation; a registry `env`; a terminal sign-in; `authenticate`, `session/new` or any method other than `initialize`; a status command from the registry, a plugin or a repository.

### Phase 6 detection states

```go
package harness

type State string

const (
    StateNative           State = "native"            // found; the product's own CLI speaks ACP
    StateAdapterInstalled State = "adapter_installed" // found; an ACP adapter for a CLI
    StateAdapterAvailable State = "adapter_available" // adapter missing; its base CLI found
    StateNeedsAuth        State = "needs_auth"        // found; the harness said it needs sign-in
    StateIncompatible     State = "incompatible"      // found; it does not speak ACP v1, or is too old
    StateNotInstalled     State = "not_installed"     // nothing found; listed only with --all
)
```

`not_installed` is the sixth state. The plan names five; this one is only shown with `--all` (decision 1).

**How the state is chosen** (`harness.Resolve`, first match wins):

| # | Condition | State | Code |
| --- | --- | --- | --- |
| 1 | Nothing found, kind `adapter`, a base command found | `adapter_available` | `base_only` |
| 2 | Nothing found | `not_installed` | `missing` |
| 3 | Found; the last `initialize` for this identity answered a `protocolVersion` other than 1 | `incompatible` | `protocol_version` |
| 4 | Found; answered `initialize` with an error other than `-32000`, or wrote something that is not JSON-RPC | `incompatible` | `protocol` |
| 5 | Found; the version is below `min_version` | `incompatible` | `version_too_old` |
| 6 | Found; the sign-in check answered `signed_out` for this identity; or `initialize` answered `-32000`; or an ACP session recorded `auth_required` after the last good probe (`Store.MarkAuthRequired`, called by phase 7) | `needs_auth` | `signed_out` or `auth_required` |
| 7 | Found, kind `native` | `native` | from the probe (below) |
| 8 | Found, kind `adapter` | `adapter_installed` | from the probe (below) |

**Probe status** (`Harness.Probe`) is separate from the state, so the three cases the state alone would blur stay clear:

| `probe.status` | `probe.code` | Meaning |
| --- | --- | --- |
| `none` | `not_probed` | Found on PATH; nothing has run for this identity yet. The state is from the file only. |
| `ok` | `ok` | `initialize` answered with protocol version 1. |
| `failed` | `timeout`, `exited`, `launch` | The probe ran and gave no answer: it timed out, exited first or could not start. The state stays `native` or `adapter_installed`, with the fix for the code. |
| `failed` | `protocol`, `protocol_version` | See rows 3 and 4: the state is `incompatible`. |
| `refused` | `needs_person`, `shim`, `test_guard` | agentflow did not run it (see "Phase 6 who may probe"). |
| `cached` | as stored | The result is from the cache; `probed_at` says when. |

A version command that times out or prints no version gives `version: ""` and `version_code: "no_version"`. It does not change the state unless `min_version` is set (then row 5 cannot be checked, and the state keeps its row 7 or 8 value with code `version_unknown`).

**Where `needs_auth` comes from.** ACP v1 `initialize` does not say whether the person is signed in: every registry agent lists `authMethods` either way, and `-32000` normally comes back only from later requests such as `session/new`, which phase 6 never sends. So the state comes from the sign-in check (below) for harnesses with a verified status command, and otherwise only from an agent that refuses `initialize` or a phase 7 session that records it. Every other found harness shows sign-in `unknown` and its sign-in methods as guidance.

### Phase 6 sign-in check (lanes 6.1 and 6.2)

*Amendment: the owner chose this in place of the first draft's decision 2.* agentflow also runs each harness's own sign-in status command, with the person's real home folder and credentials, to tell whether the person is signed in. That is the only command phase 6 runs with the person's real home, so every rule here is strict.

**Which harnesses have one.** A status command is in the overlay only when its meaning is verified from the vendor's official documentation or source. A harness without one has sign-in `unknown` (code `no_check`) and shows the sign-in guidance. Verified on 2026-11-27:

| Overlay ID | Runs | Signed in | Signed out | Anything else | Source |
| --- | --- | --- | --- | --- | --- |
| `claude-acp` (adapter) | the base CLI: `claude auth status` | exit 0 | exit 1 | `unknown` | Claude Code CLI reference, <https://code.claude.com/docs/en/cli-reference>: it documents exit code 0 when logged in and 1 when not. The adapter needs a Claude login (`agentclientprotocol/claude-agent-acp` README at `9918571`). |
| `codex-acp` (adapter) | the base CLI: `codex login status` | exit 0 | exit 1 **and** a line that is exactly `Not logged in` on stdout or stderr | `unknown` (exit 1 is also used for errors) | `openai/codex` at `bee5dc55ac118d9136171fed55ffd89af26f8d21`, `codex-rs/cli/src/login.rs` (`run_login_status`) and `codex-rs/cli/src/main.rs` (`LoginSubcommand::Status`). The adapter uses Codex's own sign-in (`agentclientprotocol/codex-acp` README at `08746f8`). |

No other pinned harness has a verified status command:

- `cursor`: `agent status` is documented (<https://cursor.com/docs/cli/reference/authentication>), but neither its exit codes nor its output layout are. Without a documented mapping it stays `unknown`. It can be added when Cursor documents one.
- `github-copilot-cli`: the CLI reference documents no non-interactive status command (the `/user` command works only inside a session).
- `gemini`: no status subcommand in `google-gemini/gemini-cli` at `60f8082`.
- `opencode`: `opencode auth list` is documented, but its output format, exit codes and whether it prints secrets are not, so it is not used.
- `goose` and the rest: not checked; they stay `unknown` until someone verifies a command from the vendor's documentation or source.

**Where the command may come from.** Only from the agentflow overlay (reviewed agentflow code, each entry citing its source) or the person's own override. Never from the registry, a plugin definition or a repository. The check runs with the person's credentials: any process started there can read the person's tokens, and a chosen argument can turn a status call into something else (`claude -p "<prompt>"` would start a paid session with full access). Only agentflow's maintainers and the person may make that choice. `harnessdef.Validate` refuses every `status_*` field in a plugin definition (`status_not_allowed`). The registry has no such field, and a repository has no harness layer.

**What it runs.** `status_from` is `self` (the harness's own found command) or `base` (the adapter's found base CLI). It is never a free command name, so the status command is always a file the finder already found and checked. For an adapter, the base CLI is started only for this check, and only when the adapter itself is found (`adapter_installed`, or `needs_auth` from an earlier check). An `adapter_available` adapter's base CLI is never started. Arguments are `status_args`, checked by the arg rule.

**The status profile** (a separate profile from the probe's):

| Field | Value |
| --- | --- |
| Path, Root | as the probe: the found file and the deepest common directory |
| Dir, DataDir | `Dir` is `<home>/harnesses/<id>/status`, an empty directory (0700), and `DataDir` is `<home>/harnesses/<id>`, so `Dir` is checked against it and never against the parent of the real `HOME`. Never a repository, so no project settings, hooks or project config load. |
| Env | from the parent, only: `HOME`, `USER`, `LOGNAME`, `LANG`, `LC_*`, `TZ`, `TMPDIR`; `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME` when set; on Windows `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `HOMEDRIVE`, `HOMEPATH` and the phase 5 Windows names; and the definition's `config_env` names when set (directory variables such as `CODEX_HOME`). `PATH` is the found directory plus the fixed system PATH. Nothing else: no credential variable, token, key, proxy setting, `SSH_AUTH_SOCK`, `AGENTFLOW_*` or session ID. |
| Timeout | 5 s (`DefaultStatusTimeout`), then the process group is killed (`jsonrpc.Run`) |
| Output | stdout and stderr, at most 4 KiB each, read into memory only to match `status_signed_out_line`, then dropped. Never written to the cache, a log (not even `probe.log`), an event, a result or the page. |
| Runs | at most one at a time per process; the same identity at most once per 5 minutes unless a person asks |

`config_env` names for the two entries: `CODEX_HOME` (in the `openai/codex` source above). `CLAUDE_CONFIG_DIR` is added only if lane 6.1 finds it verbatim in the official Claude Code environment variable reference (<https://code.claude.com/docs/en/env-vars>); otherwise a person with a moved Claude configuration gets `unknown` or a wrong `signed_out`, and the fix text says how to check.

`jsonrpc.Run` checks every variable name against `EnvAllowed`. Lane 6.1 adds `LaunchSpec.AllowEnv []string` (additive): names the call also allows. Only the status profile sets it.

**Directory variables a repository could have set.** With the real home, a CLI reads its own configuration, which can name commands it runs. So before the check starts, each of `HOME`, `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, the `XDG_*_HOME` variables and the `config_env` names that is set is resolved. If `config.InsideRepository` reports one as inside any git working tree or git directory (links and `..` resolved; a directory that cannot be checked counts as inside), the check does not run: sign-in `unknown`, code `unsafe_environment`. The only exception is a repository whose root is the OS account's home (dotfiles). This is the same rule as for the personal layer (#105), applied to the harness's configuration.

**People who sign in with an environment variable.** The check never passes credential variables, so a CLI that would use one says "signed out". So agentflow never reports `signed_out` when the parent environment has a variable that could sign the harness in:

- the definition's `credential_env` names, verbatim from the vendor's source or documentation. Verified: for `codex-acp`, `OPENAI_API_KEY`, `CODEX_API_KEY` and `CODEX_ACCESS_TOKEN` (`codex-rs/login/src/auth/manager.rs` at the commit above). For `claude-acp`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_FOUNDRY_API_KEY`, `AWS_BEARER_TOKEN_BEDROCK` and `ANTHROPIC_AWS_API_KEY` (Claude Code environment variable reference). Lane 6.1 adds `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CODE_USE_BEDROCK` and `CLAUDE_CODE_USE_VERTEX` only after it finds them verbatim on that page.
- and, for every harness, any variable whose name ends in `_API_KEY`, `_AUTH_TOKEN`, `_ACCESS_TOKEN`, `_OAUTH_TOKEN` or `_REFRESH_TOKEN` (the last added by the security review amendments).

Only the name's presence (non-empty) is checked; the value is never read. With one present, a `signed_out` answer becomes `unknown` with code `env_credential`, and the page says "You may be signed in with an environment variable; agentflow cannot check that." This errs towards `unknown`, never towards a false `signed_out`. The daemon checks its own environment, which may differ from the shell where the person runs the harness; the same note applies.

**Verdicts** (`harness.SignInStatus`, stored with the time only):

| `sign_in.status` | `sign_in.code` | State effect |
| --- | --- | --- |
| `signed_in` | `ok` | none |
| `signed_out` | `signed_out` | `needs_auth` (row 6 of the state table) |
| `unknown` | `no_check`, `env_credential`, `timeout`, `exit_code` (an unmapped exit), `launch`, `off` (`sign_in_check` is off), `needs_person`, `base_missing`, `unsafe_environment`, `test_guard` | none; sign-in guidance shown |

**When it runs:** in the same pass as a probe, after `initialize`, for a found harness with a status command: when a person probes; when an agent or the daemon probes and both `auto_probe` and `sign_in_check` are on; and again when the identity of the harness or its base CLI changes, or after 24 hours (the cached verdict expires). It never runs while `sign_in_check` is off, whoever asks.

**The fake CLI.** `harnesstest.StatusCLI(tb, dir, name string, exit int, line string)` (lane 6.1) writes an executable that records its working directory and environment names (never values) to a file under `tb.TempDir()`, prints `line`, and exits with `exit`. Tests show: the working directory is the status directory; no credential variable reaches it; `HOME` is the parent's; a `signed_out` with `ANTHROPIC_API_KEY` set in the parent becomes `unknown`/`env_credential`; output never reaches the cache, the log, an event or a result; a hanging fake is killed at 5 s. The test guard covers it like every other start: in a test binary a status command outside the temporary directory fails with `ErrTestGuard`.


### Phase 6 fix text

Fix text is **agentflow's own template**, chosen by state and code, in `internal/harness/fix.go`. Values are filled in only from validated fields: a command name, a package name, an https URL, a SHA-256, a version, a configuration key. Registry descriptions and agent text never become fix text. Commands are shell-quoted for POSIX shells (PowerShell on Windows) and are shown, never run.

```go
// Fix is what the person can do about a state. Command is one line to
// copy; URL is https or empty.
type Fix struct {
    Text    string `json:"text"`
    Command string `json:"command,omitempty"`
    URL     string `json:"url,omitempty"`
}
```

| State, code | `text` | `command` | `url` |
| --- | --- | --- | --- |
| `native`/`adapter_installed`, probe `ok` | none (`fix` is omitted) | | |
| any found state, probe `none`, `not_probed` | "Not checked yet. Probe it to confirm it speaks ACP." | `agentflow ai harnesses --probe` | |
| probe `failed`, `timeout` | "`<command>` did not answer within <timeout>. Probe again, or allow more time." | `agentflow config set ai.harnesses.probe_timeout=15s` | |
| probe `failed`, `exited` | "`<command> <args>` exited before it answered. Check that this `<command>` is <name> and supports ACP. If it needs other arguments, set them in Harness overrides on the Settings page." | | registry `repository` |
| probe `failed`, `launch` | "agentflow could not start `<path>`. Check that the file is executable." | | |
| probe `refused`, `needs_person` | "Automatic probes are off. Probe it yourself, or turn them on." | `agentflow ai harnesses --probe` | |
| probe `refused`, `shim` | "`<file>` is a Windows command script, which agentflow does not start. Point the override at the program's `.exe`." | | |
| `adapter_available`, `base_only`, registry `npx` | "<base name> is installed, but its ACP adapter is not. Install the adapter yourself; agentflow never installs it." | `npm install -g <package@version>` | registry `repository` |
| same, `uvx` | same text | `uv tool install <package==version>` | registry `repository` |
| same, `binary` for this platform | "… Download it, check its SHA-256, and put `<cmd base name>` on your PATH." | none (shown: archive URL, `sha256`) | the archive URL |
| same, no distribution (an override) | "<base name> is installed, but `<command>` is not on your PATH." | | |
| `not_installed`, `missing` | as `adapter_available`, with the harness's name | as above | registry `repository` or `website` |
| `incompatible`, `protocol_version` | "<name> speaks ACP version <n>; agentflow needs version 1. Update <name>." | the install command for the registry version, when there is one | registry `repository` |
| `incompatible`, `protocol` | "`<command> <args>` did not answer like an ACP agent. Check the arguments in Harness overrides on the Settings page." | | registry `repository` |
| `incompatible`, `version_too_old` | "<name> <version> is older than <min_version>. Update it." | the install command, when there is one | registry `repository` |
| `needs_auth`, `signed_out` or `auth_required` | "Sign in to <name>, then probe again." Followed by `sign_in` (below). | for `claude-acp` `claude auth login`, for `codex-acp` `codex login` (from the same sources) | |
| sign-in `unknown`, `env_credential` | "You may be signed in with an environment variable; agentflow cannot check that." | | |
| sign-in `unknown`, `no_check` | none extra: the sign-in guidance below | | |

**Sign-in guidance** (`Harness.SignIn`, from the last good `initialize`, shown for every found harness and first for `needs_auth`):

- A `terminal` method → `{method, name, command}`, where `command` is the found command name (not its path), the definition's `args`, then the method's `args`, with the method's `env` as a prefix (`NAME=value`). The person runs it in their own terminal. The command is omitted (`unsafe: true`) when an arg fails the arg rule, there are more than 8 args, an env name is not `^[A-Z_][A-Z0-9_]*$`, an env value does not match the arg rule (the value comes from the agent and is pasted into a shell) or is over 128 bytes, or an env name changes program lookup, loading or trust: `PATH`, `LD_*`, `DYLD_*`, `NODE_OPTIONS`, `NODE_PATH`, `PYTHONPATH`, `PYTHONSTARTUP`, `BASH_ENV`, `ENV`, `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY` (any case), `SSL_CERT_FILE`, `SSL_CERT_DIR`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `GIT_SSH_COMMAND`, `GIT_SSH`.
- An `agent` method → `{method, name, description}` and the text "Start <name> in your terminal and sign in with its own login, then probe again." agentflow does not call `authenticate`.
- Agent-supplied text is sanitized (Content Filter): one line, control and bidirectional characters removed; `id` and `name` at most 64 bytes, `description` 256, at most 8 methods.

### Phase 6 `Harness` type (lane 6.1)

```go
package harness

// Harness is one detected (or, with All, known) harness.
type Harness struct {
    ID          string            `json:"id"`
    Name        string            `json:"name"`
    Description string            `json:"description,omitempty"`
    Kind        harnessdef.Kind   `json:"kind"`
    State       State             `json:"state"`
    Code        string            `json:"code,omitempty"`
    // Sources lists the layers that shaped the definition, lowest first:
    // "registry", "agentflow", "plugin:<id>", "personal".
    Sources     []string          `json:"sources"`
    Quarantined bool              `json:"quarantined,omitempty"`
    // QuarantineReason is the registry's reason, clamped and sanitized like
    // a description.
    QuarantineReason string        `json:"quarantine_reason,omitempty"`
    // Command, Args: what is (or would be) started. Path: where Command was
    // found; a path under the OS home is shown as "~/...". Never in events.
    Command     string            `json:"command,omitempty"`
    Args        []string          `json:"args,omitempty"`
    // VersionArgs: the version command's arguments, which a probe runs
    // first (["--version"] when the definition sets none). NoVersion: it
    // runs none. Set only with Command. Never in events. (Added by
    // "Phase 6 security review amendments", item 6.)
    VersionArgs []string          `json:"version_args,omitempty"`
    NoVersion   bool              `json:"no_version,omitempty"`
    Path        string            `json:"path,omitempty"`
    // Base is the base CLI's command and where it was found, for adapters.
    Base        *Base             `json:"base,omitempty"`
    // Version: from the version command. RegistryVersion: the pinned copy's.
    Version         string        `json:"version,omitempty"`
    VersionCode     string        `json:"version_code,omitempty"` // "", "no_version"
    RegistryVersion string        `json:"registry_version,omitempty"`
    Probe       Probe             `json:"probe"`
    // ACP is the sanitized initialize answer for the current identity.
    ACP         *ACPInfo          `json:"acp,omitempty"`
    SignIn      []SignIn          `json:"sign_in,omitempty"`
    // Auth is the sign-in check's verdict ("Phase 6 sign-in check").
    Auth        SignInStatus      `json:"auth"`
    Fix         *Fix              `json:"fix,omitempty"`
    Notes       []string          `json:"notes,omitempty"` // codes: registry_env_ignored, quarantined
}

// SignInStatus is the sign-in check's verdict. It holds no output.
type SignInStatus struct {
    Status    string     `json:"status"` // signed_in, signed_out, unknown
    Code      string     `json:"code"`
    CheckedAt *time.Time `json:"checked_at,omitempty"`
}

type Base struct {
    Name    string `json:"name"`
    Command string `json:"command"`
    Path    string `json:"path,omitempty"`
}

type Probe struct {
    Status   string     `json:"status"` // none, ok, failed, refused, cached
    Code     string     `json:"code"`
    At       *time.Time `json:"probed_at,omitempty"`
    Duration int64      `json:"duration_ms,omitempty"`
}

type ACPInfo struct {
    ProtocolVersion int    `json:"protocol_version"`
    AgentName       string `json:"agent_name,omitempty"`
    AgentTitle      string `json:"agent_title,omitempty"`
    AgentVersion    string `json:"agent_version,omitempty"`
    LoadSession     bool   `json:"load_session"`
    Image           bool   `json:"image"`
    Audio           bool   `json:"audio"`
    EmbeddedContext bool   `json:"embedded_context"`
    MCPHTTP         bool   `json:"mcp_http"`
    MCPSSE          bool   `json:"mcp_sse"`
    Logout          bool   `json:"logout"`
}

type SignIn struct {
    Method      string `json:"method"`
    Type        string `json:"type"` // agent, terminal
    Name        string `json:"name"`
    Description string `json:"description,omitempty"`
    Command     string `json:"command,omitempty"`
    Unsafe      bool   `json:"unsafe,omitempty"`
}

// Report is one detection pass.
type Report struct {
    Registry    RegistryInfo  `json:"registry"` // commit, committed_at, agents
    AutoProbe   bool          `json:"auto_probe"`
    Harnesses   []Harness     `json:"harnesses"` // sorted by state group (ready, attention, not installed), then ID
    SkippedPath []SkippedDir  `json:"skipped_path,omitempty"`
    Problems    []Problem     `json:"problems,omitempty"`
    // ProbeRefused is set when a probe was asked for and not allowed.
    ProbeRefused string       `json:"probe_refused,omitempty"` // needs_person
    CheckedAt   time.Time     `json:"checked_at"`
}

type RegistryInfo struct {
    Commit      string    `json:"commit"`
    CommittedAt time.Time `json:"committed_at"`
    Agents      int       `json:"agents"`
}

type SkippedDir struct {
    Dir    string `json:"dir"` // shown with "~"
    Reason string `json:"reason"`
}

type Problem struct {
    Layer   string `json:"layer"` // registry, agentflow, plugin:<id>, personal
    ID      string `json:"id"`
    Code    string `json:"code"`
    Message string `json:"message"`
}

// Found is what the finder saw for one definition.
type Found struct {
    ID         string
    Definition harnessdef.Definition
    Sources    []string
    Path       string // absolute, as found on PATH
    Target     string // resolved
    PathDir    string // the PATH entry
    Identity   string // "sha256:<hex>" of the identity fields
    // BaseFound is the adapter's base CLI, found by the same rules: Path
    // absolute, PathDir, Target, Identity = IdentityOf(Path, StatusArgs,
    // nil). Its Definition holds only Name (base name) and Command (the
    // base command that matched). nil when no base command was found.
    BaseFound  *Found
}

// ProbeRecord is the stored result for one identity.
type ProbeRecord struct {
    ID             string             `json:"id"`
    Identity       string             `json:"identity"`
    Version        string             `json:"version,omitempty"`
    VersionCode    string             `json:"version_code,omitempty"`
    Status         string             `json:"status"`
    Code           string             `json:"code"`
    ACP            *ACPInfo           `json:"acp,omitempty"`
    SignIn         []SignIn           `json:"sign_in,omitempty"`
    ProbedAt       time.Time          `json:"probed_at"`
    DurationMS     int64              `json:"duration_ms"`
    AuthRequiredAt *time.Time         `json:"auth_required_at,omitempty"`
    // Auth is the last sign-in verdict for this identity, with its time;
    // never any output. BaseIdentity is BaseFound.Identity when that
    // verdict was taken ("" without a base); a change makes it due again.
    Auth           *SignInStatus      `json:"auth,omitempty"`
    BaseIdentity   string             `json:"base_identity,omitempty"`
}

// Store is the probe cache (lane 6.2: cache.go).
type Store interface {
    Get(id, identity string) (ProbeRecord, bool, error)
    Put(r ProbeRecord) error
    MarkAuthRequired(id, identity string, at time.Time) error
    // Published is the last state the daemon published per ID.
    Published() (map[string]PublishedState, error)
    SetPublished(map[string]PublishedState) error
}

type PublishedState struct {
    State State  `json:"state"`
    Code  string `json:"code,omitempty"`
    Version string `json:"version,omitempty"`
    // ProbeStatus is Harness.Probe.Status, so a probe-status change
    // survives a daemon restart.
    ProbeStatus string `json:"probe_status,omitempty"`
}

// Prober runs one initialize probe (lane 6.2: probe.go). The version
// command is the Detector's own (lane 6.1: version.go).
type Prober interface {
    Probe(ctx context.Context, f Found, timeout time.Duration) ProbeRecord
}

// SignInChecker runs one sign-in check (lane 6.2: signin.go,
// *SignInCheck). The Detector calls it in the probe pass after initialize
// and stores the verdict in ProbeRecord.Auth.
type SignInChecker interface {
    Check(ctx context.Context, f Found, r SignInRequest) SignInStatus
}

// SignInRequest says who asks for a check and under which settings.
type SignInRequest struct {
    Requester string        // person, agent or daemon
    AutoProbe bool          // effective ai.harnesses.auto_probe
    Enabled   bool          // effective ai.harnesses.sign_in_check
    Previous  *SignInStatus // the stored verdict for this identity, if any
}

// DetectOptions select a pass.
type DetectOptions struct {
    All   bool     // include not_installed
    IDs   []string // only these
    Probe bool     // run probes now (subject to "who may probe")
    // Requester is "person" or "agent" (see "who may probe"); the daemon
    // uses "daemon".
    Requester string
}

// Detector is the one entry point CLI, MCP and daemon use (lane 6.1:
// detect.go).
type Detector interface {
    Detect(ctx context.Context, o DetectOptions) (Report, error)
}
```

Lane 6.1 implements `Detector` over the registry, the finder, its own version command, a `Prober`, a `SignInChecker` and a `Store`. In a probe pass the Detector runs the version command, then `Prober.Probe`, then (when allowed) `SignInChecker.Check`, and then `Store.Put`s the one record; the `Prober` and the checker never write the store. A `-32000` answer to initialize is recorded by the `Prober` as probe status `failed`, code `auth_required`, with `AuthRequiredAt` set, and the state table gives `needs_auth`. Lane 6.2 implements `Prober`, `SignInChecker` and `Store` and wires them in through `harness.Options`. Production wires them through `harness.Production(home, version, workDirs)`, which builds the `FileStore`, `ACPProber` and `*SignInCheck` over the one agentflow home the pass reads its settings from (see "Phase 6 integration amendments").

**The cache** (`<home>/harnesses/cache.json`, lane 6.2): mode 0600 in a 0700 directory, replaced atomically under `cache.json.lock` (`internal/filelock`), read-modify-write per entry so the CLI and the daemon can both write. At most 256 records and 256 KiB; the oldest records go first. A record applies only to its identity, and for at most 24 hours. It holds no path, no stderr and no agent text beyond the sanitized `ACPInfo` and `SignIn`.

### Phase 6 who may probe

A probe runs the harness, and the sign-in check runs a CLI with the person's credentials. Who may ask for them:

| Requester | `--probe` / `probe: true` | Automatic |
| --- | --- | --- |
| A person: the CLI in a terminal with no agent environment (`app.AgentEnvironment() == ""` and a terminal on stdin), or the console's Probe button | yes; the sign-in check too unless `sign_in_check` is off | |
| An agent: any MCP call, or the CLI inside an agent session | when `personal:ai.harnesses.auto_probe` is on (the default); the sign-in check only when `sign_in_check` is also on. Otherwise `probe_refused: needs_person` and cached results | |
| The daemon | | when `auto_probe` is on, only for a harness whose identity changed; the sign-in check when `sign_in_check` is also on |

Both toggles are **on by default** (decisions 3 and 11) and can be turned off on the Settings page. With `auto_probe` off, agentflow only looks at files until a person asks for a probe. With `sign_in_check` off, agentflow never runs a status command, whoever asks. Turning either on again is a loosening edit. Because both are on by default, no test or `make e2e` run may rely on the defaults: see "Phase 6 test guard".

### Phase 6 configuration keys (lane 6.1)

Lane 6.1 adds `config.User.AI` (`internal/config/ai.go`), an optional key, so it is backwards compatible:

```go
type UserAI struct {
    Harnesses UserHarnesses `yaml:"harnesses,omitempty"`
}

type UserHarnesses struct {
    AutoProbe    *bool                            `yaml:"auto_probe,omitempty"`    // nil means on
    SignInCheck  *bool                            `yaml:"sign_in_check,omitempty"` // nil means on
    PollInterval *time.Duration                   `yaml:"poll_interval,omitempty"`
    ProbeTimeout *time.Duration                   `yaml:"probe_timeout,omitempty"`
    Disabled     []string                         `yaml:"disabled,omitempty"`
    Overrides    map[string]harnessdef.Definition `yaml:"overrides,omitempty"`
}
```

Example:

```yaml
ai:
  harnesses:
    auto_probe: true
    sign_in_check: true
    poll_interval: 30s
    probe_timeout: 5s
    disabled: [kimi]
    overrides:
      gemini:
        args: [--acp]
      my-agent:
        name: My agent
        command: my-agent
        args: [acp]
```

`config.Field` metadata, in `internal/config/field_meta_table.go`, group `ai` ("AI harnesses", order 850, between Automatic setup and Plugins):

| ID | Widget | Default | Limits | Label | Description | Applies | Loosens |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `personal:ai.harnesses.auto_probe` | `toggle` | true | | Probe harnesses automatically | Lets agentflow start an installed harness for a few seconds to read its version and check that it speaks ACP, when the harness appears or changes, and lets agents ask for a probe. When off, agentflow only looks at files on your PATH until you ask for a probe. | `live` | `enable` |
| `personal:ai.harnesses.sign_in_check` | `toggle` | true | | Check sign-in | Runs a harness's own sign-in status command, such as `claude auth status`, with your real home folder, to show whether you are signed in. agentflow keeps only the answer (signed in, signed out or unknown), never the command's output. When off, agentflow never runs these commands. | `live` | `enable` |
| `personal:ai.harnesses.poll_interval` | `duration` | 30s | 10s to 10m | How often to look for harnesses | How often the console daemon checks your PATH for harnesses that were installed, changed or removed. | `live` | |
| `personal:ai.harnesses.probe_timeout` | `duration` | 5s | 1s to 30s | Probe time limit | How long a harness has to answer the ACP initialize request before the probe gives up. | `next-run` | |
| `personal:ai.harnesses.disabled` | `chips` | [] | 64 items | Harnesses to ignore | Harness IDs agentflow never lists or starts. | `live` | `shrink` |

| `personal:ai.harnesses.overrides` | `kv-table`, key = harness ID, `columns` below | {} | 64 entries | Harness overrides | Changes how agentflow finds or starts a harness, or adds one that is not in the ACP registry. | `live` | |

**The loosening check is code, not metadata.** `settings.loosening` reads specific keys (`policyOf` in `internal/settings/checks.go`); `FieldMeta.Loosens` only says that a field is checked. Lane 6.1 adds the harness cases there. Both toggles read **nil as on**, so the cases compare effective values: `auto_probe` from false to true or unset gives "Lets agentflow start installed harnesses without asking, and lets agents ask for probes."; `sign_in_check` from false to true or unset gives "Lets agentflow run harness sign-in status commands with your real home folder."; and an ID removed from `disabled` gives "Stops ignoring the harness <id>, so agentflow may list and start it." Turning a toggle off, or removing the key while it is already unset, is not loosening.

`overrides` columns (one `FieldMeta` each; a personal override of a status command uses the default exit codes 0 and 1 and has no signed-out line): `status_from` (`select` empty/`self`/`base`, "Sign-in check runs"), `status_args` (`chips`, "Sign-in check arguments"), `credential_env` (`chips`, "Sign-in variables", "Environment variables that sign this harness in; agentflow checks only that one is set."), `name` (`text`, "Name"), `kind` (`select` `native`/`adapter`, "Kind"), `command` (`text`, "Command", "A command name on your PATH, or an absolute path."), `args` (`chips`, "ACP arguments"), `version_args` (`chips`, "Version arguments"), `no_version` (`toggle`, "Never ask for a version"), `base_name` (`text`, "Base CLI name"), `base_commands` (`chips`, "Base CLI commands"), `min_version` (`text`, "Oldest version").

- `command` is `text`, not `file-picker`: the picker's `base` is only `repo` or `home`, and a harness may be in `/opt/homebrew/bin`. The save's check chain runs `harnessdef.Validate` on every override (a `harness` diagnostic), so a refused launcher or an unsafe arg is an error in the Save review.
- An override is not marked loosening. The person saves it through the always-confirmed Save review. An agent changes it only through `config.set`, which always needs a person's confirmation (phase 4 decision 7).
- No harness key exists in the repository layer. A repository `config.yml` with an `ai` section is a `syntax` problem, as for any unknown key.
- `TestEveryConfigFieldHasControl` covers the six fields and the twelve columns. If the Settings page cannot draw a `kv-table` column of type `chips`, `select` or `toggle`, lane 6.1 stops and reports: it must not change the Settings UI.

### Phase 6 events (lane 6.2)

| Type | Scope | Global | Transient | Payload | Produced when |
| --- | --- | --- | --- | --- | --- |
| `harness.detected` | repository-wide | only | no | `HarnessChanged` | a harness that the last published snapshot did not have (or had as `not_installed`) is now in any other state |
| `harness.changed` | repository-wide | only | no | `HarnessChanged` | a known harness's state, code, version or probe status changed |
| `harness.removed` | repository-wide | only | no | `HarnessRemoved` | a harness in the last snapshot is now `not_installed` or gone from the definitions |

```go
// internal/bus/types_phase6.go
const (
    TypeHarnessDetected Type = "harness.detected"
    TypeHarnessChanged  Type = "harness.changed"
    TypeHarnessRemoved  Type = "harness.removed"
)

var phase6Types = []TypeInfo{
    {Type: TypeHarnessDetected, Scope: ScopeRepo, Phase: 6, Global: GlobalOnly},
    {Type: TypeHarnessChanged, Scope: ScopeRepo, Phase: 6, Global: GlobalOnly},
    {Type: TypeHarnessRemoved, Scope: ScopeRepo, Phase: 6, Global: GlobalOnly},
}

// HarnessChanged is the payload of harness.detected and harness.changed.
type HarnessChanged struct {
    Harness     string `json:"harness"` // definition ID
    Kind        string `json:"kind"`    // native, adapter
    State       string `json:"state"`   // a harness.State value
    Previous    string `json:"previous,omitempty"`
    Code        string `json:"code,omitempty"`
    ProbeStatus string `json:"probe_status"`
    // SignIn is the sign-in verdict: signed_in, signed_out, unknown.
    SignIn      string `json:"sign_in,omitempty"`
    Version     string `json:"version,omitempty"` // clamped
    // Source is the highest layer: registry, agentflow, plugin:<id>, personal.
    Source  string `json:"source"`
    // Trigger: start, poll, focus, probe, config.
    Trigger string `json:"trigger"`
}

// HarnessRemoved is the payload of harness.removed.
type HarnessRemoved struct {
    Harness  string `json:"harness"`
    Previous string `json:"previous"`
    Trigger  string `json:"trigger"`
}
```

- Only the daemon's harness source produces them, on its own bus, in `global`. None is a socket type (`notifysock.Allowed` is unchanged). The CLI's probes write the cache, and the daemon publishes what changed on its next pass.
- Source `harness.detector`. Payloads are codes and clamped versions: no path, no args, no agent text, no stderr.
- No `global/*/harness.**` stream topic: the `harnesses` projection follows them through `globalTopics` and sends its state in each repository's frames (phase 5 decision 10).
- Not audited by the Wire Tap. No `TestEveryWriteOperationEmitsEvents` change: they are not operation events.
- The last published snapshot is kept in the cache (`Store.Published`), so a daemon restart publishes only real changes. With no snapshot (first start), every harness in a found state gets `harness.detected`.

### Phase 6 daemon source (lane 6.2)

`internal/watch/harness.go`: a Polling Consumer started by the daemon from a `runtimeExtension` in `internal/webui/stream_runtime_harnesses.go` (phase 5 precedent; `stream_runtime.go` is not edited).

- **A pass** is `Detector.Detect(ctx, {Requester: "daemon", Probe: auto_probe})`: PATH lookups and stats, the cache, and probes for changed identities only when `auto_probe` is on, and the sign-in check when `sign_in_check` is also on. Then compare with the published snapshot, publish `harness.*`, and store the snapshot.
- **When:** at start (`start`); every `poll_interval` (`poll`); on a focus check from the console (`focus`); on a personal `config.layer.changed`, which covers overrides and the plugin registry (`config`); after a console probe (`probe`). Passes never overlap. A request during a pass runs one more pass after it. Triggers within 2 s of a pass are joined.
- The pass also re-reads `cache.json` (a stat; a new modification time means a CLI probe wrote it), so a terminal `ai harnesses --probe` reaches the page within one poll.

### Phase 6 harnesses projection (lane 6.2)

| Projection | Key | Topics | Reads | State |
| --- | --- | --- | --- | --- |
| `harnesses` | repository | `global/*/harness.detected`, `global/*/harness.changed`, `global/*/harness.removed`, `global/*/config.layer.changed` | the daemon's last `Report` (`currentHarnesses()` in `stream_runtime_harnesses.go`) | `HarnessesState` |

`internal/webui/projection_harnesses_state.go`:

```go
const ProjectionHarnesses = "harnesses"

// HarnessesState is the "harnesses" state. It is the daemon's last
// harness.Report, so it is the same in every repository's frames.
type HarnessesState struct {
    // Detector is false when the daemon runs no harness source.
    Detector     bool                `json:"detector"`
    Registry     harness.RegistryInfo `json:"registry"`
    AutoProbe    bool                `json:"auto_probe"`
    Harnesses    []harness.Harness   `json:"harnesses"` // found states only; not_installed is left out
    SkippedPath  []harness.SkippedDir `json:"skipped_path,omitempty"`
    Problems     []harness.Problem   `json:"problems,omitempty"`
    CheckedAt    time.Time           `json:"checked_at"`
    // Probing lists IDs being probed right now.
    Probing      []string            `json:"probing,omitempty"`
}
```

One delta frame:

```text
event: delta
id: v1:3f2a9c1b7d4e5f60=57
data: {"event":{"type":"harness.changed","repo":"global",...},"states":[{"projection":"harnesses","repo":"3f2a9c1b7d4e5f60","worktree":"","state":{"detector":true,"registry":{"commit":"4273056f03b93f8ce41e9276256ea47b057b794c","committed_at":"2026-11-26T23:54:34Z","agents":46},"auto_probe":false,"harnesses":[{"id":"gemini","name":"Gemini CLI","kind":"native","state":"native","code":"","sources":["registry","agentflow"],"command":"gemini","args":["--acp"],"path":"~/.local/bin/gemini","version":"0.71.1","registry_version":"0.71.1","probe":{"status":"ok","code":"ok","probed_at":"2026-11-27T09:00:00Z","duration_ms":812},"acp":{"protocol_version":1,"agent_name":"gemini-cli","load_session":true,"image":true,"audio":false,"embedded_context":true,"mcp_http":true,"mcp_sse":false,"logout":false},"sign_in":[{"method":"oauth-personal","type":"agent","name":"Log in with Google"}],"auth":{"status":"unknown","code":"no_check"}}],"checked_at":"2026-11-27T09:00:01Z"}}]}
```

(The values are an example of the shape, not real output.)

- Registered through `projectionExtensions` from `projection_harnesses.go` (phase 3 decision 12). `globalTopics` gains the three `harness.*` topics from `stream_runtime_harnesses.go`'s `init`.
- Paths under the OS home are shown as `~/...`, here and in every result.
- **Requests from the page** (in `internal/webui/harnesses.go`, under the console's session, origin and request-header checks; route lines in `server.go`):

| Route | Body | Does | Answer |
| --- | --- | --- | --- |
| `POST /api/harnesses/check` | none | wakes the daemon source (`focus`). Starts a harness only under the daemon's own rules (a changed identity, `auto_probe` on), so the page may send it on every `visibilitychange` to visible and on window focus. At most one pass every 10 s per console; more answer 202 and do nothing. | `202 {}` |
| `POST /api/harnesses/probe` | `{"ids": ["gemini"]}`, or `{}` for all found | invokes the `ai.harnesses` Spec through the daemon's Invoker with `probe: true` and client `repository-console` (a person). At most one a minute per console (429 beyond). | `202 {}`; results arrive over the stream |

  The page draws only what the stream sends. Neither answer carries harness data.

### Phase 6 commands (lane 6.3)

Lane 6.3 adds these Specs in `internal/command/ops/ai_harnesses.go`, calling `internal/app/harnesses.go`, which calls `harness.Detector`. They are compatibility contracts from the release that ships them.

| Spec | CLI | MCP tool | Class | Gate | Events | Result |
| --- | --- | --- | --- | --- | --- | --- |
| `ai.harnesses` | `ai harnesses [--probe] [--all] [--id ID]... [--json]` | `agentflow_ai_harnesses` (read-only) | `read` | none | none | `harness.Report` |
| `ai.doctor` | `ai doctor [--probe] [--json]` | `agentflow_ai_doctor` (read-only) | `read` | none | none | `DoctorReport` |

- **Tool inputs.** `agentflow_ai_harnesses`: `{probe?: bool, all?: bool, ids?: [string]}`. `agentflow_ai_doctor`: `{probe?: bool}`. No `confirm_token`: neither writes a repository or provider.
- **Requester.** The calling surface sets it, never a client name string: the MCP adapter always passes `agent`; the CLI Verb passes `person` only with a terminal on stdin and `app.AgentEnvironment() == ""`, otherwise `agent`; the console's probe handler passes `person`; the daemon source passes `daemon`. The input has no requester field, so a caller cannot choose one. See "Phase 6 who may probe".
- **Why `read`.** They change only the user's cache under the agentflow home, and a probe runs only what the rules above allow. No approval, no audit line (phase 3: reads are not audited). Decision 4.
- **`ai harnesses` text output:** one line per harness, `ID  STATE  VERSION  COMMAND`, then the fix text and command indented under any harness that has one, then skipped PATH entries and problems. With nothing found: "No ACP harnesses found on your PATH." and the `ai doctor` hint.
- **`DoctorReport`** is `{checks: [{id, status, message, fix?}], report: harness.Report}`, `status` one of `ok`, `warn`, `fail`. Checks, in order:

| `id` | `fail` / `warn` when | Fix |
| --- | --- | --- |
| `registry` | warn: the pinned copy is older than 120 days | "Update agentflow; the registry copy is refreshed with releases." |
| `home` | fail: `home_unsafe` | "agentflow ignores personal overrides and plugin harnesses because the agentflow home is not safe. Use a home outside any repository, owned by you and not writable by others." |
| `path` | warn: entries were skipped | per reason, for example "`<dir>` is inside a git working tree, so agentflow ignores it. Name the harness by absolute path in your overrides if you trust it." |
| `definitions` | fail: a personal or plugin definition was refused | the problem's message and the key to edit |
| `cache` | warn: the cache cannot be read or has the wrong mode | "agentflow rebuilds it on the next probe." |
| `daemon` | warn: no daemon runs | "Run `agentflow ui` for live detection." |
| `auto_probe` | warn: off and some harness has probe `none` | "`agentflow ai harnesses --probe`, or turn on `ai.harnesses.auto_probe`." |
| `sign_in` | warn: a harness is `needs_auth`; or `sign_in_check` is off and a harness has a status command | the harness's sign-in fix; or "Sign-in checks are off, so agentflow cannot tell whether you are signed in." |
| `harnesses` | warn: no harness is `native` or `adapter_installed` with probe `ok` | the fix of the first harness that needs one |

- `ai doctor` exits 1 when a check fails, so scripts can use it.
- The `ai` namespace is new. Phase 7 adds `ai run`, `ai status`, `ai respond` and `ai cancel`. Phase 10's `harness add` keeps its name: phase 6 adds no `harness` command.
- The plugin skill is `adapters/claude-code/skills/ai/SKILL.md` (for the `ai` command; phase 7 extends it). The `agentflow` skill lists the two tools for agents.
- No lane edits `cli.go`, `mcpserver/server.go` or `parity_test.go`: the registry derives dispatch, usage, tools and parity rows. Lane 6.3 updates `internal/mcpserver/testdata/golden/tools.json` and the CLI golden files through `-update` only.

### Phase 6 Harnesses page (lane 6.4)

- One page, `#harnesses`, from `HarnessesState` only.
- Groups: **Ready** (`native`, `adapter_installed` with probe `ok`), **Needs attention** (every other found state, and found harnesses not probed yet), each harness as a card: name, ID, kind, state with its code, version and registry version, the command and path, the layers (`sources`), the probe status and time, the ACP capabilities, the fix (text, the command with a Copy button, the link), and the sign-in methods with their commands to copy.
- A header with the registry commit and date, whether automatic probes are on (a link to the Settings group `ai`), the skipped PATH entries and the problems.
- **Probe now** (all, or one card) sends `POST /api/harnesses/probe`; cards show "probing" from `probing`. **Focus**: on `visibilitychange` to visible and on window focus, the page sends `POST /api/harnesses/check`.
- Overrides and auto-probe are edited on the Settings page (group `ai`), never here.
- Every text from the state is shown as text, never HTML. No registry icon is loaded: the page draws its own generic icon per kind.

### Phase 6 test guard

No test may resolve or start a real harness (plan guardrail).

- **In production code** (lane 6.1, `internal/harness/guard.go`): when `testing.Testing()` is true, the finder refuses every PATH entry, override path and found file whose resolved path is not inside `os.TempDir()` (resolved). Detection then fails with `harness.ErrTestGuard` (code `test_guard`), naming the entry. The probe checks again right before `jsonrpc.Launch` and `jsonrpc.Run`. A test that forgets to set a fake PATH therefore fails, whatever package it is in.
- **`harnesstest`** (lane 6.1): `harnesstest.PATH(tb, dirs...)` sets `PATH` to temporary directories only, and `HOME` and `AGENTFLOW_HOME` to fresh temporary directories (`tb.Setenv`). `harnesstest.Bin(tb, name, version)` writes a non-ACP executable that prints a version, for finder and version tests. Fake ACP agents come from `acptest.Install` (lane 6.2).
- `TestGuardRefusesRealPATH` (lane 6.1) runs detection with `PATH=/usr/bin:/bin` and expects `ErrTestGuard`.
- Every test that reaches detection, in any lane (CLI, MCP, webui, ops), sets its PATH with `harnesstest.PATH`.
- **Existing daemon tests.** `stream_runtime_harnesses.go` registers from `init`, so every webui test that starts a daemon runtime would reach detection. As with `newPluginOptions` (phase 5), the extension reads its dependencies from `newHarnessOptions`; in a test binary that returns no `Detector` unless a test sets one, so the extension starts no source and the state has `detector: false`. Lane 6.2 tests set it with a fake PATH.
- **`make e2e` (lane 6.3).** The guard does not cover it: e2e runs the real binary, and the MCP server inherits `claude`'s real PATH, on which `claude` itself (the base CLI of `claude-acp`) is found. With both toggles on by default, `scripts/e2e-claude.sh` must write `ai: {harnesses: {auto_probe: false, sign_in_check: false}}` into its temporary `AGENTFLOW_HOME/config.yml` before anything starts, call `agentflow_ai_harnesses` and `agentflow_ai_doctor` without `probe`, and finish by asserting that `$AGENTFLOW_HOME/harnesses/` holds no probe or sign-in record and no `probe.log`. A run that breaks this fails. Lane 6.3 owns the change to `scripts/e2e-claude.sh`.
- **Daemon and CLI tests.** Because both toggles default to on, every test home written by `harnesstest.PATH` sets both to false unless the test turns one on with fakes on its PATH.

### Phase 6 demo and gate

Runnable with no real harness, on a `make build` of `develop`:

```bash
T=$(mktemp -d)
go build -o "$T/bin/fake-acp" ./internal/acp/acptest/cmd/fake-acp
cp "$T/bin/fake-acp" "$T/bin/fake-cli"
export AGENTFLOW_HOME="$T/home" PATH="$T/bin:/usr/bin:/bin"
./agentflow config set --yes ai.harnesses.auto_probe=true 'ai.harnesses.overrides={
  "demo-ready":   {"name": "Demo ready",   "command": "fake-acp", "args": ["--mode=ok"]},
  "demo-auth":    {"name": "Demo sign-in", "command": "fake-acp", "args": ["--mode=auth"]},
  "demo-v2":      {"name": "Demo v2",      "command": "fake-acp", "args": ["--mode=v2"]},
  "demo-slow":    {"name": "Demo slow",    "command": "fake-acp", "args": ["--mode=hang"]},
  "demo-adapter": {"name": "Demo adapter", "kind": "adapter", "command": "fake-adapter-acp",
                   "base_name": "Demo CLI", "base_commands": ["fake-cli"]}}'
./agentflow ai harnesses            # all found, probe "none", fix: probe them
./agentflow ai harnesses --probe    # native, needs_auth, incompatible (protocol_version), native with probe failed/timeout, adapter_available
./agentflow ai doctor               # checks with fixes
```

`auto_probe` and `sign_in_check` are on by default, so the demo also works when the coordinator runs it from an agent session. The fake agent answers `-32000` to `initialize` in `--mode=auth`; real agents normally do not. The sign-in check is shown with a fake CLI: add `"demo-signin": {"name": "Demo sign-in check", "kind": "adapter", "command": "fake-acp", "args": ["--mode=ok"], "base_name": "Demo CLI", "base_commands": ["fake-cli"], "status_from": "base", "status_args": ["--status=out"]}` to the overrides (`fake-acp --status=out` exits 1, `--status=in` exits 0). It shows `needs_auth`/`signed_out`; with `DEMO_API_KEY=x` exported before `ai harnesses --probe` it shows sign-in `unknown`/`env_credential` instead.

1. The five states and their fixes appear as above, from the CLI and from `agentflow_ai_harnesses` over MCP.
2. After `./agentflow config set --yes ai.harnesses.auto_probe=false`, `agentflow_ai_harnesses {"probe": true}` returns `probe_refused: needs_person` and runs nothing (the fake agent's `probe.log` gets no new line). After `ai.harnesses.sign_in_check=false`, a person's `--probe` shows sign-in `unknown`/`off` and the fake CLI is not started.
3. `./agentflow ui` (started with the PATH above): the Harnesses page shows the same list. `rm "$T/bin/fake-cli"` makes `demo-adapter` disappear (`harness.removed`) within one poll, or at once on focus; `cp` it back and it returns (`harness.detected`). `agentflow events --follow --topic 'global/*/harness.**'` prints the same events.
4. In a scratch git repository, put a copy of `fake-acp` in its `bin/` and that directory first on PATH, then run `ai doctor` from inside the repository: it lists the entry as skipped (`inside_repository`), and nothing from it is started.

**Gate:** all four lanes merged; `make check` green on `develop` (including `TestPinnedRegistry`, `TestOverlayMatchesRegistry`, `TestGuardRefusesRealPATH`, `TestEveryConfigFieldHasControl`, `TestEveryMCPToolHasCLICommand`); `make e2e` passes (new MCP tools); the demo works; tag `develop-p6`; ledger updated.

### Phase 6 integration amendments

*Amendment from the phase 6 integration (issue #114), after lanes 6.1 to 6.4.* These change or add to the sections above; where they differ, this section wins.

**Production wiring.**

- `harness.Production(home, version, workDirs)` is the one production constructor. It builds `FileStore`, `NewProber(home, version, store)` and `NewSignInCheck(home)` over the same home. `app.NewHarnessDetector` (the CLI and MCP) and the daemon's `productionHarnessOptions` both use it. Without a usable home it wires no `Prober` and no sign-in check, so no probe file lands relative to the working directory. `harness.NewForWorkDir(workDir, version)` is `Production` for one work directory.
- The daemon's work directories are every worktree it has registered (its index, read on each pass, plus the worktree being registered as it starts) and every worktree attached to its runtime, so the first pass already skips them. The home checks (`config.InsideRepository`) cover any repository and need no list.
- `auto_probe` and `poll_interval` are read from the personal `config.yml` by `harness.ReadSettings` on every pass, and `sign_in_check` and `probe_timeout` by the Detector on every pass, so a change applies without a restart. An unsafe or unreadable home counts both toggles as off.
- A console probe runs `ai.harnesses` with `app.WithHarnessRequester(ctx, person)` and `app.WithHarnessDetector(ctx, daemonDetector)`: the daemon's PATH rules and its prober's limits apply to it. Only the console's probe handler and the CLI Verb set a requester; no input field does.
- `PublishedState.ProbeStatus` is written and read with the snapshot, so a probe-status change after a daemon restart is still published.

**Probe announcements (coordinator decision).** The page must learn that a console probe started and landed even when nothing changed. A console probe publishes `harness.changed`, trigger `probe`, for each probed ID that the last pass published: once when it starts (the projection then has those IDs in `probing`) and once from the pass after it ends (with `probing` cleared). An ID with a real change in that pass gets only its diff event. The end IDs are kept apart from the trigger name, so a focus or config trigger during the probe does not lose them.

**One installed program is listed once.** Two catalog entries can name the same command (the legacy `kimi` and `kimi-code` both install `kimi`). Found definitions that found the same file on PATH (the path as found, before links are resolved) form a group, and only the highest-ranked member is listed: highest layer (personal, plugin, agentflow, registry), then lowest ID. A definition a person or a plugin shaped is never dropped. So `kimi-code` (registry and agentflow overlay) is listed and the legacy `kimi` is left out, in every list including `--all`. Links are not followed for this: a version manager (Volta, mise) links many command names to one binary, and those stay separate harnesses.

**A stored `signed_out` is checked against the current environment.** When the Detector shows a cached verdict, a `signed_out` becomes `unknown`/`env_credential` while a credential variable (the definition's `credential_env` or a credential suffix) is set now, so a variable set after the check is honoured without running the status command again.

**`DoctorReport` checks.** A check's `fix` is a `harness.Fix` object (`{text, command?, url?}`), not a string: `{checks: [{id, status, message, fix?: harness.Fix}], report: harness.Report}`.

**Overrides columns.** `personal:ai.harnesses.overrides` has 17 columns: the twelve above plus `status_signed_in` and `status_signed_out` (exit codes, `chips`), `status_signed_out_line` (`text`), `config_env` (`chips`) and `status_source` (`text`), all marked advanced. A personal status command may therefore set its own exit codes and signed-out line.

**`config_env` names.** These variables are passed, with their values, to a command that runs with the real `HOME`, so `harnessdef.Validate` refuses, in every layer (overlay, plugin, personal): a name that changes program lookup, loading or trust (the sign-in guidance list: `PATH`, `LD_*`, `DYLD_*`, `NODE_OPTIONS` and the rest); a credential-like name (the credential suffixes, any name with `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD` or `CREDENTIAL`, a name ending in `_KEY`, `SSH_AUTH_SOCK`, or a name in the definition's `credential_env`); and an `AGENTFLOW_*` or `DEVFLOW_*` name. The status profile applies the same rule again at run time (`harnessdef.ConfigEnvRefused`). `CODEX_HOME` and `CLAUDE_CONFIG_DIR` pass.

**A repository `ai:` key is accepted and ignored**, not a `syntax` problem. The repository loader keeps unknown keys aside, as for every key it does not know, and no harness code reads the repository layer. Making it an error would fail every command in that repository, which is a larger change than phase 6 makes.

**`Found.BaseFound` is a `*Found`** (as amended in the `Found` type above). The sign-in check starts the base file the finder found and checked (its `Path`, resolved `Target` and `PathDir`), and its `Identity` decides when a stored verdict is due again.

### Phase 6 security review amendments

*Amendment from the independent security review of the phase 6 integration (PR #116).* These change the sections above; where they differ, this section wins.

**1. A plugin's harness is started only by a person's own probe that names it** (changes "Phase 6 who may probe" and decision 9).
- A declarative plugin promises to run no code, but a harness entry names a PATH program with arguments (`command`, `args`, `version_args`), and `auto_probe` is on by default. So a harness whose command, args or version_args were chosen by a plugin layer is never started by the daemon or for an agent, whatever `auto_probe` says.
- A probe of every harness is not consent for a plugin's command. Only a person's probe that names the harness's ID starts it: `agentflow ai harnesses --probe --id <id>` (the fix command), or the ids the console's card **Probe now** sends (`POST api/harnesses/probe` with `{"ids":[...]}`). `ai harnesses --probe` with no `--id`, `ai doctor --probe` and the page's top **Probe now** (no ids) skip it; the console does not mark it as probing. `ai doctor`'s `harnesses` check warns and names each such harness with `agentflow ai harnesses --probe --id <id>`, also when another harness is ready. The rule is in the Detector: `DetectOptions.IDs` must contain the ID and the requester must be a person. The page's card puts its own **Probe now** in the fix box for `plugin_launch`, as it does for `not_probed` and `needs_person`. Tests: `TestDetectPluginHarnessOnlyByName`, `TestHarnessProbe`, the Harnesses page fixture and the doctor case "plugin harness waits for a probe by name".
- `harness.Layered.PluginLaunch` records the plugin. A plugin adds only new IDs, so it chose all three launch fields unless the person's override sets each again (`no_version` counts for `version_args`).
- Until a person probes it by name, such a harness has probe `refused`, code `plugin_launch` (new), the note `plugin_launch` (new), and the fix "A plugin chose the command or its arguments, so agentflow never probes it automatically, for an agent or in a probe of every harness. Probe it by name if you trust it." with `agentflow ai harnesses --probe --id <id>`. After that probe, a later pass shows its saved result (`cached`) and starts nothing. The page shows the same.
- `plugin add`'s summary shows, per harness, the command and args, the version command (`--version` by default) or "no version command", and that it runs "only when you probe it by name" with the `--probe --id <id>` command.
- `harnessdef.Validate` accepts, in a **plugin** layer only, `args` and `version_args` values that are plain tokens and nothing else: an optional `-` or `--`, then `[A-Za-z0-9][A-Za-z0-9._-]*`, optionally `=` and a value of `[A-Za-z0-9._-]+`, with no `..` (`harnessdef.PluginArgAllowed`; `PluginArgRefused` is its inverse; code `args`). This allowlist replaces the first review's deny rules, which a second review bypassed with a path glued to a flag (`-C/Users/x/repo`, `-f/etc/passwd`, `--output-file/x`) and with `user@host`, so `{command: git, args: [-C/path, reset, --hard]}` was accepted. The definition is left out whole with a problem, so nothing runs. The registry, overlay and personal layers keep `ValidArg`. Every `args` value in the pinned registry and the overlay passes the allowlist except `--uid=` (antigravity-acp, Linux), whose empty value it refuses; a plugin cannot copy that entry, which is acceptable. Tests: `TestPluginArgAllowlist`, `TestValidatePluginGitReset`, `TestDetectPluginArgBypassesRefused` (every listed bypass, nothing runs even for a probe by name) and `TestDetectPluginCannotPointAProgramAtAPathOrURL` (the first review's `rsync` reproduction).

**2. More sign-in variables** (changes "People who sign in with an environment variable").
- `codex-acp` `credential_env` adds `OPENAI_FEDERATION_RULE_ID`, `OPENAI_IDENTITY_TOKEN_FILE`, `OPENAI_IDENTITY_TOKEN_SOURCE`, `OPENAI_SPIFFE_AUDIENCE` and `OPENAI_SPIFFE_ID`, verified in `openai/codex` at `bee5dc55ac118d9136171fed55ffd89af26f8d21`: `codex-rs/login/src/auth/workload_identity.rs` uses them, and `codex-rs/protocol/src/shell_environment.rs` defines each name verbatim.
- `claude-acp` `credential_env` adds `CLAUDE_CODE_USE_FOUNDRY`, `CLAUDE_CODE_USE_ANTHROPIC_AWS`, `CLAUDE_CODE_USE_MANTLE`, `CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD`, `ANTHROPIC_FEDERATION_RULE_ID`, `ANTHROPIC_ORGANIZATION_ID`, `ANTHROPIC_PROFILE` and `CLAUDE_CODE_OAUTH_REFRESH_TOKEN`, each found verbatim on <https://code.claude.com/docs/en/env-vars> on 2026-11-27 (with `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CODE_USE_BEDROCK` and `CLAUDE_CODE_USE_VERTEX`, which lane 6.1 already added).
- The generic credential suffixes are `_API_KEY`, `_AUTH_TOKEN`, `_ACCESS_TOKEN`, `_OAUTH_TOKEN` and `_REFRESH_TOKEN`.

**3. The `inside_repository` check fails closed** (changes "Phase 6 finding a harness on PATH").
- `harness.RepositoryWorktrees` adds each work directory's enclosing repository root (found by walking up for `.git`) even when `git worktree list` fails, and the superproject of a submodule (`git rev-parse --show-superproject-working-tree`, with its worktrees).
- When the enclosing repository cannot be told (a folder on the way cannot be read), every PATH entry is skipped as `inside_repository`.

**4. Configuration text never reaches a problem.**
- A `config.yml` that does not load gives "Your config.yml does not load (line N), so agentflow ignores your harness settings. Check its syntax." The YAML error text, which can quote values, is dropped.
- An override key that breaks the ID rule is shown as `(invalid ID)` in `Problem.ID` and in the message (`harnessdef.SafeID`).

**5. Tests and `make e2e`.**
- The demo's step 3 is a Go test (`TestHarnessProductionWiring`) because the live step would reach the owner's running console daemon: removing the fake base CLI publishes `harness.removed` and drops the harness from the projection in one pass, and restoring it publishes `harness.detected`.
- The console daemon's registry is the OS account's `~/.agentflow-ui`, not `AGENTFLOW_HOME`. So `scripts/e2e-claude.sh` exports `CONTINUOUS_INTEGRATION=1`, which suppresses only the automatic console, and fails when any fixture path appears in that registry. `make e2e` therefore cannot start or reuse a console daemon that would probe with the person's real settings.

**6. Versioned launcher names, and the launch a probe by name runs** (issue #120; changes "Phase 6 harness definitions" and "Phase 6 `Harness` type").
- `harnessdef.RefusedLauncher` compares `harnessdef.LauncherName(command)`: the base name (split at `/` and `\`), without case, without `.exe`, `.cmd`, `.bat` or `.com`, and without a version suffix `([._-]v?)?[0-9]+(\.[0-9]+)*[tm]?$` (a name that is only a version keeps it). So `python3.12`, `PIP3.12.EXE`, `pip-3.12`, `node22`, `node-v22` and `python3.13t` are refused. Every caller gets the rule: `Validate` (`command`, `base_commands`), the registry's default command, the probe (the found path and its resolved target), the sign-in check (path and target) and sign-in commands. The finder applies no name rule of its own; a file that resolves to a launcher (a `my-agent` link to `python3.12`) is refused by the probe and the sign-in check.
- The list adds JavaScript runners and managers (`yarnpkg`, `nodejs`, `corepack`, `tsx`, `ts-node`, `volta`, `fnm`, `nvm`), Python ones (`pythonw`, `py`, `pypy`, `poetry`, `pipenv`, `pdm`, `hatch`, `conda`, `mamba`, `micromamba`, `pixi`), other interpreters and their runners (`ruby`, `irb`, `gem`, `bundle`, `bundler`, `rake`, `cargo`, `rustup`, `go`, `perl`, `php`, `composer`, `lua`, `luajit`, `java`, `jbang`, `dotnet`, `dnx`, `awk`, `gawk`, `mawk`, `nawk`, `tclsh`, `rscript`, `osascript`, `cscript`, `wscript`, `mshta`), version and environment managers (`mise`, `asdf`, `pkgx`, `nix`, `nix-shell`), shells (`dash`, `ksh`, `mksh`, `csh`, `tcsh`, `ash`, `nu`, `elvish`, `xonsh`, `busybox`, `toybox`), programs that run their arguments or input as a command (`su`, `runuser`, `pkexec`, `xargs`, `nohup`, `nice`, `ionice`, `timeout`, `stdbuf`, `setsid`, `chroot`, `unshare`, `nsenter`, `flock`, `exec`, `time`, `watch`, `script`, `caffeinate`, `start`, `runas`, `wsl`) and `aria2c`. The suffix rule matches `pip3` and `python3`. It stays one list for every layer.
- `TestRegistryCommandsAreNotLaunchers` checks that no pinned registry default command (every platform key, and npx/uvx), binary `cmd`, overlay `command` or `base_commands` is refused. `TestRefusedLauncherNames`, `TestProbeRefusals`, `TestSignInRefusals` and `TestDetectPluginHarnessShowsItsLaunch` cover the names.
- `Harness` adds `version_args` (the effective version arguments: `["--version"]` by default) and `no_version` (additive). `harness.ProbeLaunch` writes what a probe runs as one line: "<version command>, then <command and args>" (`--version` when `version_args` is empty and `no_version` is not set), or "<command and args>, with no version command". For a harness with the note `plugin_launch`, `ai harnesses` prints "A probe by name runs: <that line>" before its fix; `ai doctor`'s `harnesses` check adds "(runs <that line>)" after each `--probe --id` command; and the Harnesses page card shows the same right before its own **Probe now**, in the fix box or, once the harness is ready, on the card, as text with no Copy button. Tests: `TestProbeLaunch`, `TestAIHarnessesShowPluginLaunch` (golden `ai/harnesses_plugin.txt`), the doctor cases "plugin harness waits for a probe by name" and "plugin harness beside a ready one", and the Harnesses page fixture.

### Phase 6 amendments to earlier contracts

All additive.

| Contract | Change | Lane |
| --- | --- | --- |
| `bus` registry | `harness.*` types registered; `harness` leaves `ReservedFamilies` | 6.2 |
| `internal/jsonrpc` | `HarnessEnv`, `Run`, and `LaunchSpec.AllowEnv` (extra names one call allows; only the sign-in status profile sets it). No change to `ScrubbedEnv`, `EnvAllowlist` or to `Launch` for a spec without `AllowEnv` | 6.1 |
| `manifest` | `PointPhase[PointHarness] = 6`; `Extensions.Harness` becomes `[]HarnessEntry` (`{id, harnessdef.Definition}`), declarative only; `plugin add`'s summary lists them | 6.1 |
| `config.User` | optional `ai` section (`auto_probe` and `sign_in_check` default on); group `ai` | 6.1 |
| settings check chain | `harness` diagnostic from `harnessdef.Validate` for `personal:ai.harnesses.overrides`; harness cases in `loosening` | 6.1 |
| `globalTopics` | the three `harness.*` topics | 6.2 |
| console routes | `POST /api/harnesses/check`, `POST /api/harnesses/probe` | 6.2 |

The phase 5 comment that "phase 6 harness definitions are the first" builtin plugins (`internal/plugin/types.go`) is superseded: phase 6 ships no builtin plugin. The comment is corrected in phase 11.3: no phase 6 lane edits `internal/plugin/*.go`.

### File ownership for phase 6 lanes

A lane changes only the files it owns. A lane that needs another lane's file, or a contract change, stops and reports.

| Lane | Issue | Owns | Must not touch |
| --- | --- | --- | --- |
| 6.1 Registry, PATH, versions | #100 | `internal/harness/**` except `probe.go`, `cache.go`, `signin.go` and their tests; in it: `types.go`, `doc.go` (contract, first PR), `harnessdef/**`, `acpregistry/**`, `overlay.yml`, `registry.go`, `find.go`, `pathsafe*.go`, `version.go`, `state.go`, `fix.go`, `detect.go`, `guard.go`, `harnesstest/**`; `scripts/refresh-acp-registry.sh`; `internal/jsonrpc/harness.go` and its test, and `LaunchSpec.AllowEnv` in `internal/jsonrpc/types.go` with its check in `launch.go`; the overlay's verified status entries; `harnesstest.StatusCLI`; `internal/config/ai.go`, the `AI` field in `config.go`, the `ai` rows in `field_meta_table.go`, the `ai` group in `catalog.go`; the `harness` check (`harnessLink` in `internal/settings/check_harness.go`, and its entry in `chain` in `internal/settings/checks.go`) and the harness cases in `policyOf` and `loosening` there; `internal/plugin/manifest/harness.go`, `PointPhase` and `Extensions.Harness` in `manifest/types.go`, the harness call in `manifest/validate.go` and the summary line in `internal/plugin/install` | `internal/plugin/*.go`, `internal/acp/**`, `internal/bus/**`, `internal/watch/**`, `internal/webui/**`, `internal/command/**`, `internal/app/**`, README, `cli.go`, `server.go`, `parity_test.go` |
| 6.2 Probe, cache, events, projection | #101 | `internal/acp/doc.go`, `types.go` (contract, first PR), `initialize.go`, tests; `internal/acp/acptest/**` (including `cmd/fake-acp`); `internal/harness/probe.go`, `cache.go`, `signin.go` (the sign-in check: status profile, verdict mapping, credential-variable rule, output discarding) and their tests; the `--status` mode of `cmd/fake-acp`; `internal/bus/types_phase6.go` and its test, the `ReservedFamilies` and `init` lines in `internal/bus/types.go`; `internal/watch/harness.go` and its test; `internal/webui/stream_runtime_harnesses.go`, `projection_harnesses.go`, `projection_harnesses_state.go` (contract, first PR), `harnesses.go`, the two route lines in `server.go`, and their tests | the lane 6.1 files, `internal/plugin/**`, `internal/webui/static/**`, `internal/command/**`, README |
| 6.3 Commands, skills, README | #102 | `internal/command/ops/ai_harnesses.go` and its test; the harness lines in `scripts/e2e-claude.sh`; `internal/app/harnesses.go` and its test; golden files through `-update` only; `adapters/claude-code/skills/ai/SKILL.md`; the two tools in `adapters/claude-code/skills/agentflow/SKILL.md`; `docs/harnesses.md`; README sections on the `ai` commands, MCP tools, harness detection and its security; the blueprint capability table (`docs/artifact/` then `scripts/build-blueprint.sh`) | `internal/harness/**`, `internal/acp/**`, `internal/webui/**`, `cli.go`, `server.go`, `parity_test.go` |
| 6.4 Harnesses page | #103 | `internal/webui/static/harnesses.*`; the Harnesses nav entry, page section and script tag in `static/index.html`; harnesses rules in `static/style.css`; `internal/webui/harnesses_browser_test.go`; `internal/webui/testdata/harnesses/**`; `docs/design/ui/harnesses.md`; the README section on the Harnesses page; the Harnesses wiring in `internal/webui/static/app.js`; the `harnesses` screen in `internal/webui/destination.go` | every Go file other than its browser test and `destination.go`, every other static file |

- All four lanes can start at once. 6.1 and 6.2 each land their contract files first.
- Lane 6.1 codes against `Store` and `Prober` with fakes; lane 6.2 against `harness.Found` and a fake `Detector`; lane 6.3 against `harness.Detector` with a fake; lane 6.4 against `HarnessesState` with a scripted stream fixture and fake endpoints. Each wires production in its last commit, after the lanes it uses have merged.
- The console probe endpoint (6.2) invokes `ai.harnesses` by name through the Invoker; its tests register a fake Spec until 6.3 merges.
- `README.md` is shared by section: 6.3 writes commands, tools, detection and security; 6.4 the Harnesses page.
- Lane 6.1 changes `internal/plugin/manifest` and `internal/plugin/install` as listed, never `internal/plugin/*.go`. (The `internal/plugin` race fix, #99, has merged, so there is nothing to wait for.)

### Phase 6 security

**Threat model.** A repository is hostile: its files, its `.envrc`, its `node_modules`. Registry text and a harness's `initialize` answer are untrusted data. A plugin's author may be hostile, but the person reviewed and enabled the plugin. The person and their personal layer are trusted.

**Security review (lanes 6.1, 6.2 and 6.3, before merge).**

- [ ] Nothing fetches the registry, an adapter or a package at runtime. The only network use is the maintainer's refresh script.
- [ ] No code path runs `npx`, `uvx`, a refused launcher, a shell, an install, an `adapter_available` adapter or its base CLI, a terminal sign-in, `authenticate` or any ACP method except `initialize`.
- [ ] A repository cannot make agentflow run a binary: PATH entries inside a worktree of a repository agentflow works in, or under `node_modules`, are skipped; no repository key names a harness; an unsafe agentflow home ignores personal overrides and plugin definitions.
- [ ] Every probe goes through `jsonrpc.Launch` or `jsonrpc.Run` with `HarnessEnv`: a test sets `GH_TOKEN`, `GITHUB_TOKEN`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `AWS_SECRET_ACCESS_KEY`, `CLAUDE_CODE_SESSION_ID` and `AGENTFLOW_HOME` in the parent and shows none reaches the fake agent.
- [ ] Every probe is bounded by its timeout and ends with the process group killed. A hanging, flooding or garbage-writing fake agent cannot block the daemon, the CLI or another probe.
- [ ] Agents cannot start a probe while `auto_probe` is off, nor a sign-in check while `sign_in_check` is off; nobody can start a sign-in check while it is off.
- [ ] The sign-in check runs only an overlay or personal status command on an already found file, in the empty status directory, with only the listed variables and the parent's real `HOME`; a test shows no credential variable reaches the fake CLI.
- [ ] No byte of a status command's output is stored, logged, published, returned or shown; only the verdict, its code and its time.
- [ ] A `signed_out` answer with a credential variable present is reported as `unknown`/`env_credential`.
- [ ] A `HOME`, XDG or `config_env` directory inside a repository agentflow works in stops the sign-in check (`unsafe_environment`).
- [ ] With both toggles at their default (on), no Go test and no `make e2e` run starts a real harness or a real status command.
- [ ] Registry and agent text is sanitized, clamped and shown as text. Fix text comes only from agentflow's templates. A sign-in command with an unsafe arg or env is not shown.
- [ ] No event, projection or result carries stderr, tokens or agent text other than the sanitized fields. Paths appear in results and the projection, under `~`, and never in events.
- [ ] `TestGuardRefusesRealPATH` passes, and no test resolves a file outside its temporary directory.

### Phase 6 decisions

*Decided by the architect in the contract PR. The coordinator confirms or changes them before the lanes start.*

1. **A sixth state, `not_installed`.** It is needed for `--all` and for `harness.removed`. It is never shown on the page or in the default list, so the plan's five states are what a person sees for installed harnesses.
2. **`needs_auth` comes from a sign-in check** (owner's choice): "Also run each harness's own status command with your real home folder and credentials." Only for harnesses whose status command is verified from vendor documentation or source (`claude-acp` and `codex-acp` today); the rest stay `unknown` with sign-in guidance. A `session/new` in the probe stays rejected: it creates sessions in the person's history and may start paid work.
3. **`auto_probe` is on by default** (owner's choice: "On by default with it being configurable in settings"). The daemon probes a harness when its binary changes, and agents may ask for probes. Turning it off is on the Settings page; turning it on again is a loosening edit.
4. **`ai harnesses` and `ai doctor` are reads.** They write only the user's cache. Probes are bounded and limited to what the person installed. Making them gated writes would put an approval on a list command.
5. **Probes use the scrubbed environment with the harness's own PATH directory added.** The plan says harnesses run with a scrubbed environment; a fixed system PATH would break every npm-installed adapter (`#!/usr/bin/env node`). The added directory has already passed the PATH checks. `HOME` stays scrubbed for probes; phase 7 decides the session environment.
6. **The registry is pinned as per-agent files at a commit, not the CDN index.** The index has no commit; the files can be diffed in review. Icons and preview channels are not used.
7. **Native or adapter, and npm bin names, are agentflow data** (`overlay.yml`), because the registry does not record them.
8. **Windows `.cmd` and `.bat` shims are not started.** `cmd.exe` re-parses arguments, which ACP's launch-safety guidance warns about. npm-installed adapters on Windows therefore show probe `refused`/`shim` until an override names an `.exe`.
9. **Plugin harness definitions: declarative, new IDs, PATH names only.** A plugin cannot redirect a registry harness to another binary or ship its own harness executable in phase 6.
10. **No contract Go files in the contract PR.** The coordinator asked for a docs-only PR. Lanes 6.1 and 6.2 land the contract files first, verbatim from this section.
11. **The sign-in check has its own toggle, `sign_in_check`, on by default.** It is a different risk from a probe: it runs with the person's real home and credentials, while a probe runs in an empty home. A person who accepts automatic probes may still not want CLIs started with their credentials, so one toggle cannot serve both. Every option has its own control. When `sign_in_check` is on, the check follows `auto_probe` for automatic and agent-requested runs, and always runs for a person's probe.

**Open questions for the coordinator.**

- Decision 8 leaves npm adapters unprobed on Windows. Confirm, or accept `.cmd` shims with arguments limited to the arg rule.

### Phase 6 consequences

- `ai harnesses`, `ai doctor`, their MCP tools and the Harnesses page show the same report, from one `Detector`.
- A person sees each installed harness's state and an exact fix, and agentflow never installs anything.
- A repository cannot choose a binary for agentflow to run. Agents can ask for probes while `auto_probe` is on, the default, and the person can turn that off.
- `needs_auth` is real for the harnesses with a verified status command; for the others agentflow says it does not know.
- Phase 7 builds on `internal/acp`, `acptest`, the harness definitions, the launch profile and `Store.MarkAuthRequired`. The ACP "Agent Authentication State Query" RFD (`auth/status`, which `codex-acp` already implements) is a possible later source of sign-in state, once it is in the stable schema. Phase 9's `delegate` reads its agents from the same registry, and phase 10's `harness add` adds a layer.

## Phase 7

*Amendment for phase 7 (issue #121), lanes 7.1 to 7.4 (issues #122 to #125).* Phase 7 starts ACP sessions. `agentflow ai run --harness X "<prompt>"` runs one prompt turn of an installed harness in a task worktree and streams it, an AI panel in the console streams the same session, and `ai status`, `ai respond` and `ai cancel` work from the CLI and MCP. When the harness asks for permission, or asks agentflow to read a file, write a file or run a command, a chain of checks decides: the never-list, the repository boundary, a policy rule, then `policy.Gate`, and deny when nothing allows.

**A session runs a coding agent with the person's sign-in, in the person's repository.** Every rule in this section follows from that. A probe (phase 6) ran a harness for a few seconds in an empty home; a session runs it for minutes, with the person's credentials, and lets it change files. So agentflow starts only a harness that is ready, in a worktree the person chose, with a listed environment and hard limits; it checks every request that reaches it over ACP; and it never lets an agent, a plugin, a repository or the session itself give an "allow" that only a person may give.

Everything in the phase 1 to 6 sections still holds unless a paragraph here says it changes.

**No contract Go files in this PR.** As in phase 6, the contract PR is documentation only. The Go declarations below are the contract. Each contract file belongs to one lane, and that lane lands it first, verbatim, as a small PR of its own. **Lane 7.1 lands first** (lane 7.2's types import `internal/acp`), then lane 7.2. Lanes 7.3 and 7.4 code against the declarations here from the start.

| Contract | Go | First PR of lane | Order |
| --- | --- | --- | --- |
| ACP v1 session types, methods, `ClientHandler`, session limits | `internal/acp/session_types.go`, the phase 7 paragraph in `internal/acp/doc.go` | 7.1 | 1 |
| `Limits.OrderedResponses` and `LaunchSpec.AllowDir` (declarations only) | `internal/jsonrpc/types.go` | 7.1 | 1 |
| Session, states, codes, request kinds, `PermissionHandler`, `Verdict`, answers, requester, store and host interfaces, transcript items, limits | `internal/aisession/types.go`, `internal/aisession/doc.go` | 7.2 | 2 |
| Event types and payloads | `internal/bus/types_phase7.go`; `ai` leaves `ReservedFamilies` in `internal/bus/types.go` | 7.2 | 2 |
| AI projection state | `internal/webui/projection_ai_state.go` (`AIState`) | 7.2 | 2 |

### Phase 7 what agentflow controls, and what it cannot

Read this first. It sets the limits of every guarantee below.

The ACP v1 security page (`docs/protocol/v1/security.mdx` at the pinned commit) says three things that shape the whole phase:

- "Permission requests are advisory: the Agent performs its own operations and is responsible for honoring the user's decision."
- The client performs file system and terminal operations itself, so it is responsible for checking them.
- A malicious agent binary and operating-system sandboxing are out of scope.

So agentflow's checks bind exactly three things:

| What | Who performs it | agentflow's control |
| --- | --- | --- |
| `fs/read_text_file`, `fs/write_text_file` | agentflow | Full: agentflow does the read or write itself, after the chain, through `os.Root` at the worktree. |
| `terminal/create` and the other `terminal/*` methods | agentflow | Full for the start: agentflow starts the process itself, after the chain. What the process then does is not confined (no sandbox). |
| `session/request_permission` | the harness, afterwards | Advisory: agentflow answers; the harness decides whether to honour it. |
| The harness's own tools (its built-in shell, editor, web fetch) | the harness | **None.** They never reach agentflow. |

A harness such as `claude-agent-acp` or `codex-acp` may run its own shell or edit files with its own tools without calling `fs/*` or `terminal/*`, and without asking. agentflow cannot see that or stop it. What agentflow does about it:

- It advertises `fs` and `terminal`, so a harness that supports them routes file and command work through agentflow's checks.
- It runs every session in a task worktree only (open question 9), so the person's main checkout is never touched and every change is a reviewable diff.
- It gives the harness a listed environment without tokens or keys ("Phase 7 session environment").
- It reports tool calls the harness ran without asking, and cancels the session when one names a place outside the worktree ("Phase 7 tool calls the harness did not ask about").
- It records which files changed in the worktree when the session ends.
- It can set a session mode the person chose for that harness (`ai.sessions.modes`), since mode IDs are the agent's own and agentflow cannot tell which one asks more.

Open question 2 asks the owner to accept this limit. It is the main residual risk of the phase.

**Same-user processes.** Every process agentflow starts runs as the person. Such a process can read any file the person can, including the agentflow home, the console registry (`~/.agentflow-ui`) and the session store. No check in this section can stop a process that deliberately reads those files and imitates a person. So no dangerous decision rests on one identity check: each rests on several layers ("Phase 7 who may start a session and who may answer"), and the section says where the last layer is a same-user limit.

### Phase 7 patterns

| Piece | GoF / EIP pattern | Go |
| --- | --- | --- |
| Background sessions, started, followed and stopped from anywhere | Process Manager (EIP) | `aisession.Host`, `aisession.Manager` |
| Session lifecycle | State (a transition table) | `aisession.State`, `aisession.Transitions` |
| Deciding each agent request | Chain of Responsibility | `aisession.PermissionHandler`: never-list, boundary, rule, gate, default deny |
| The harness behind ACP | Proxy (GoF); Request-Reply (EIP) | `acp.ClientHandler` over `jsonrpc.Conn` |
| `session/update` decoding | Message Translator; Content Filter | `acp` decode rules, `aisession` sanitizing |
| A held request waiting for a person | Claim Check (the ask file) with a correlation ID | `asks/<ask>.json`, `asks/<ask>.answer.json` |
| Agent text | Content Filter | sanitizing and best-effort redaction before the store |
| Live panel | Event Notification with a server-side projection | `ai.session.*` → the `ai` projection reading the session store |
| Session output to the stream | Aggregator (coalescing) | `ai.session.output` at most 4 a second per session |
| Unanswered asks | Message Expiration | deny at `ask_timeout` |
| A frame that is not JSON-RPC | Invalid Message Channel | the connection closes; the session ends `protocol` |

### Phase 7 ACP v1 session types (lane 7.1)

**Source.** The same pin as phase 6: `agentclientprotocol/agent-client-protocol` tag `schema-v1.35.0`, commit `065b57951b6b2a91ac5eddd249e145d1f6d54545`. It is still the newest `schema-v1.*` tag on 2026-11-27. Shapes are checked against `schema/v1/schema.json` and `schema/v1/meta.json` (the stable schema, never `schema.unstable.json`), and against `docs/protocol/v1/session-setup.mdx`, `prompt-turn.mdx`, `tool-calls.mdx`, `file-system.mdx`, `terminals.mdx`, `cancellation.mdx` and `security.mdx` at that commit.

`internal/acp/session_types.go`:

```go
package acp

import "encoding/json"

// Agent methods agentflow calls in phase 7 (meta.json "agentMethods").
const (
    MethodSessionNew     = "session/new"
    MethodSessionPrompt  = "session/prompt"
    MethodSessionSetMode = "session/set_mode"
    MethodSessionCancel  = "session/cancel" // a notification
)

// Client methods the agent calls on agentflow (meta.json "clientMethods").
const (
    MethodSessionUpdate       = "session/update" // a notification
    MethodRequestPermission   = "session/request_permission"
    MethodReadTextFile        = "fs/read_text_file"
    MethodWriteTextFile       = "fs/write_text_file"
    MethodTerminalCreate      = "terminal/create"
    MethodTerminalOutput      = "terminal/output"
    MethodTerminalWaitForExit = "terminal/wait_for_exit"
    MethodTerminalKill        = "terminal/kill"
    MethodTerminalRelease     = "terminal/release"
)

// MethodCancelRequest is the protocol notification "$/cancel_request".
const MethodCancelRequest = "$/cancel_request"

// NewSessionRequest. cwd must be absolute. additionalDirectories and
// systemPrompt are never sent in phase 7.
type NewSessionRequest struct {
    Cwd        string      `json:"cwd"`
    MCPServers []MCPServer `json:"mcpServers"` // required: [] when none
}

// MCPServer is a stdio MCP server (McpServerStdio). Phase 7 sends only
// agentflow's own; http and sse are never sent.
type MCPServer struct {
    Name    string        `json:"name"`
    Command string        `json:"command"` // absolute path
    Args    []string      `json:"args"`
    Env     []EnvVariable `json:"env"` // required: []
}

type EnvVariable struct {
    Name  string `json:"name"`
    Value string `json:"value"`
}

type NewSessionResponse struct {
    SessionID string            `json:"sessionId"`
    Modes     *SessionModeState `json:"modes,omitempty"` // default on error
    // configOptions is ignored in phase 7.
}

type SessionModeState struct {
    CurrentModeID  string        `json:"currentModeId"`
    AvailableModes []SessionMode `json:"availableModes"` // skip invalid items
}

type SessionMode struct {
    ID          string `json:"id"`
    Name        string `json:"name"`
    Description string `json:"description,omitempty"`
}

type SetSessionModeRequest struct {
    SessionID string `json:"sessionId"`
    ModeID    string `json:"modeId"`
}

type PromptRequest struct {
    SessionID string         `json:"sessionId"`
    Prompt    []ContentBlock `json:"prompt"` // phase 7 sends one text block
}

type PromptResponse struct {
    StopReason StopReason `json:"stopReason"`
}

type StopReason string

const (
    StopEndTurn         StopReason = "end_turn"
    StopMaxTokens       StopReason = "max_tokens"
    StopMaxTurnRequests StopReason = "max_turn_requests"
    StopRefusal         StopReason = "refusal"
    StopCancelled       StopReason = "cancelled"
)

type CancelNotification struct {
    SessionID string `json:"sessionId"`
}

// CancelRequestNotification is "$/cancel_request". RequestID is the raw
// JSON-RPC ID (number, string or null).
type CancelRequestNotification struct {
    RequestID json.RawMessage `json:"requestId"`
}

// ContentBlock keeps text and the names of other blocks. Image and audio
// data and embedded resource contents are dropped while decoding.
type ContentBlock struct {
    Type     string `json:"type"`               // text, image, audio, resource_link, resource
    Text     string `json:"text,omitempty"`     // text
    URI      string `json:"uri,omitempty"`      // resource_link, resource
    Name     string `json:"name,omitempty"`     // resource_link
    MimeType string `json:"mimeType,omitempty"` // image, audio, resource_link
}

type SessionNotification struct {
    SessionID string        `json:"sessionId"`
    Update    SessionUpdate `json:"update"`
}

// UpdateKind is the "sessionUpdate" discriminator.
type UpdateKind string

const (
    UpdateUserMessageChunk  UpdateKind = "user_message_chunk"
    UpdateAgentMessageChunk UpdateKind = "agent_message_chunk"
    UpdateAgentThoughtChunk UpdateKind = "agent_thought_chunk"
    UpdateToolCall          UpdateKind = "tool_call"
    UpdateToolCallUpdate    UpdateKind = "tool_call_update"
    UpdatePlan              UpdateKind = "plan"
    UpdateAvailableCommands UpdateKind = "available_commands_update"
    UpdateCurrentMode       UpdateKind = "current_mode_update"
    UpdateConfigOption      UpdateKind = "config_option_update"
    UpdateSessionInfo       UpdateKind = "session_info_update"
    UpdateUsage             UpdateKind = "usage_update"
)

// SessionUpdate is one decoded update. Exactly the field for its Kind is
// set. Any other kind (notice, compaction_*, plan_update, plan_removed,
// subagent_update, session_message*, or a future one) keeps only Kind and
// Raw: it is recorded as "unknown" and never acted on.
type SessionUpdate struct {
    Kind           UpdateKind
    Content        *ContentBlock   // the three *_chunk kinds
    MessageID      string          // the three *_chunk kinds, optional
    ToolCall       *ToolCall       // tool_call
    ToolCallUpdate *ToolCallUpdate // tool_call_update
    Plan           *Plan           // plan
    CurrentModeID  string          // current_mode_update
    Title          string          // session_info_update
    Usage          *UsageUpdate    // usage_update
    Raw            json.RawMessage // unknown kinds, at most 64 KiB, else dropped
}

type ToolKind string

const (
    ToolRead       ToolKind = "read"
    ToolEdit       ToolKind = "edit"
    ToolDelete     ToolKind = "delete"
    ToolMove       ToolKind = "move"
    ToolSearch     ToolKind = "search"
    ToolExecute    ToolKind = "execute"
    ToolThink      ToolKind = "think"
    ToolFetch      ToolKind = "fetch"
    ToolSwitchMode ToolKind = "switch_mode"
    ToolOther      ToolKind = "other" // also the value for an unknown kind
)

type ToolCallStatus string

const (
    ToolPending    ToolCallStatus = "pending"
    ToolInProgress ToolCallStatus = "in_progress"
    ToolCompleted  ToolCallStatus = "completed"
    ToolFailed     ToolCallStatus = "failed"
)

type ToolCall struct {
    ToolCallID string             `json:"toolCallId"`
    Title      string             `json:"title"`
    Kind       ToolKind           `json:"kind,omitempty"`   // default on error: other
    Status     ToolCallStatus     `json:"status,omitempty"` // default on error: pending
    Content    []ToolCallContent  `json:"content,omitempty"`
    Locations  []ToolCallLocation `json:"locations,omitempty"`
    RawInput   json.RawMessage    `json:"rawInput,omitempty"` // at most 64 KiB, else dropped
}

// ToolCallUpdate: nil or empty fields are unchanged.
type ToolCallUpdate struct {
    ToolCallID string             `json:"toolCallId"`
    Title      *string            `json:"title,omitempty"`
    Kind       *ToolKind          `json:"kind,omitempty"`
    Status     *ToolCallStatus    `json:"status,omitempty"`
    Content    []ToolCallContent  `json:"content,omitempty"`
    Locations  []ToolCallLocation `json:"locations,omitempty"`
    RawInput   json.RawMessage    `json:"rawInput,omitempty"`
}

// ToolCallContent: Type "content" (Content), "diff" (Path, OldText,
// NewText) or "terminal" (TerminalID). "session" and unknown types are
// skipped.
type ToolCallContent struct {
    Type       string        `json:"type"`
    Content    *ContentBlock `json:"content,omitempty"`
    Path       string        `json:"path,omitempty"`
    OldText    *string       `json:"oldText,omitempty"`
    NewText    string        `json:"newText,omitempty"`
    TerminalID string        `json:"terminalId,omitempty"`
}

type ToolCallLocation struct {
    Path string  `json:"path"`
    Line *uint32 `json:"line,omitempty"`
}

type Plan struct {
    Entries []PlanEntry `json:"entries"` // skip invalid items
}

type PlanEntry struct {
    Content  string `json:"content"`
    Priority string `json:"priority"` // high, medium, low
    Status   string `json:"status"`   // pending, in_progress, completed
}

type UsageUpdate struct {
    Used uint64 `json:"used"`
    Size uint64 `json:"size"`
    Cost *Cost  `json:"cost,omitempty"` // agent-reported; never trusted
}

type Cost struct {
    Amount   float64 `json:"amount"`
    Currency string  `json:"currency"`
}

type RequestPermissionRequest struct {
    SessionID string             `json:"sessionId"`
    ToolCall  ToolCallUpdate     `json:"toolCall"`
    Options   []PermissionOption `json:"options"`
}

type PermissionOptionKind string

const (
    AllowOnce    PermissionOptionKind = "allow_once"
    AllowAlways  PermissionOptionKind = "allow_always"
    RejectOnce   PermissionOptionKind = "reject_once"
    RejectAlways PermissionOptionKind = "reject_always"
)

type PermissionOption struct {
    OptionID string               `json:"optionId"`
    Name     string               `json:"name"`
    Kind     PermissionOptionKind `json:"kind"`
}

type RequestPermissionResponse struct {
    Outcome PermissionOutcome `json:"outcome"`
}

// PermissionOutcome is {"outcome":"cancelled"} or
// {"outcome":"selected","optionId":"..."}.
type PermissionOutcome struct {
    Outcome  string `json:"outcome"` // OutcomeSelected or OutcomeCancelled
    OptionID string `json:"optionId,omitempty"`
}

const (
    OutcomeSelected  = "selected"
    OutcomeCancelled = "cancelled"
)

type ReadTextFileRequest struct {
    SessionID string  `json:"sessionId"`
    Path      string  `json:"path"`
    Line      *uint32 `json:"line,omitempty"`  // 1-based
    Limit     *uint32 `json:"limit,omitempty"` // lines
}

type ReadTextFileResponse struct {
    Content string `json:"content"`
}

type WriteTextFileRequest struct {
    SessionID string `json:"sessionId"`
    Path      string `json:"path"`
    Content   string `json:"content"`
}

type WriteTextFileResponse struct{}

type CreateTerminalRequest struct {
    SessionID       string        `json:"sessionId"`
    Command         string        `json:"command"`
    Args            []string      `json:"args,omitempty"`
    Env             []EnvVariable `json:"env,omitempty"`
    Cwd             *string       `json:"cwd,omitempty"`
    OutputByteLimit *uint64       `json:"outputByteLimit,omitempty"`
}

type CreateTerminalResponse struct {
    TerminalID string `json:"terminalId"`
}

// TerminalRequest is the params of terminal/output, wait_for_exit, kill
// and release.
type TerminalRequest struct {
    SessionID  string `json:"sessionId"`
    TerminalID string `json:"terminalId"`
}

type TerminalOutputResponse struct {
    Output     string              `json:"output"`
    Truncated  bool                `json:"truncated"`
    ExitStatus *TerminalExitStatus `json:"exitStatus,omitempty"`
}

// TerminalExitStatus is also the result of terminal/wait_for_exit.
type TerminalExitStatus struct {
    ExitCode *uint32 `json:"exitCode"`
    Signal   *string `json:"signal"`
}

// ClientHandler is what agentflow does for each agent request. Lane 7.2
// implements it (the session host); lane 7.1 decodes and dispatches.
// Returning a *jsonrpc.Error sends it as is.
type ClientHandler interface {
    SessionUpdate(ctx context.Context, n SessionNotification)
    RequestPermission(ctx context.Context, r RequestPermissionRequest) (RequestPermissionResponse, error)
    ReadTextFile(ctx context.Context, r ReadTextFileRequest) (ReadTextFileResponse, error)
    WriteTextFile(ctx context.Context, r WriteTextFileRequest) (WriteTextFileResponse, error)
    CreateTerminal(ctx context.Context, r CreateTerminalRequest) (CreateTerminalResponse, error)
    TerminalOutput(ctx context.Context, r TerminalRequest) (TerminalOutputResponse, error)
    WaitForTerminalExit(ctx context.Context, r TerminalRequest) (TerminalExitStatus, error)
    KillTerminal(ctx context.Context, r TerminalRequest) error
    ReleaseTerminal(ctx context.Context, r TerminalRequest) error
}

// agentflow's codes in answers to an agent. ACP reserves -32000, -32002
// and -32800; -32001 is in its "Other" range.
const (
    // CodeDenied: the chain denied the request. Data is {"code": "<verdict code>"}.
    CodeDenied = -32001
)

// SessionLimits are the jsonrpc limits of a session connection.
var SessionLimits = jsonrpc.Limits{
    MaxFrame:         8 << 20,
    MaxInFlight:      16,
    CallTimeout:      30 * time.Second, // initialize, session/new, set_mode; never prompt
    OrderedResponses: true,
}

// SessionInitialize is the initialize request of a session. It differs
// from the probe's: fs and terminal are true, auth.terminal is false.
func SessionInitialize(version string) InitializeRequest

// NewClientHandler decodes every agent request by the phase 6 decode rules
// and calls h. Unknown methods, ext methods ("_..."), elicitation/* and
// every method agentflow does not advertise get -32601. A
// "$/cancel_request" for a request h is still handling cancels its context;
// the request is then answered -32800, or {"outcome":"cancelled"} for a
// permission request.
func NewClientHandler(h ClientHandler) jsonrpc.Handler

func NewSession(ctx context.Context, c jsonrpc.Conn, r NewSessionRequest) (NewSessionResponse, error)
func SetMode(ctx context.Context, c jsonrpc.Conn, r SetSessionModeRequest) error

// Prompt sends session/prompt and waits until ctx ends. ctx must have a
// deadline (ErrNoDeadline): jsonrpc's CallTimeout never applies to it.
func Prompt(ctx context.Context, c jsonrpc.Conn, r PromptRequest) (PromptResponse, error)

// Cancel sends session/cancel.
func Cancel(ctx context.Context, c jsonrpc.Conn, sessionID string) error
```

(The imports of `context`, `time` and `internal/jsonrpc` are left out of the listing.)

**The session `initialize` request** (`SessionInitialize`):

```json
{"protocolVersion": 1,
 "clientCapabilities": {"fs": {"readTextFile": true, "writeTextFile": true}, "terminal": true, "auth": {"terminal": false}},
 "clientInfo": {"name": "agentflow", "title": "agentflow", "version": "<agentflow version>"}}
```

agentflow advertises no `session`, `subagents`, `plan` or `elicitation` capability, so the agent must not send `plan_update`, `plan_removed`, subagent updates, notices or elicitations. If it does, they are recorded as unknown updates or answered `-32601`. The answer must give `protocolVersion` 1, else the session ends `protocol_version`.

### Phase 7 ACP client (lane 7.1)

- **Decode rules.** Every ACP message follows the phase 6 rule ("default on error", "skip invalid items", unknown fields ignored). A `session/update` whose `sessionUpdate` is unknown is kept as `Kind` and `Raw` and never an error. An unknown `ToolKind` decodes as `other`.
- **Ordered responses.** `internal/jsonrpc` runs peer notifications on their own queue, so a response can reach `Call` before the `session/update` notifications sent before it are handled. Lane 7.1 adds `Limits.OrderedResponses` (additive, in `conn.go`): when set, the reader hands a response to its `Call` only after every notification read before it has been handled. `SessionLimits` sets it. The ACP spec requires the agent to send its updates before it answers `session/prompt`, so with this option a returned `Prompt` means the turn's output is complete.
- **Deadlines.** `Prompt` refuses a context without a deadline. The host gives it the session's remaining `max_duration`. `initialize`, `session/new` and `session/set_mode` use 30 s.
- **In flight.** The agent may have 16 requests waiting for agentflow (`MaxInFlight`). A held `terminal/wait_for_exit` or a permission request waiting for a person counts. One more gets jsonrpc's busy error.
- **Frames.** 8 MiB each way. File contents are limited further ("Phase 7 limits").
- **Denials** to `fs/*` and `terminal/*` are JSON-RPC errors `-32001` with a fixed message per verdict code and `data: {"code": "<code>"}`. A missing file is `-32002` (ACP's "resource not found"). No message echoes a path or the agent's own text.
- **Cancellation.** On `ai cancel` or a limit, the host sends `session/cancel`, answers every pending permission request `{"outcome":"cancelled"}` at once (the spec's MUST), answers every held `fs/*` or `terminal/create` with `-32800`, waits up to 10 s for `stopReason: cancelled`, then stops the process.
- **Never sent:** `authenticate`, `logout`, `session/load`, `session/resume`, `session/list`, `session/close`, `session/delete`, `session/set_config_option`, an `http` or `sse` MCP server, `systemPrompt`, `additionalDirectories`, `$/cancel_request`.

### Phase 7 fake ACP agent (lane 7.1)

- `acptest.Script` gains `Session *SessionScript`. A `SessionScript` is a list of steps the fake plays after `session/prompt`: `{"update": <SessionUpdate JSON>}`, `{"ask": {"permission" | "read" | "write" | "terminal" | "wait" | "release": ...}}` (the fake sends that client request and logs the answer or the error code), `{"tool_unasked": {...}}` (a `tool_call` that goes to `completed` with no request), `{"sleep": "100ms"}`, `{"stop": "end_turn"}`, `{"crash": true}`, `{"new_session_error": -32000}`. It logs every message it receives (`LogEntry`), including the `mcpServers` it was given, and never starts an MCP server.
- `cmd/fake-acp` gains `--mode=session-demo`, `--mode=session-nested`, `--mode=session-hang` and `--mode=session-auth`, the scripts the demo uses (below). It is built by hand only.
- A test covers every script with a real `jsonrpc.Launch` of the fake.

### Phase 7 session lifecycle (lane 7.2)

`aisession.State` and `aisession.Transitions` (GoF State):

```text
 ai run      launch ok      initialize ok    session/new ok      prompt answered
(none) ─► starting ─► initializing ─► creating ─► running ─────────────────────► ended
                                                    │  ▲                            ▲
                                       ask held     │  │ every ask answered         │
                                                    ▼  │                            │
                                                  waiting ── ai cancel, limit ─► cancelling
  any state ── launch, protocol, auth_required, exited, host_lost ──────────────────► ended
```

| From | Event | To | Code |
| --- | --- | --- | --- |
| none | `ai run` passed its gate; record written | `starting` | |
| `starting` | harness launched | `initializing` | |
| `starting` | launch failed | `ended` | `launch` |
| `initializing` | `initialize` answered with version 1 | `creating` | |
| `initializing` | other version, a protocol error, 30 s | `ended` | `protocol_version`, `protocol`, `timeout` |
| `creating` | `session/new` answered (then `set_mode` if configured) | `running` | |
| `creating` | `session/new` answered `-32000` | `ended` | `auth_required` (and `harness.Store.MarkAuthRequired`) |
| `running` | an ask is held for a person | `waiting` | |
| `waiting` | no ask is held | `running` | |
| `running`, `waiting` | `ai cancel`, a limit, an unasked tool call outside the worktree | `cancelling` | `cancelled`, `limit_duration`, `limit_idle`, `limit_output`, `unasked` |
| `running`, `waiting`, `cancelling` | `session/prompt` answered | `ended` | the stop reason |
| `cancelling` | no answer within 10 s | `ended` | the cancel code |
| any but `ended` | harness exited, connection closed | `ended` | `exited`, `protocol` |
| any but `ended` | host heartbeat older than 30 s | `ended` | `host_lost` (written by any reader, under the lock) |

- **One prompt turn per session** in phase 7 (decision 2). When the turn ends, the host stops the harness: close stdin, 2 s, kill the process group (the phase 5 rule).
- Every transition is written to `session.json` under the store lock and publishes `ai.session.changed`.
- `Session.Code` holds the stop reason (`end_turn`, `max_tokens`, `max_turn_requests`, `refusal`, `cancelled`) or the failure code.

### Phase 7 session host (lane 7.2)

A session is hosted by the process that starts it, never by the console daemon (decision 1):

| Started by | Host | Output |
| --- | --- | --- |
| A person: `ai run` in a terminal, without `--detach` | the `ai run` process itself (foreground) | streamed to the terminal; asks are prompted on the terminal; Ctrl-C cancels, a second Ctrl-C kills |
| `ai run --detach`, the CLI in an agent session, or `agentflow_ai_run` | a detached `agentflow ai host --session ID` process | none; follow with `ai status --follow` or the AI panel |

- **Detached host.** The starter writes the record (`starting`), then starts its own binary (`os.Executable`, links resolved) as `agentflow ai host --session ID` in a new session (Unix `Setsid`; Windows `DETACHED_PROCESS` and `CREATE_NEW_PROCESS_GROUP`), with stdin, stdout and stderr on `<session>/host.log`. Its environment is the starter's own; the host builds the harness environment from it by the rules below, as a foreground host would. `ai host` is runtime plumbing in `cli.go`'s fixed switch (like `mcp`), not a Spec, and not in the usage text. It claims the record under the store lock and exits at once when the record is not `starting` or is claimed.
- **Heartbeat.** The host writes `host.heartbeat` every 5 s. A reader that finds it older than 30 s ends the session `host_lost` under the lock. It kills the harness's process group only when it can show the group still belongs to that harness (Linux: process start time from `/proc`; macOS: `sysctl` `kern.proc.pid` start time); otherwise it leaves it, and `ai status` says so.
- **Launch.** `harness.Locate` (below) gives the `harness.Found` for the ID. The host launches it through `jsonrpc.Launch` with: `Path` the found file, `Root` `harness.LaunchRoot(...)`, `Dir` the worktree root, `AllowDir` the worktree root, `DataDir` the session directory, `Args` the definition's `args`, `Env` the session environment, `AllowEnv` the names that environment adds beyond `EnvAllowed`, `Stderr` `host.log`, `Limits` `acp.SessionLimits`, `Handler` `acp.NewClientHandler(host)`.
- **`LaunchSpec.AllowDir`** (lane 7.1 declares it, lane 7.2 implements its check in `launch.go`): an absolute existing directory that `Dir` may also be inside (links resolved). Empty means the old rule. Only the session profile sets it.
- **`harness.Locate`** (new file `internal/harness/session.go`, lane 7.2, added after issue #120 merges; no existing harness file changes): `Locate(ctx, home, version string, workDirs func() []string, id string) (Found, Harness, error)` resolves one definition with the same layers, PATH rules and probe cache as `Detect`, and never probes. It returns `ErrNotReady` unless the state is `native` or `adapter_installed` with a probe result `ok` (or `cached` `ok`) for the current identity, and reports `Harness.Probe` and the fix otherwise. The same file exports `GuardPath(path string) error`, the test guard of `guard.go`.

### Phase 7 package dependencies (lanes 7.2 and 7.3)

`internal/command` imports `internal/app`; `internal/webui`, `internal/command/ops` and `internal/command/deliver` import `internal/app` and `internal/plugin`; `internal/plugin` imports `internal/app` (checked with `go list -deps` on `develop` at `fdf0eca`). `internal/app` (lane 7.3) and `internal/webui` (lane 7.2) will import `internal/aisession`. So `aisession` calling any of them directly would be an import cycle.

- **`internal/aisession` may import only:** `internal/acp`, `internal/jsonrpc`, `internal/harness`, `internal/policy`, `internal/config`, `internal/boundary`, `internal/bus`, `internal/notifysock`, `internal/filelock`, `internal/gitx` and the standard library.
- **It must never import:** `internal/app`, `internal/command/**`, `internal/webui`, `internal/mcpserver`, `internal/plugin`, `internal/cli`. A test in lane 7.2 (`TestAISessionImports`) fails on any of them.
- **What it needs from those layers comes in through `aisession.Options`** (declared in lane 7.2's `types.go`):

```go
type Options struct {
    Home    string // the agentflow home
    Version string // agentflow's version, for clientInfo
    // Emit delivers one event (command/deliver: the store.Exists check, the
    // socket, the unknown_worktree retry). nil drops events.
    Emit func(ctx context.Context, e bus.Event) error
    // WorktreeID is webui.IdentifyWorktree's ID for dir.
    WorktreeID func(ctx context.Context, dir string) (string, error)
    // PluginDeny runs the registered Invoker stages (command.CheckStages)
    // for a synthetic operation "ai.request" with the request's gate class
    // and action and the session's branch; a non-nil error is a plugin deny.
    PluginDeny func(ctx context.Context, class, action, branch string) error
    // MergeBase is policy.NeverOptions.MergeBase, as the pre-bash hook wires it.
    MergeBase func(cmd string) (string, error)
    // Detach starts the detached host; nil means re-execute os.Executable.
    Detach func(ctx context.Context, id ID) error
    // MCPServer is the server entry for session/new, or nil for none.
    MCPServer func(id ID) *acp.MCPServer
    Now func() time.Time
}
```

- **Lane 7.3 wires them** in `internal/command/ops/ai_sessions.go` (`ops.AISessionOptions`, which may import `command`, `command/deliver` and `webui`; `deliver` does not import `ops`) and uses the same function in `internal/cli/ai_host.go`. `app.AIRun`, `app.AIStatus`, `app.AIRespond` and `app.AICancel` take the `Options` from their caller: `internal/app` never imports `internal/command`.
- The console's routes (lane 7.2, `internal/webui/ai.go`) answer and cancel through the `ai.respond` and `ai.cancel` Specs, by name through the daemon's Invoker, so they need no `Options` of their own. The `ai` projection reads the store with `aisession.OpenStore(home)`, which needs none.

### Phase 7 session environment (lane 7.2)

This is the environment the harness process gets. Open question 1 asks the owner to choose the `HOME` row; the table shows the recommended default.

| Variable | Value | Why |
| --- | --- | --- |
| `HOME` | the OS account's home (`os/user`, not `$HOME`), with `ai.sessions.home: real` (default). With `isolated`: `<session>/home`, empty | The harness needs its own sign-in, which lives under the real home (and in the OS keychain). A different `HOME` is not a security boundary: the process runs as the person, and OpenSSH reads the account home from the password database anyway. |
| `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `HOMEDRIVE`, `HOMEPATH` (Windows) | as `HOME`: inherited (real) or under `<session>/home` (isolated) | same |
| `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME` | inherited when set (real); unset (isolated) | the harness's configuration |
| The definition's `config_env` names (`CODEX_HOME`, `CLAUDE_CONFIG_DIR`) | inherited when set | a moved harness configuration |
| `USER`, `LOGNAME`, `LANG`, `LC_*`, `TZ`; the phase 5 Windows names | inherited when set | |
| `SHELL` | inherited when it is an absolute path that passes the phase 6 file checks and is not inside a repository | harnesses run their own shell tool with it |
| `TERM` | `dumb` | no terminal |
| `TMPDIR`, `TEMP`, `TMP` | `<session>/tmp` (0700, removed when the session ends) | |
| `PATH` | the harness's found directory, then each entry of the starter's `PATH` that passes the phase 6 PATH checks (`relative`, `missing`, `inside_repository` against every worktree of the session's repository and of every repository the daemon serves, `node_modules`, `unsafe_permissions`), then the fixed system PATH; duplicates removed | the harness and its tools need `go`, `npm`, `make`; a repository must never put a program in front of them |
| The definition's `credential_env` names | inherited when set, **only** for a harness listed in `personal:ai.sessions.pass_credentials` (default empty; open question 3) | people who sign in with an API key |
| `AGENTFLOW_ACP_SESSION` | the session ID | marks every process below the harness as inside a session (one layer, not a boundary) |
| `GIT_TERMINAL_PROMPT` | `0` | no credential prompts |

**Nothing else passes**, in particular: `GH_TOKEN`, `GITHUB_TOKEN`, every other name the phase 6 credential rules match (the credential suffixes, `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`, `CREDENTIAL`, `_KEY`), cloud keys, proxy variables (they can hold credentials; decision 11), `SSH_AUTH_SOCK`, `GPG_AGENT_INFO`, every `AGENTFLOW_*` and `DEVFLOW_*` of the parent, client session IDs (`CLAUDECODE`, `CLAUDE_CODE_*`, `CODEX_*` other than `CODEX_HOME`), and the loading and trust names of the phase 6 sign-in guidance list (`LD_*`, `DYLD_*`, `NODE_OPTIONS`, `PYTHONPATH` and the rest).

**Refusals before launch** (`ai run` fails with the code, nothing starts):

- `unsafe_environment`: the real `HOME`, a set `XDG_*_HOME` or a `config_env` directory is inside a git working tree or git directory (the phase 6 rule, `config.InsideRepository`; the only exception is a repository whose root is the account home).
- `home_unsafe`: the agentflow home fails the phase 6 home checks.
- `test_guard`: in a test binary, the harness file is outside the test's temporary directory.

**What the person's real home means.** With `real`, the harness's own tools can use what the person's home holds: their git credential helpers, `~/.config/gh`, cloud CLI profiles, SSH keys. agentflow cannot stop that (see "what agentflow controls"); it only keeps tokens out of the environment and checks what reaches it over ACP. With it, the person's own client settings also apply to the harness, for example their Claude Code hooks (including agentflow's pre-bash never-list). The `isolated` option trades that for a harness that may not be signed in. Lane 7.2 checks, from vendor documentation or source, whether `claude-acp` and `codex-acp` can sign in with `isolated` plus their `config_env` names, and reports; it does not guess.

**Terminal environment.** A process started for `terminal/create` gets the session environment **without** the `credential_env` values, plus the agent's `env` entries that pass "Phase 7 terminals".

### Phase 7 permission chain (lane 7.2)

Every agent request that asks agentflow to do or allow something becomes an `aisession.Request` and goes through one chain:

```go
package aisession

// Kind is what the agent asked for.
type Kind string

const (
    KindPermission Kind = "permission" // session/request_permission
    KindRead       Kind = "fs_read"    // fs/read_text_file
    KindWrite      Kind = "fs_write"   // fs/write_text_file
    KindTerminal   Kind = "terminal"   // terminal/create
)

// Request is one agent request, as agentflow resolved it. Title, ToolKind,
// ToolCallID and Options are the agent's words (sanitized, display only);
// everything else agentflow worked out itself.
type Request struct {
    Session    ID
    Kind       Kind
    ToolCallID string
    ToolKind   acp.ToolKind
    Title      string
    Options    []acp.PermissionOption
    // Paths: fs path, or the tool call's locations, resolved by the boundary.
    Paths   []Path
    Command *Command // KindTerminal
    Size    int64    // KindWrite: content bytes
    // Set by the never-list: only a person may allow this.
    PersonOnly bool
    // Reason is why it would ask: control_file, secret_file, terminal,
    // repository_program, no_locations, kind, rule, gate, ceiling.
    Reason string
}

type Path struct {
    Abs   string // cleaned absolute path as asked
    Rel   string // slash path relative to the worktree root; "" when outside
    Real  string // resolved, when it exists
    Exists bool
}

type Command struct {
    Program  string   // as asked
    Resolved string   // absolute, after lookup on the session PATH
    Args     []string
    Cwd      Path
    EnvNames []string // the agent's env names; values are never shown or stored
    Line     string   // POSIX-quoted Program and Args, for the never-list and the boundary
}

type Decision string

const (
    Allow Decision = "allow"
    Deny  Decision = "deny"
    Ask   Decision = "ask" // hold it for a person
)

// Verdict names who decided and why.
type Verdict struct {
    Decision Decision
    Handler  string // never, boundary, rule, gate, default, person, timeout, cancel
    Code     string // e.g. outside_worktree, git_dir, never_list, rule:<name>, gate_never
}

type Next func(ctx context.Context, r *Request) Verdict

// PermissionHandler is one link of the chain. It returns Deny, or sets
// fields on r and returns next(ctx, r). Only the rule and gate links may
// return Allow or Ask.
type PermissionHandler interface {
    Decide(ctx context.Context, r *Request, next Next) Verdict
}
```

**The links, in order** (`aisession.DefaultChain`):

1. **Never-list.** Denies what no one may allow, and marks what only a person may allow (`PersonOnly`). For terminals it runs `policy.ForbiddenWith` on `Command.Line` with the repository's protected branches, the current branch and `NeverOptions{CoordinatorMerge}` from configuration; then the session never-list ("Phase 7 terminals"). For files it marks control files and secret files (below).
2. **Repository boundary.** Resolves every path and the terminal's `cwd` against the worktree ("Phase 7 file access") and denies anything outside it, into `.git` or into a nested repository. For terminals it also runs `boundary.Checker` with the session's repository as home, so a command that writes another repository is denied, as the pre-bash hook does.
3. **Policy rule.** The first matching rule decides: the repository's rules first (they may only `ask` or `deny`), then the person's `personal:ai.permissions.rules` (`allow`, `ask` or `deny`), then the enabled plugins' `permission_rule`s (`deny` only; phase 5). No match passes on.
4. **`policy.Gate`.** `Gate.Explain(class, action, nil, false)` for the action in the table below: `auto` gives allow, `confirm` gives ask, `suggest` and `never` give deny. For a session an agent started, every action except `ai.fs.read` is capped at `confirm` (the ceiling, `Gate.WithCeiling`).
5. **Default.** Deny (`Handler: default`).

**Combining.** Any deny wins. Otherwise `PersonOnly`, the ceiling or a rule `ask` gives ask. Otherwise a person's rule `allow` gives allow (it is the person's own standing answer, and adding one is a loosening edit). Otherwise the gate's answer. A rule can never allow what the never-list marked `PersonOnly`, and no rule from a repository or a plugin can allow anything.

**Per request kind:**

| Request | Paths checked | Gate action (class) | Asks a person when |
| --- | --- | --- | --- |
| `fs/read_text_file` | `path` | `ai.fs.read` (read) | a secret file (below); a rule says ask |
| `fs/write_text_file` | `path` | `ai.fs.write` (local) | a control file (always); the gate says confirm; the ceiling |
| `terminal/create` | `cwd` (default: the worktree root), the resolved program | `ai.terminal` (local, mandatory confirmation) | always, unless a person's rule allows the program; a shell, a wrapper or a program inside the worktree always (`PersonOnly`) |
| `session/request_permission`, tool kind `read`, `search` | `toolCall.locations` | `ai.fs.read` | no locations (`no_locations`) |
| tool kind `think` | | `ai.fs.read` | |
| tool kind `edit`, `delete`, `move` | `toolCall.locations` (required) | `ai.fs.write` | no locations; a control file; the gate says confirm |
| tool kind `execute` | `locations` when given | `ai.terminal` | always (`PersonOnly`): the command is in `rawInput`, whose shape is the agent's own, so no rule can match it reliably |
| tool kind `fetch` | | `ai.fetch` (remote, mandatory confirmation) | always, unless a person's rule allows `tool` `fetch` |
| tool kind `switch_mode`, `other`, unknown | | `ai.permission` (local, mandatory confirmation) | always (`PersonOnly`) |

- A `sessionId` in a request that is not this session's ACP session ID is denied `wrong_session`.
- An approved `session/request_permission` never authorizes a later `fs/*` or `terminal/*` call: each one runs the chain on its own. The tool call's title, content and options are the agent's words.
- **Ask.** The host holds the request, writes the ask to the store, publishes `ai.session.asked`, and waits up to `ask_timeout` (5 min). No answer: deny (`Handler: timeout`). At most 4 asks may wait per session (`too_many_asks`), and at most 30 asks a minute (`rate`): more are denied.
- **Answering `session/request_permission`.** agentflow chooses by `kind`, never by `name`. Allow selects the first option with kind `allow_once`; with none, it cannot allow, and the ask shows only Reject. Deny selects `reject_once`, else `reject_always`, else answers `{"outcome":"cancelled"}`. agentflow never selects `allow_always`: the agent stores remembered decisions with a scope agentflow cannot see or bound (decision 5).
- **Answering `fs/*` and `terminal/create`.** Allow: agentflow does it and answers with the result. Deny: `-32001` with the verdict code.
- **Audit.** Every resolved request writes one `Gate.Audit` line: session, kind, tool kind, the worktree-relative path or the program name (clamped to 256 bytes), decision, handler, code and who answered. Never file contents, arguments beyond the program name, or env values.

### Phase 7 file access (lane 7.2)

All file work goes through `os.Root` (standard library since Go 1.24; `go.mod` is 1.26), opened once per session at the worktree root's resolved path. Its documented behaviour: methods "can only access files and directories beneath a root directory", "will follow symbolic links, but symbolic links may not reference a location outside the root", and do not prevent crossing file system boundaries, bind mounts, `/proc` files or device files.

For each path, in order (first failure decides; codes in brackets):

1. Absolute and clean (`relative`). `ReadTextFileRequest.line` and `limit` are used as given.
2. Lexically inside the worktree root, compared by path component after resolving the root itself (`outside_worktree`). On macOS and Windows the comparison ignores case.
3. No component equal to `.git`, ignoring case (`git_dir`). The `.git` file of a linked worktree and the common git directory are never readable or writable.
4. Windows: no component with `:` (alternate data streams), a trailing dot or space, or a reserved device name (`name`).
5. No directory on the way below the root holds a `.git` entry (`nested_repository`): a submodule or a nested clone is another repository.
6. The operation runs through the `os.Root`, which refuses a link that leaves the root at the moment of use (`outside_worktree`), so a link swapped in after the checks above still cannot escape.
7. Reads: a regular file (`not_regular`), at most 4 MiB (`too_large`), valid UTF-8 (`not_text`). Writes: content at most 4 MiB (`too_large`); the final component is not a symbolic link (`link`); an existing file has one link only, on Unix (`hardlink`: a hard link could point at a file outside the worktree); parents are created inside the root (0755), the file keeps its mode or gets 0644, and the content is written to a temporary file in the same directory, then renamed through the root.

**Control files** (writes always ask a person, `PersonOnly`, reason `control_file`; `aisession.ControlFiles`), matched on the worktree-relative path, ignoring case on macOS and Windows: `.agentflow/**`, `.claude/**`, `.mcp.json`, `.codex/**`, `.gemini/**`, `.cursor/**`, `.vscode/**`, `.idea/**`, `.githooks/**`, `.husky/**`, `.github/workflows/**`, `.github/actions/**`, `.envrc`, `.pre-commit-config.yaml`, `lefthook.yml`, and `AGENTS.md`, `CLAUDE.md`, `GEMINI.md` at any depth. These are the files that control execution or approval, which the ACP file-system page says a client should ask about.

**Secret files** (reads always ask, `PersonOnly`, reason `secret_file`; `aisession.SecretFiles`): `.env` and `.env.*` at any depth, `*.pem`, `*.key`, `*.p12`, `*.pfx`, `id_rsa*`, `id_ed25519*`, `.npmrc`, `.pypirc`, `.netrc`. Reading them would send their content to the model's provider.

A personal rule can change neither list. A repository rule can add to the ask list (`repo:ai.permissions.ask`).

### Phase 7 terminals (lane 7.2)

- **No shell.** agentflow starts `Command.Resolved` with `Args` directly, in its own process group (Unix `Setpgid`), with the terminal environment. A bare `command` is looked up on the session PATH; a path with a separator is resolved against `cwd`. A program inside the worktree (`./gradlew`, `bin/tool`) is repository code: `PersonOnly`, reason `repository_program`.
- **Shells and wrappers** (`sh`, `bash`, `zsh`, `fish`, `dash`, `ksh`, `cmd`, `powershell`, `pwsh`, `env`, `xargs`, `nohup`, `timeout`, `nice`, `time`, `script`, interpreters with `-c` or `-e`): the never-list and the boundary still read the command inside them (`boundary.SimpleCommands`, which is a heuristic), and only a person may allow them (`PersonOnly`). `sudo`, `doas`, `su`, `pkexec` are denied (`privilege`).
- **The session never-list** (`aisession.NeverList`, in addition to `policy.ForbiddenWith`): any `agentflow` invocation except the read-only commands `context`, `scope`, `ac`, `lint-msg`, `docs search`, `docs get`, `policy explain`, `version` and `ai harnesses` without `--probe` is denied (`agentflow_in_session`). This covers `ai run`, `ai respond`, `ai cancel`, `ai host`, `delegate`, `promote`, `plugin`, `config`, `policy approve`, `approval`, `mcp`, `ui`, `update`, `install`, `login` and every write. It finds `agentflow` the way `policy.changesPlugins` does (by program name, the plugin launcher, a cached build, `go run`).
- **Agent `env`.** At most 32 entries, each value at most 4 KiB with no NUL. A name the phase 6 rules refuse for `config_env` (loading and trust names, credential-like names, `AGENTFLOW_*`, `DEVFLOW_*`), or `PATH`, `HOME`, `SHELL`, `TMPDIR`, `GIT_CONFIG*`, `GIT_SSH*`, `GIT_EXEC_PATH`, `GIT_DIR`, `GIT_WORK_TREE`, `GIT_ASKPASS`, `SSH_ASKPASS`, denies the whole request (`env`). Rules match the full line and the env names, as the ACP terminal page asks.
- **Output.** stdout and stderr together, kept up to the agent's `outputByteLimit` (default 256 KiB, at most 1 MiB), truncated from the start at a character boundary (the spec's rule). Terminal control sequences are removed before the output reaches the transcript, the projection or a person's terminal; the agent gets the bytes it would get from a pipe.
- **Lifetime.** At most 4 terminals at once and 64 per session (`too_many_terminals`). `terminal/kill` and `terminal/release` kill the process group; release frees the ID. When the session ends, every terminal's group is killed. Windows kills only the child (phase 5 decision 9; open question 12).

### Phase 7 tool calls the harness did not ask about (lane 7.2)

The host follows every `tool_call` and `tool_call_update`. A tool call is **unasked** when it reaches `in_progress`, `completed` or `failed` with kind `edit`, `delete`, `move`, `execute` or `fetch`, no `session/request_permission` named its `toolCallId`, none of its content is a terminal agentflow started, and the chain would not have allowed the equivalent request (built from its kind and locations).

- It is always recorded: an `unasked` item in the transcript, the counter, and the tool call marked on the panel.
- When any of its locations is outside the worktree, in `.git`, or a control file, the host cancels the session (`unasked`), whatever the setting.
- Otherwise `personal:ai.sessions.unasked` decides: `warn` (default; open question 6) or `cancel`.
- A harness can also act without reporting a tool call at all. agentflow cannot see that. So when a session ends, the host records the worktree's changed paths (`git status --porcelain=v1 -z`, at most 200, worktree-relative) in `session.json` as `changed_files`, and `ai status` and the panel show them.

### Phase 7 who may start a session and who may answer

**The requester is set by the calling surface, never by an input field** (phase 6 rule): the console's handlers pass `person`; the CLI passes `person` only with a terminal on stdin, `app.AgentEnvironment() == ""` and no `AGENTFLOW_ACP_SESSION`; every MCP call passes `agent`; a session's own MCP server passes `session`. `aisession.WithRequester(ctx, Requester)` carries it.

```go
type Requester struct {
    Kind    string // person, agent, session
    Surface string // terminal, console, cli, mcp
    Client  string // the app client: "cli", "claude", "ai-session:<id>"
    Session ID     // the caller's own session when Kind is session, else ""
}
```

**Starting a session:**

| Who | How | Allowed | Confirmation | Then |
| --- | --- | --- | --- | --- |
| A person | `ai run` in a terminal | yes | always: the full summary, y/N (`--yes` accepted) | the person's own policy for the session's requests |
| An agent | `agentflow_ai_run`, or `ai run` in an agent session | only when `personal:ai.sessions.agent_start` is on (default off; open question 4) | always `needs_confirmation`, under every autonomy level (`RequireConfirmation`) | detached; the `confirm` ceiling: every request except reads inside the worktree asks a person |
| A session | its MCP server, or `agentflow` in its terminal | never (`nested`): the session MCP server has no `ai_run` tool, the session never-list denies it in terminals, and `ai.run` refuses a requester of kind `session` or a process with `AGENTFLOW_ACP_SESSION` | | |
| A plugin | | never: no `ai.*` command is in `manifest.InvocableCommands`, and no `ai.**` type is in `manifest.SubscribableFamilies` | | |
| The console or the daemon | | never in phase 7 (open question 10) | | |

The running-session cap ("Phase 7 limits") is counted in the store under its lock, so it holds whoever asks and whatever a process says about itself.

**Answering an ask** (`aisession.Answer`, the one place these rules live):

| Who | Allow once | Reject | Cancel the session |
| --- | --- | --- | --- |
| A person in the console (AI panel) | yes | yes | yes |
| The person at the foreground `ai run` terminal | yes | yes | yes (Ctrl-C) |
| A person: `ai respond --allow` in a terminal | yes, under the person-only rules below | yes | `ai cancel` |
| An agent: `agentflow_ai_respond`, or `ai respond` in an agent session | **no** (`needs_person`; open question 5) | yes | yes |
| The session itself (its MCP server, its terminals, `AGENTFLOW_ACP_SESSION`) | no (`self_answer`) | no (`self_answer`) | no |
| A plugin, the daemon on its own | no | no | no |
| Policy (rules, the gate) | yes, in the chain, before anyone is asked | yes | limits only |

**Why an agent cannot allow its own session's requests**, layer by layer. No single layer is relied on:

1. No MCP tool can allow. `agentflow_ai_respond` accepts only `reject`.
2. The session's own MCP server is opened with the reserved client prefix `ai-session:` (lane 7.3, `policy.SessionClientPrefix`): `policy.NewGate` and `app.Open` refuse it, so only `app.OpenSession` makes such a client, and `aisession.Answer` refuses any requester of kind `session` and any `ai-session:` client.
3. `ai respond --allow` follows the `plugin approve` rules (phase 5): it refuses agent environments (`app.AgentEnvironment`, which lane 7.3 extends to `AGENTFLOW_ACP_SESSION`), refuses a non-terminal stdin, ignores `--yes`, shows the whole ask and asks y/N.
4. The pre-bash never-list (`policy.ForbiddenAlways`, lane 7.3) blocks agents from running `agentflow ai respond ... --allow` in every mode, `--convention-only` included.
5. The session never-list denies every `agentflow ai` command in the session's terminals.
6. An allow answer must carry `HMAC-SHA256(answer.key, fingerprint)`, where `answer.key` is a random 32-byte key in the session directory (0600) and `fingerprint` is the SHA-256 of the canonical ask. Writing an answer file is not enough.
7. Every answer is bound to one ask's fingerprint and expiry, the first answer wins (`O_EXCL`), allow means `allow_once`, and every answer is audited and shown on the panel with who gave it.

**The last layer is a same-user limit.** A process that deliberately reads `answer.key` and computes the HMAC, or drives a pseudo-terminal to pass the person check, can forge a person's allow. agentflow cannot distinguish processes of the same user. These layers stop agent use and prompt-injected attempts; they do not stop a deliberately hostile binary, which ACP itself places out of scope.

### Phase 7 what a repository can influence

**Through agentflow:** only tightening, in the repository layer.

| Key | Effect | Loosens when |
| --- | --- | --- |
| `repo:ai.sessions.disabled` (toggle) | `ai run` refuses in this repository (`repo_disabled`) | turned off (`shrink`) |
| `repo:ai.permissions.ask` (chips, worktree-relative globs) | requests on these paths always ask a person | an entry is removed (`shrink`) |
| `repo:ai.permissions.deny` (chips, worktree-relative globs) | requests on these paths are denied | an entry is removed (`shrink`) |

A repository cannot name a harness, change a harness's command or arguments, add environment variables, add MCP servers, set a prompt or a mode, allow anything, raise a limit, or start a session. PATH entries inside its worktrees are dropped from the session PATH, and a `HOME` or configuration directory inside it stops the start (`unsafe_environment`). Phase 6 accepted and ignored a repository `ai:` key; phase 7 reads exactly the three keys above from it and still ignores the rest.

**Through the harness itself:** the harness starts in the worktree, so it loads that repository's own agent configuration, as it would if the person started it there: `CLAUDE.md` and `AGENTS.md`, `.claude/settings.json` (hooks and permission rules), `.mcp.json` (MCP servers), `.codex/`, `.gemini/`, `.cursor/`. A hostile repository can therefore make the harness run code when the session starts, before any request reaches agentflow. agentflow does not read, approve or pass any of it. What it does: `ai run`'s confirmation lists every control file that differs from the merge base with the repository's default branch, or is untracked (open question 7 asks whether to refuse instead).

**Through the content it holds:** repository text reaches the model through the harness's own reads. agentflow adds no repository text to the prompt in phase 7.

### Phase 7 limits

| Limit | Value | Key |
| --- | --- | --- |
| Running sessions per user | 2 (1 to 4) | `personal:ai.sessions.max_running` |
| Running sessions per worktree | 1 | |
| Starts per user | 10 an hour | |
| Nesting | none: a session cannot start a session | |
| Turn time | 30 min (1 min to 4 h) | `personal:ai.sessions.max_duration` |
| Idle (nothing from the agent, no ask waiting) | 10 min (1 min to 1 h) | `personal:ai.sessions.idle_timeout` |
| Ask waits for a person | 5 min (30 s to 30 min), then deny | `personal:ai.sessions.ask_timeout` |
| Asks waiting per session | 4 | |
| Asks per session | 30 a minute | |
| Agent requests in flight | 16 | |
| Frame | 8 MiB | |
| File read, file write | 4 MiB each | |
| Terminals | 4 at once, 64 per session | |
| Terminal output kept | the agent's limit, default 256 KiB, at most 1 MiB | |
| Transcript | 32 MiB per session; past it, text is dropped (`transcript_truncated`) and counting goes on | |
| `initialize`, `session/new`, `set_mode` | 30 s each | |
| Cancel | 10 s for the `cancelled` stop reason, then stop | |
| Stop | close stdin, 2 s, kill the process group | |
| Host heartbeat | every 5 s; lost after 30 s | |

- **The caps are counted in the store** (`<home>/ai/sessions.lock`, `internal/filelock`): `ai run` lists the records that are not `ended` and whose heartbeat is fresh, and refuses past `max_running` (`limit_running`), past one per worktree (`limit_worktree`) or past 10 starts in the last hour (`limit_rate`). The check and the new record are written under one lock.
- **Cost** (`usage_update.cost`) is agent-reported. agentflow shows it and never enforces on it alone (decision 10). The time limits are what bound spending. Open question 8 asks about a cost cap.
- Memory and CPU are not limited (phase 5 decision 9).

### Phase 7 session store and retention (lane 7.2)

```text
<home>/ai/                          0700
  sessions.lock                     the store lock
  sessions/<id>/                    0700  one session; <id> is "ais_" and 32 hex characters
    session.json                    0600  aisession.Session, replaced atomically under the lock
    transcript.jsonl                0600  aisession.Item lines, appended by the host only
    asks/<ask>.json                 0600  the held request, its fingerprint and expiry
    asks/<ask>.answer.json          0600  the first answer, created with O_EXCL
    answer.key                      0600  32 random bytes
    host.log                        0600  the detached host's and the harness's stderr; 1 MiB, one old file
    tmp/                            0700  TMPDIR; removed when the session ends
    home/                           0700  only with ai.sessions.home: isolated
```

**What is stored.** The transcript holds the prompt, the agent's messages and thoughts, tool calls with their content (diffs included), plans, usage, mode changes, asks and answers, terminal command lines and the last 64 KiB of each terminal's output, and the unasked and limit records. Before anything is stored, text is sanitized (control and bidirectional characters removed, terminal sequences neutralized) and passed through a best-effort redaction of known token shapes (`gh[pousr]_…`, `github_pat_…`, `sk-…`, `sk-ant-…`, `AKIA…`, `xox[abprs]-…`, PEM private key blocks) to `[redacted]`. Redaction is a help, not a guarantee: **transcripts may hold secrets**, and the store's modes and retention are what protect them.

**What is never stored:** environment values, the MCP server's env, the content of a file agentflow read for the agent (only its path and size), the content agentflow wrote (only path, size and SHA-256; the diff the agent reported is in its tool call), `answer.key` outside its file, and anything in the repository's event store or `.git`.

**Retention** (pruned by every `ai run` and by the host when it ends): a session that ended more than `personal:ai.sessions.keep_days` ago (7, 1 to 90) is deleted; at most 50 ended sessions and 200 MiB are kept, oldest first; a running session is never deleted. With `personal:ai.sessions.keep_transcripts` off, the transcript, asks, `host.log` and `answer.key` are deleted when the session ends and only `session.json` stays. Open question 8 asks the owner to confirm the defaults.

```go
// Session is session.json.
type Session struct {
    ID          ID         `json:"id"`
    Harness     string     `json:"harness"`
    HarnessName string     `json:"harness_name"`
    Identity    string     `json:"identity"` // harness.Found.Identity at start
    Repo        string     `json:"repo"`     // bus repository ID
    WorktreeID  string     `json:"worktree_id"`
    Worktree    string     `json:"worktree"` // absolute; shown with "~"
    Branch      string     `json:"branch,omitempty"`
    State       State      `json:"state"`
    Code        string     `json:"code,omitempty"`
    StartedBy   Requester  `json:"started_by"`
    Ceiling     string     `json:"ceiling,omitempty"` // "confirm" for an agent's session
    Home        string     `json:"home"`              // real, isolated
    EnvNames    []string   `json:"env_names"`         // names passed to the harness, never values
    MCP         bool       `json:"mcp"`               // agentflow's MCP server passed
    AgentSession string    `json:"agent_session,omitempty"` // the ACP sessionId, sanitized, 128 bytes
    Mode        string     `json:"mode,omitempty"`
    Host        HostInfo   `json:"host"`
    CreatedAt   time.Time  `json:"created_at"`
    EndedAt     *time.Time `json:"ended_at,omitempty"`
    Usage       *Usage     `json:"usage,omitempty"`
    Counters    Counters   `json:"counters"`
    ChangedFiles []string  `json:"changed_files,omitempty"`
}

type HostInfo struct {
    PID       int       `json:"pid"`
    Mode      string    `json:"mode"` // foreground, detached
    StartedAt time.Time `json:"started_at"`
    Heartbeat time.Time `json:"heartbeat"`
    HarnessPID int      `json:"harness_pid,omitempty"`
}

type Usage struct {
    Used, Size   uint64
    CostAmount   float64 `json:"cost_amount,omitempty"`
    CostCurrency string  `json:"cost_currency,omitempty"`
}

type Counters struct {
    Updates, ToolCalls, Asks, Allowed, Denied, Unasked, Terminals int
    BytesRead, BytesWritten, TranscriptBytes                        int64
}

// Item is one transcript line.
type Item struct {
    Seq      int64           `json:"seq"`
    Time     time.Time       `json:"time"`
    Kind     string          `json:"kind"` // prompt, message, thought, tool, plan, usage, mode, ask, answer, terminal, file, unasked, limit, info, unknown
    Text     string          `json:"text,omitempty"` // sanitized, at most 64 KiB
    ToolCall string          `json:"tool_call,omitempty"`
    Data     json.RawMessage `json:"data,omitempty"` // the kind's view, sanitized
}
```

The lifecycle constants (`State*`, end codes), `Transitions`, `Ask`, `Answer`, the `Store` and `Host` interfaces and `Manager` (start, status, answer, cancel, prune) are in `types.go` too. Lane 7.2 writes them from the rules in this section; their names follow the tables here.

### Phase 7 events (lane 7.2)

| Type | Scope | Global | Transient | Payload | Produced when |
| --- | --- | --- | --- | --- | --- |
| `ai.session.started` | worktree | never | no | `AISessionStarted{session, harness, started_by, client, ceiling}` | the record is written |
| `ai.session.changed` | worktree | never | no | `AISessionChanged{session, state, previous, code}` | every transition |
| `ai.session.output` | worktree | never | yes | `AISessionOutput{session, seq, items, kinds}` | new transcript items; at most 4 a second per session (coalesced) |
| `ai.session.asked` | worktree | never | no | `AISessionAsked{session, ask, kind, tool_kind, reason}` | an ask is held for a person |
| `ai.session.answered` | worktree | never | no | `AISessionAnswered{session, ask, decision, by, handler, code}` | an ask is answered, times out or is cancelled |
| `ai.session.ended` | worktree | never | no | `AISessionEnded{session, code, duration_ms, tool_calls, asks, allowed, denied, unasked}` | the session ends |

- They go to the session's repository store, tagged with its worktree, through `notifysock.Deliver` (the phase 3 rule for `op.*`: append while no daemon owns the store, send over the socket while one does, drop when no store exists). `ai.session.**` becomes a socket type (`notifysock.socketTypes`, lane 7.2). Source `cli`.
- **Payloads carry IDs, codes, counts and kinds only**: no prompt, message, title, path, command, option name, cost or agent text. The text stays in the session store in the agentflow home (0600) and reaches the console only through the projection's frames. So no session text enters a repository's event store.
- A socket type can be sent by any local process, so the projection never trusts a payload: it reads the session store and draws asks only from `asks/*.json` that the store's host wrote.
- Not audited by the Wire Tap (`bus.AuditedFamilies` stays empty): the host writes its own audit lines. Not op events, so no `TestEveryWriteOperationEmitsEvents` change for them.

### Phase 7 AI projection (lane 7.2)

| Projection | Key | Topics | Reads | State |
| --- | --- | --- | --- | --- |
| `ai` | repository | `<repo>/*/ai.session.**` | the session store of the daemon's agentflow home: the sessions whose `repo` is this repository | `AIState` |

`internal/webui/projection_ai_state.go`:

```go
const ProjectionAI = "ai"

// AIState is the "ai" state: this repository's sessions, newest first.
type AIState struct {
    Sessions []AISession `json:"sessions"` // at most 20
    Running  int         `json:"running"`  // across the user
    MaxRunning int       `json:"max_running"`
}

type AISession struct {
    ID          string     `json:"id"`
    Harness     string     `json:"harness"`
    HarnessName string     `json:"harness_name"`
    Worktree    string     `json:"worktree"` // "~/..."
    WorktreeID  string     `json:"worktree_id"`
    Branch      string     `json:"branch,omitempty"`
    State       string     `json:"state"`
    Code        string     `json:"code,omitempty"`
    StartedBy   string     `json:"started_by"` // person, agent
    Client      string     `json:"client,omitempty"`
    Ceiling     string     `json:"ceiling,omitempty"`
    Home        string     `json:"home"`
    CreatedAt   time.Time  `json:"created_at"`
    EndedAt     *time.Time `json:"ended_at,omitempty"`
    Prompt      string     `json:"prompt"` // at most 4 KiB
    Usage       *AIUsage   `json:"usage,omitempty"`
    Plan        []AIPlanEntry `json:"plan,omitempty"`
    Items       []AIItem   `json:"items"`     // the last 200 items or 256 KiB, oldest first
    Truncated   bool       `json:"truncated"` // older items exist
    ToolCalls   []AIToolCall `json:"tool_calls"` // the last 50
    Terminals   []AITerminal `json:"terminals,omitempty"`
    Asks        []AIAsk    `json:"asks,omitempty"` // waiting only
    ChangedFiles []string  `json:"changed_files,omitempty"`
    Counters    aisession.Counters `json:"counters"`
}

type AIUsage struct {
    Used         uint64  `json:"used"`
    Size         uint64  `json:"size"`
    CostAmount   float64 `json:"cost_amount,omitempty"`
    CostCurrency string  `json:"cost_currency,omitempty"`
    // CostReported is always the agent's own figure.
    CostReported bool    `json:"cost_reported"`
}

type AIPlanEntry struct {
    Content  string `json:"content"`
    Priority string `json:"priority"`
    Status   string `json:"status"`
}

type AIItem struct {
    Seq      int64     `json:"seq"`
    Time     time.Time `json:"time"`
    Kind     string    `json:"kind"` // as aisession.Item.Kind
    Text     string    `json:"text,omitempty"`
    ToolCall string    `json:"tool_call,omitempty"`
}

type AIToolCall struct {
    ID        string   `json:"id"`
    Title     string   `json:"title"` // the agent's words
    Kind      string   `json:"kind"`
    Status    string   `json:"status"`
    Locations []string `json:"locations,omitempty"` // worktree-relative, or "~/..." outside
    Diffs     []AIDiff `json:"diffs,omitempty"`
    Terminal  string   `json:"terminal,omitempty"`
    Asked     bool     `json:"asked"`
    Unasked   bool     `json:"unasked"`
}

type AIDiff struct {
    Path    string `json:"path"`
    OldText string `json:"old_text,omitempty"` // at most 64 KiB each
    NewText string `json:"new_text"`
    Cut     bool   `json:"cut,omitempty"`
}

type AITerminal struct {
    ID      string `json:"id"`
    Line    string `json:"line"` // POSIX-quoted command
    Cwd     string `json:"cwd"`  // worktree-relative
    Output  string `json:"output"` // the last 64 KiB, sequences removed
    Exit    *int   `json:"exit,omitempty"`
    Signal  string `json:"signal,omitempty"`
}

type AIAsk struct {
    ID          string   `json:"id"`
    Kind        string   `json:"kind"`
    ToolKind    string   `json:"tool_kind,omitempty"`
    Title       string   `json:"title,omitempty"` // the agent's words
    Paths       []string `json:"paths,omitempty"`
    Line        string   `json:"line,omitempty"`
    Cwd         string   `json:"cwd,omitempty"`
    EnvNames    []string `json:"env_names,omitempty"`
    Options     []AIOption `json:"options,omitempty"`
    Reason      string   `json:"reason"`
    CanAllow    bool     `json:"can_allow"`
    ExpiresAt   time.Time `json:"expires_at"`
    Fingerprint string   `json:"fingerprint"`
}

type AIOption struct {
    ID   string `json:"id"`
    Name string `json:"name"` // the agent's words
    Kind string `json:"kind"`
}
```

- Registered through `projectionExtensions` from `projection_ai.go`; the daemon side starts from a `runtimeExtension` in `stream_runtime_ai.go` (the phase 5 precedent: `stream_runtime.go` is not edited). In a test binary `newAIOptions` returns no store unless a test sets one.
- Paths under the OS home are shown as `~/...`. Every string is the store's sanitized text.
- **Requests from the page** (`internal/webui/ai.go`, under the console's session, origin and request-header checks; route lines in `server.go`):

| Route | Body | Does | Answer |
| --- | --- | --- | --- |
| `POST /api/ai/respond` | `{"session", "ask", "decision": "allow" \| "reject", "fingerprint"}` | invokes `ai.respond` through the daemon's Invoker with `Requester{person, console}`; at most 30 a minute per console | `202 {}` |
| `POST /api/ai/cancel` | `{"session"}` | invokes `ai.cancel` the same way | `202 {}` |

  The answers carry no session data; results arrive over the stream. The console computes the allow HMAC from the session's `answer.key`; the page never sees the key. Until lane 7.3 merges, lane 7.2's tests register fake Specs (phase 6 precedent).

### Phase 7 configuration keys (lane 7.2)

Lane 7.2 adds to `config.UserAI` (`internal/config/ai.go`) and adds `config.Repo.AI`. Every key is optional, so this is backwards compatible.

```go
type UserAI struct {
    Harnesses   UserHarnesses     `yaml:"harnesses,omitempty"`
    Sessions    UserAISessions    `yaml:"sessions,omitempty"`
    Permissions UserAIPermissions `yaml:"permissions,omitempty"`
}

type UserAISessions struct {
    AgentStart      *bool             `yaml:"agent_start,omitempty"`      // nil means off
    Home            string            `yaml:"home,omitempty"`             // real (default), isolated
    PassCredentials []string          `yaml:"pass_credentials,omitempty"` // harness IDs
    MaxRunning      *int              `yaml:"max_running,omitempty"`
    MaxDuration     *time.Duration    `yaml:"max_duration,omitempty"`
    IdleTimeout     *time.Duration    `yaml:"idle_timeout,omitempty"`
    AskTimeout      *time.Duration    `yaml:"ask_timeout,omitempty"`
    Unasked         string            `yaml:"unasked,omitempty"`          // warn (default), cancel
    AgentflowMCP    *bool             `yaml:"agentflow_mcp,omitempty"`    // nil means on
    KeepTranscripts *bool             `yaml:"keep_transcripts,omitempty"` // nil means on
    KeepDays        *int              `yaml:"keep_days,omitempty"`
    Modes           map[string]string `yaml:"modes,omitempty"`            // harness ID -> session mode ID
}

type UserAIPermissions struct {
    Rules map[string]AIRule `yaml:"rules,omitempty"`
}

// AIRule matches one request kind. Match is a worktree-relative slash glob
// for fs_read and fs_write, a program name glob (the resolved base name) for
// terminal, and a tool kind for tool.
type AIRule struct {
    Kind     string `yaml:"kind"`     // fs_read, fs_write, terminal, tool
    Match    string `yaml:"match"`
    Decision string `yaml:"decision"` // allow, ask, deny
}

type RepoAI struct {
    Sessions struct {
        Disabled bool `yaml:"disabled,omitempty"`
    } `yaml:"sessions,omitempty"`
    Permissions struct {
        Ask  []string `yaml:"ask,omitempty"`
        Deny []string `yaml:"deny,omitempty"`
    } `yaml:"permissions,omitempty"`
}
```

`config.Field` metadata in group `ai` ("AI harnesses" is renamed "AI harnesses and sessions"):

| ID | Widget | Default | Limits | Label | Applies | Loosens |
| --- | --- | --- | --- | --- | --- | --- |
| `personal:ai.sessions.agent_start` | `toggle` | false | | Let agents start sessions | `live` | `enable` |
| `personal:ai.sessions.home` | `select` real / isolated | real | | Session home folder | `next-run` | `enable` (isolated to real) |
| `personal:ai.sessions.pass_credentials` | `chips` | [] | 64 | Pass sign-in variables to these harnesses | `next-run` | `enable` (an ID added) |
| `personal:ai.sessions.max_running` | `stepper` | 2 | 1 to 4 | Sessions at once | `live` | `enable` (raised) |
| `personal:ai.sessions.max_duration` | `duration` | 30m | 1m to 4h | Session time limit | `next-run` | `enable` (raised) |
| `personal:ai.sessions.idle_timeout` | `duration` | 10m | 1m to 1h | Idle time limit | `next-run` | |
| `personal:ai.sessions.ask_timeout` | `duration` | 5m | 30s to 30m | Time to answer a request | `next-run` | |
| `personal:ai.sessions.unasked` | `select` warn / cancel | warn | | When the harness acts without asking | `next-run` | `enable` (cancel to warn) |
| `personal:ai.sessions.agentflow_mcp` | `toggle` | true | | Give the harness agentflow's read-only tools | `next-run` | `enable` |
| `personal:ai.sessions.keep_transcripts` | `toggle` | true | | Keep session transcripts | `live` | |
| `personal:ai.sessions.keep_days` | `stepper` | 7 | 1 to 90 | Days to keep sessions | `live` | |
| `personal:ai.sessions.modes` | `kv-table`, key = harness ID, one `text` column `mode` | {} | 64 | Session mode per harness | `next-run` | |
| `personal:ai.permissions.rules` | `kv-table`, key = rule name; columns `kind` (`select`), `match` (`text`), `decision` (`select`) | {} | 64 | Session permission rules | `live` | `enable` (an `allow` added, a `deny` or `ask` removed or changed) |
| `repo:ai.sessions.disabled` | `toggle` | false | | Turn sessions off in this repository | `live` | `shrink` |
| `repo:ai.permissions.ask` | `chips` | [] | 64 | Always ask for these paths | `live` | `shrink` |
| `repo:ai.permissions.deny` | `chips` | [] | 64 | Always deny these paths | `live` | `shrink` |

- Every label gets a one-sentence description in the table file (lane 7.2 writes them in the phase 6 style). The loosening cases are code in `internal/settings/checks.go` (phase 6 rule), compared on effective values.
- The save's check chain validates rules (`kind`, `decision`, a glob that compiles; an `allow` for `tool` with `execute`, `switch_mode` or `other` is an error: those are person-only) and modes.
- New autonomy actions (lane 7.2, `internal/config/autonomy.go`): `ai.run` (local), `ai.respond` (local), `ai.cancel` (local), `ai.fs.read` (read), `ai.fs.write` (local), `ai.terminal` (local), `ai.fetch` (remote), `ai.permission` (local). `ai.run`, `ai.terminal`, `ai.fetch` and `ai.permission` join the mandatory-confirmation list there (with `plugin.install`), so `auto` resolves to `confirm` for them: only a person's rule or a person's answer allows a terminal, a fetch or an unknown tool.
- `TestEveryConfigFieldHasControl` covers every field and column.

### Phase 7 agentflow's MCP server in a session (lane 7.3)

When `ai.sessions.agentflow_mcp` is on, `session/new` carries one MCP server:

```json
{"name": "agentflow", "command": "<absolute path of the agentflow binary>", "args": ["mcp", "--session", "<session ID>"], "env": []}
```

- The command is the host's own binary (`os.Executable`, links resolved). If it is inside the session's worktree (a development build there, which the agent could change), no MCP server is passed (`mcp_skipped`).
- `env` is empty: no value, credential or token is passed. The server inherits the harness's environment.
- `agentflow mcp --session ID` (additive to the `agentflow mcp --client NAME` contract) opens `app.OpenSession(ctx, dir, id)`: client `ai-session:<id>`, refused unless the session record exists, is not `ended`, and its worktree contains `dir`. It registers **only** these read-only tools, each also checked to be `Class: read` in the registry: `agentflow_context`, `agentflow_scope_check`, `agentflow_ac_checklist`, `agentflow_commit_validate`, `agentflow_commit_context`, `agentflow_docs_search`, `agentflow_docs_get`, `agentflow_policy_explain`. Nothing else, not `agentflow_ai_*` (open question 11).
- The `ai-session:` gate refuses every operation that is not a read (`session_read_only`), as a second layer behind the allowlist.
- This is not a boundary: the harness can start `agentflow` itself. The session never-list denies that in agentflow-run terminals; in the harness's own shell, `AGENTFLOW_ACP_SESSION` makes `app.Open` treat the process as a session client (lane 7.3), which refuses writes, `ai run` and answers. A harness that strips the variable falls back to an ordinary agent client: it still cannot allow an ask or start a session past the store cap, and its writes go through the gate as any agent's do.

### Phase 7 commands (lane 7.3)

Lane 7.3 adds these Specs in `internal/command/ops/ai_sessions.go`, calling `internal/app/ai_sessions.go`, which calls `aisession.Manager`. They are compatibility contracts from the release that ships them.

| Spec | CLI | MCP tool | Class, action | Gate | Events | Result |
| --- | --- | --- | --- | --- | --- | --- |
| `ai.run` | `ai run --harness ID [--detach] [--yes] [--json] PROMPT` (`-` reads stdin) | `agentflow_ai_run` | `local`, `ai.run` | `GateByInvoker`, `RequireConfirmation`, `AuditByInvoker` | worktree | `{session, state, harness, worktree, detached, next}` |
| `ai.status` | `ai status [SESSION] [--follow] [--since SEQ] [--json]` | `agentflow_ai_status` (read-only) | `read` | none | none | `{sessions}` or `{session, items, next_seq}` |
| `ai.respond` | `ai respond SESSION ASK (--allow \| --reject) [--json]` | `agentflow_ai_respond` | `local`, `ai.respond` | in Run; `--allow` always confirmed by a person (below) | worktree | `{session, ask, decision, state}` |
| `ai.cancel` | `ai cancel SESSION [--yes] [--json]` | `agentflow_ai_cancel` | `local`, `ai.cancel` | `GateByInvoker`, `AuditByInvoker` | worktree | `{session, state}` |

- **Tool inputs.** `agentflow_ai_run`: `{harness, prompt, confirm_token?}` (always detached; runs in the MCP server's worktree). `agentflow_ai_status`: `{session?, since?}`. `agentflow_ai_respond`: `{session, ask, decision}`, where `decision` must be `reject`. `agentflow_ai_cancel`: `{session, confirm_token?}`. No input names a requester, a worktree outside the server's, an environment, a limit or a ceiling.
- **`ai.run` checks, in order** (each a refusal code with fix text): requester of kind `session` or `AGENTFLOW_ACP_SESSION` set (`nested`); an agent while `agent_start` is off (`agent_start_off`, fix: the Settings toggle); `repo:ai.sessions.disabled` (`repo_disabled`); the primary checkout (`primary_checkout`, fix: `agentflow start <task> --worktree`; open question 9); `harness.Locate` not ready (`not_ready`, with the harness's fix); a plugin-chosen launch field and an agent requester (`plugin_launch`: phase 6 security amendment 1); the environment checks; the store caps. Then the confirmation.
- **The confirmation summary** lists: the harness ID, name, version, command, path (`~`), args and identity; the worktree (`~`) and branch; who starts it and the ceiling; the `HOME` kind and every variable **name** passed (never a value), with the credential names called out; whether agentflow's MCP server is passed and its tools; the limits; the control files that differ from the merge base (warning); and the prompt (first 4 KiB, as text). `Spec.Describe` gives the subject `ai run <harness>` and the branch: never the prompt, which would reach `op.*` events that plugins can read.
- **`ai run` output.** Foreground: the agent's messages as they arrive, thoughts dimmed, one line per tool call with its kind, status and locations, the plan when it changes, asks as prompts (`Allow once? [y/N]` with the timeout), and at the end the stop reason, counters and changed files. All agent text has terminal sequences neutralized. `--json` and detached runs print the result object only.
- **`ai status --follow`** reads `transcript.jsonl` from the store (no daemon needed) and prints like the foreground run, without prompts.
- **`ai respond --allow`** (CLI only) follows the person-only rules of "who may answer": agent environments and non-terminal stdin are refused, `--yes` is ignored, it shows the whole ask (kind, paths or command line, cwd, env names, the agent's title marked as the agent's words, the options) and asks y/N, then writes the answer with the HMAC. `--reject` and `agentflow_ai_respond` need no person.
- **Text output** of `ai status`: one line per session, `ID  HARNESS  STATE  CODE  AGE  WORKTREE`, then the waiting asks indented with their `ai respond` commands.
- `agentflow` gains no `ai host` usage line; `ai host` refuses to run without a `starting`, unclaimed record.
- The never-list (`policy.ForbiddenAlways`, lane 7.3) adds `agentflow ai respond ... --allow` for agents in every mode, found the way `changesPlugins` finds plugin decisions.
- `app.AgentEnvironment` also reports `AGENTFLOW_ACP_SESSION`.
- The plugin skill `adapters/claude-code/skills/ai/SKILL.md` gains the four commands; the `agentflow` skill lists the four tools for agents, saying that an agent can reject but never allow, and must ask the person to answer in the console.
- `TestEveryWriteOperationEmitsEvents` lists `ai.run`, `ai.respond` and `ai.cancel`. No lane edits `parity_test.go`: the registry derives the parity rows.
- The `ai` screen joins `agentflow_ui_open`'s screen list (`internal/webui/destination.go`, lane 7.3), so the tool golden file changes in one lane only.

### Phase 7 AI panel (lane 7.4)

- One page, `#ai`, from `AIState` only. The page sends only `POST /api/ai/respond` and `POST /api/ai/cancel`.
- A list of this repository's sessions (harness, state with its code, who started it, age, worktree and branch), and the selected session: the prompt; the stream of items (messages, thoughts collapsed, tool calls with kind, status, locations, diffs and terminal output); the plan; usage, with cost labelled "reported by the harness"; changed files when ended; and **Cancel** while it runs.
- **Permission cards** for waiting asks: the kind, the paths or the command line in full, `cwd`, env names, the reason it asks, the agent's title labelled as the agent's words, the time left, and only **Allow once** and **Reject**. **Allow once** is absent when `can_allow` is false.
- Every agent-authored string is text, never HTML, and never Markdown rendered as HTML. Links are shown as text; nothing opens without a click, and no link with a scheme other than `https` is made clickable. Terminal output arrives with sequences removed.
- Connection and resync states as on the other pages. Accessible in light and dark. A Fable design review before merge.
- No start button in phase 7 (open question 10): the empty state shows the `agentflow ai run` command to copy.

### Phase 7 test guard

No test may start a real harness or a real program a session asks for (plan guardrail).

- **Harness launch.** The host calls `harness.GuardPath` on the found file right before `jsonrpc.Launch`: in a test binary, a path outside `os.TempDir()` fails with `harness.ErrTestGuard` (code `test_guard`).
- **Terminals.** In a test binary, a terminal program whose resolved path is outside `os.TempDir()` is denied `test_guard`. Tests use `harnesstest.Bin` fakes.
- **Detached hosts.** In a test binary, `aisession` never re-executes the binary: `Options.Detach` must be set by the test (`aisessiontest.InProcessDetach`), else `ai run --detach` fails `test_guard`. No test leaves a process running.
- **The MCP server entry.** In tests its command is the test binary; the fake agent records it and never starts it.
- **`aisessiontest.Home(tb)`** sets `AGENTFLOW_HOME`, `HOME` and `PATH` (through `harnesstest.PATH`) to temporary directories and `CONTINUOUS_INTEGRATION=1`, and writes a `config.yml` with `auto_probe` and `sign_in_check` off (phase 6 rule).
- **`make e2e` (lane 7.3).** `scripts/e2e-claude.sh` calls `agentflow_ai_status` (expects no sessions) and `agentflow_ai_run` for `claude-acp` with no token (expects `agent_start_off`, since `agent_start` defaults off), then asserts that `$AGENTFLOW_HOME/ai/sessions/` does not exist and that the console registry check of phase 6 still passes. A run that breaks this fails.
- Tests `TestSessionGuardRefusesRealPATH`, `TestSessionEnvironment` (the parent sets `GH_TOKEN`, `GITHUB_TOKEN`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `AWS_SECRET_ACCESS_KEY`, `SSH_AUTH_SOCK`, `CLAUDE_CODE_SESSION_ID` and `AGENTFLOW_HOME`; only the listed names reach the fake agent and its terminals) and `TestChainDeniesByDefault` (lane 7.2) are part of the gate.

### Phase 7 demo and gate

**The daemon collision.** The console daemon's registry is the account's `~/.agentflow-ui`, whatever `AGENTFLOW_HOME` says (phase 6 security amendment 5). A live `agentflow ui` during the demo would reach the owner's running console, with the owner's real settings and harness definitions. So the demo never starts the console: sessions are hosted by the CLI and `ai host` processes, which need no daemon; `CONTINUOUS_INTEGRATION=1` suppresses the automatic console; the repository is a fresh scratch repository the owner's daemon does not serve, so a stray socket event is refused as `unknown_repo`; and the panel is shown by Go tests.

Runnable with the fake agent only, on a `make build` of `develop`, from an agent session (as the coordinator runs it):

```bash
AF="$PWD/agentflow"
T=$(mktemp -d)
go build -o "$T/bin/fake-acp" ./internal/acp/acptest/cmd/fake-acp
export CONTINUOUS_INTEGRATION=1 AGENTFLOW_HOME="$T/home" PATH="$T/bin:/usr/bin:/bin"
git init -q "$T/repo" && git -C "$T/repo" commit -q --allow-empty -m init
git -C "$T/repo" worktree add -q "$T/wt" -b demo
printf 'hello\n' > "$T/wt/notes.txt"
"$AF" config set --yes \
  'ai.harnesses.overrides={"demo":{"name":"Demo","command":"fake-acp","args":["--mode=session-demo"]},
                           "nested":{"name":"Nested","command":"fake-acp","args":["--mode=session-nested"]},
                           "slow":{"name":"Slow","command":"fake-acp","args":["--mode=session-hang"]}}' \
  ai.sessions.agent_start=true ai.sessions.ask_timeout=30s ai.sessions.max_running=1 \
  'ai.permissions.rules={"src":{"kind":"fs_write","match":"src/**","decision":"allow"}}'
"$AF" ai harnesses --probe --id demo --id nested --id slow
cd "$T/wt"
"$AF" ai run --harness demo --yes "Add a greeting"   # agent session: runs detached, prints the session ID
"$AF" ai status --follow <ID>
```

The fake's `session-*` modes answer the probe's plain `initialize` like `--mode=ok`, so the probe step marks all three ready. The `session-demo` script plays: message chunks; a plan; `fs/read_text_file <worktree>/notes.txt` (allowed by the gate: reads inside the worktree are not capped by the ceiling, so this is the step where policy decides alone); a permission request for an `edit` of `src/hello.txt` and `fs/write_text_file src/hello.txt` (the rule would allow both, but an agent started this session, so the ceiling makes them ask: step 2); `fs/read_text_file /etc/hosts` (denied, `outside_worktree`); `fs/write_text_file .claude/settings.json` (asks, `control_file`); `terminal/create echo hi` (asks, `terminal`); an `execute` tool call with no request (`unasked`, warned); a usage update with a cost; `end_turn`.

1. The session streams in `ai status --follow`. The fake logs the content of `notes.txt` (allowed, handler `gate`). `/etc/hosts` is never read: the fake logs `-32001` with `outside_worktree`.
2. Because an agent started it, the writes to `src/hello.txt` ask instead of following the rule (the ceiling). `ai status <ID> --json` lists the asks. `ai respond <ID> <ASK> --allow` from the agent session is refused `needs_person`. `ai respond <ID> <ASK> --reject` works. The unanswered ask is denied after 30 s (`timeout`). The session ends `end_turn` with `src/hello.txt` absent, `.claude/settings.json` absent, and the counters and the unasked tool call shown.
3. With `slow` running (`--mode=session-hang`), a second `ai run --harness demo` is refused `limit_running`; `ai cancel <slow ID> --yes` ends it `cancelled`, with its waiting ask answered `cancelled`.
4. `ai run --harness nested` plays a terminal request for `agentflow ai run --harness demo x` and one for `agentflow ai respond ... --allow`: both are denied `agentflow_in_session`.
5. `ai run` in `$T/repo` (the primary checkout) is refused `primary_checkout`.
6. Last, `"$AF" config set --yes ai.sessions.agent_start=false`, then `ai run --harness demo x` in `$T/wt` is refused `agent_start_off` and nothing starts.
7. The panel: `TestAIPanelFollowsSession` (lane 7.2: a daemon runtime with a fake store and the fake agent shows the session, its asks and its end in the `ai` projection, and an allow from the console route reaches the host) and the AI panel browser test (lane 7.4). Not a live console, for the reason above.
8. After the demo, `~/.agentflow-ui` lists no path under `$T`, and `$T/home/ai/sessions/<ID>/` holds `session.json` and `transcript.jsonl` with mode 0600 in 0700 directories.

**Person-only steps** (the owner may run them on their own machine; the coordinator does not): a foreground `ai run` in a real terminal shows `Allow once? [y/N]` for the `src/hello.txt` write and the rule allows it without asking; the console's AI panel shows the same session and its Allow once button answers an ask.

**Gate:** phase 6's gate first (`develop-p6`, after issue #120); then all four lanes merged; `make check` green on `develop` (including `TestEveryMCPToolHasCLICommand`, `TestEveryConfigFieldHasControl`, `TestEveryWriteOperationEmitsEvents`, `TestSessionGuardRefusesRealPATH`, `TestSessionEnvironment`, `TestChainDeniesByDefault`, `TestAIPanelFollowsSession`); `make e2e` passes (new MCP tools); the demo works; the security review below is done; tag `develop-p7`; ledger updated.

### Phase 7 amendments to earlier contracts

All additive.

| Contract | Change | Lane |
| --- | --- | --- |
| `internal/acp` | session types, methods, `ClientHandler`, `SessionLimits`, `SessionInitialize`; phase 6 names unchanged | 7.1 |
| `internal/jsonrpc` | `Limits.OrderedResponses` (in `conn.go`); `LaunchSpec.AllowDir` (check in `launch.go`); no change for a spec or limits without them | 7.1 (declarations, `conn.go`), 7.2 (`launch.go`) |
| `bus` registry | `ai.session.*` types; `ai` leaves `ReservedFamilies` | 7.2 |
| `notifysock` | `ai.session.**` is a socket type | 7.2 |
| `harness` | `Locate`, `GuardPath` in a new file; no existing harness file changes | 7.2 |
| `config.User`, `config.Repo` | optional `ai.sessions`, `ai.permissions`; repository `ai.sessions.disabled`, `ai.permissions.ask`, `ai.permissions.deny` | 7.2 |
| autonomy catalog | `ai.run`, `ai.respond`, `ai.cancel`, `ai.fs.read`, `ai.fs.write`, `ai.terminal`, `ai.fetch`, `ai.permission` | 7.2 |
| settings check chain | rule and mode validation; the `ai` loosening cases | 7.2 |
| console routes | `POST /api/ai/respond`, `POST /api/ai/cancel` | 7.2 |
| `policy` | `SessionClientPrefix` (`ai-session:`), refused by `NewGate` like `plugin:`; `NewSessionGate`; the `ai respond --allow` never-list entry | 7.3 |
| `app` | `OpenSession`; `AgentEnvironment` reports `AGENTFLOW_ACP_SESSION` | 7.3 |
| CLI surface | `agentflow mcp --session ID`; the runtime command `agentflow ai host --session ID` | 7.3 |
| `agentflow_ui_open` | screen `ai` | 7.3 |
| `TestEveryWriteOperationEmitsEvents` | `ai.run`, `ai.respond`, `ai.cancel` | 7.3 |

### File ownership for phase 7 lanes

A lane changes only the files it owns. A lane that needs another lane's file, or a contract change, stops and reports.

| Lane | Issue | Owns | Must not touch |
| --- | --- | --- | --- |
| 7.1 ACP client, fake agent | #122 | `internal/acp/session_types.go` and the phase 7 paragraph of `doc.go` (contract, first PR); `internal/acp/session.go`, `client.go`, `decode.go` and their tests; `internal/acp/acptest/**` (session scripts, the session modes of `cmd/fake-acp`); in `internal/jsonrpc`: the `OrderedResponses` and `AllowDir` declarations in `types.go` (first PR), `OrderedResponses` in `conn.go` and its test | `internal/acp/types.go`, `initialize.go`; every other `internal/jsonrpc` file; `internal/aisession/**`, `internal/harness/**`, `internal/webui/**`, `internal/command/**`, `internal/config/**`, README |
| 7.2 Sessions, chain, store, events, projection | #123 | `internal/aisession/**` (contract `types.go`, `doc.go` first; `Options` and the import rule of "package dependencies"); `internal/bus/types_phase7.go` and its test, the `ReservedFamilies` line in `internal/bus/types.go` (first PR); `internal/webui/projection_ai_state.go` (first PR), `projection_ai.go`, `stream_runtime_ai.go`, `ai.go`, the two route lines in `server.go`, and their tests (`TestAIPanelFollowsSession` included); `internal/harness/session.go` (new, after #120) and its test; the `AllowDir` check in `internal/jsonrpc/launch.go` and its test; `ai.session.**` in `internal/notifysock/notifysock.go`; `internal/config/ai.go`, the `AI` field of `config.Repo` in `config.go`, the `ai` rows in `field_meta_table.go`, the `ai` group in `catalog.go`, the `ai.*` actions in `autonomy.go`; the `ai` checks and loosening cases in `internal/settings/checks.go` (and a new `check_ai.go`); settings and catalog golden files through `-update` only | `internal/acp/**`, every other `internal/harness/**` file, `internal/command/**`, `internal/app/**`, `internal/mcpserver/**`, `internal/cli/**`, `internal/policy/**`, `internal/plugin/**`, `internal/webui/static/**`, `internal/webui/destination.go`, README |
| 7.3 Commands, MCP, guard, docs | #124 | `internal/command/ops/ai_sessions.go` (with `ops.AISessionOptions`, the production `aisession.Options`) and its test; the `ai` rows in `internal/command/ops/emit_test.go`; `internal/app/ai_sessions.go`, `internal/app/ai_session_client.go` (`OpenSession`) and tests; `AgentEnvironment` in `internal/app/worktree.go`; `SessionClientPrefix`, `NewSessionGate` and the refusal in `internal/policy/approvals.go`, the never-list entry in `internal/policy/policy.go`, and tests; `internal/mcpserver/session.go` and the session-mode lines in `internal/mcpserver/server.go`, with tests; `internal/cli/ai_host.go` and the `ai host` line in `cli.go`'s fixed switch; `internal/webui/destination.go` (screen `ai`); golden files (`internal/mcpserver/testdata/golden/tools.json`, `internal/cli/testdata/golden/**`, `internal/app/testdata/golden/**`) through `-update` only; the session lines in `scripts/e2e-claude.sh`; `adapters/claude-code/skills/ai/SKILL.md`, the four tools in `adapters/claude-code/skills/agentflow/SKILL.md`; `docs/ai-sessions.md`; README sections on the `ai` commands, MCP tools, sessions and their security; the blueprint capability table (`docs/artifact/`, then `scripts/build-blueprint.sh`) | `internal/acp/**`, `internal/aisession/**`, `internal/harness/**`, `internal/config/**`, `internal/settings/**`, `internal/webui/**` except `destination.go`, `parity_test.go` |
| 7.4 AI panel | #125 | `internal/webui/static/ai-panel.*`; the AI nav entry, page section and script tag in `static/index.html`; `ai` rules in `static/style.css`; the AI wiring in `static/app.js` (screen name, page, state dispatch); `internal/webui/ai_browser_test.go`; `internal/webui/testdata/ai/**`; `docs/design/ui/ai-panel.md`; the README section on the AI panel | every Go file other than its browser test, `internal/webui/destination.go`, every other static file |

**Every shared file has one owner:**

| Shared file | Owner | Note |
| --- | --- | --- |
| `internal/webui/static/app.js`, `index.html`, `style.css` | 7.4 | lost time in phase 6 |
| `internal/webui/destination.go` | 7.3 | its screen list is in `agentflow_ui_open`'s description, so it moves `tools.json` (lost time in phase 6) |
| `internal/mcpserver/testdata/golden/tools.json` | 7.3 | `-update` only |
| `internal/cli/testdata/golden/**`, `internal/app/testdata/golden/**` | 7.3 | `-update` only |
| `internal/config/config.go`, `ai.go`, `field_meta_table.go`, `catalog.go`, `autonomy.go` | 7.2 | `config.go` lost time in phase 6 |
| `internal/settings/checks.go` | 7.2 | |
| `scripts/e2e-claude.sh` | 7.3 | lost time in phase 6 |
| `internal/webui/server.go` | 7.2 | route lines only |
| `internal/mcpserver/server.go` | 7.3 | session-mode lines only |
| `internal/cli/cli.go` | 7.3 | the `ai host` line only |
| `internal/command/ops/emit_test.go` | 7.3 | |
| `internal/bus/types.go` | 7.2 | the `ReservedFamilies` line |
| `internal/notifysock/notifysock.go` | 7.2 | `socketTypes` only |
| `internal/jsonrpc/types.go` | 7.1 | declares both new fields in its first PR |
| `internal/jsonrpc/conn.go` / `launch.go` | 7.1 / 7.2 | |
| `internal/policy/approvals.go`, `policy.go` | 7.3 | |
| `internal/app/worktree.go` | 7.3 | `AgentEnvironment` only |
| `internal/harness/**` | issue #120 | phase 7 adds only the new file `session.go` (7.2), after #120 merges |
| `README.md` | 7.3, and 7.4 for the AI panel section | by section |
| `adapters/claude-code/skills/**` | 7.3 | |
| `docs/knowledge/engineering/decisions/event-driven-platform.md`, `docs/plans/acp-platform/ledger.md` | the coordinator | amendments only |
| `go.mod`, `go.sum` | nobody | no new dependency: `os.Root` is the standard library |

- All four lanes can start once this PR merges. 7.1 lands its contract files first, then 7.2.
- Lane 7.2 codes against `acptest` (with the session scripts once 7.1's first PR merges, a hand-written fake before) and fake `ai.respond` and `ai.cancel` Specs. Lane 7.3 codes against the `aisession` declarations with a fake `Manager`. Lane 7.4 codes against `AIState` with a scripted stream fixture and fake endpoints. Each wires production in its last commit, after the lanes it uses have merged.
- Exported contract types change only through an amendment: everything in the code-form table at the top of this section.

### Phase 7 security

**Threat model.** A repository is hostile: its files, its agent configuration, its `.envrc`, its `node_modules`. The harness's model is steerable by what it reads, so every agent request and every agent string is untrusted. Another agent of the person (MCP or CLI) is not the person. A plugin's author may be hostile. The person, their personal layer and the harness binary they installed are trusted; a deliberately malicious harness binary and OS sandboxing are out of scope, as in ACP.

**Security review (lanes 7.1, 7.2 and 7.3, before merge; the coordinator checks it at the gate).**

- [ ] The harness environment is exactly the listed names; a test proves no token, key, `SSH_AUTH_SOCK`, `AGENTFLOW_*` or client session ID reaches the fake agent or its terminals, and credential names pass only for harnesses in `pass_credentials`, never to terminals.
- [ ] No repository can add a PATH entry, a harness, an argument, an env variable, an MCP server, a prompt, a mode, an allow or a limit. A `HOME` or configuration directory inside a repository stops the start.
- [ ] Every `fs/*` and `terminal/create` runs the whole chain; nothing is allowed when no link allows; an earlier `request_permission` authorizes nothing.
- [ ] File access is through `os.Root`; tests refuse a relative path, a path outside, `..`, a link out (also swapped in after the checks), `.git` in any case, a nested repository, a final-component link and a hard-linked file on write, and a Windows stream name.
- [ ] Control-file writes and secret-file reads always ask a person, whatever a rule says.
- [ ] Terminals start without a shell, in the worktree, killed by process group on release, kill and session end; the never-list, the session never-list and `boundary.Checker` see the full line; refused env names deny the request.
- [ ] No agent, plugin, repository or session can allow an ask: `agentflow_ai_respond` cannot allow; the `ai-session:` client cannot answer; `ai respond --allow` refuses agent environments and non-terminals and ignores `--yes`; the pre-bash and session never-lists block it; an allow needs the HMAC; answers are bound to the fingerprint.
- [ ] No session can start a session; the running cap holds under concurrent starts (a race test) and against a lying caller.
- [ ] Every limit ends the session with its code, and cancel answers every waiting ask `cancelled` and kills every process group.
- [ ] `allow_always` is never selected; deny picks `reject_once`, `reject_always` or `cancelled`.
- [ ] No event payload, audit line, log line or repository store carries session text, a path beyond the worktree-relative one in audit, a command line, env values or cost; the projection draws asks only from the store.
- [ ] The transcript and asks are 0600 in 0700 directories in the agentflow home, pruned by the retention rules, redacted best-effort; `keep_transcripts` off deletes them at the end.
- [ ] Agent text is sanitized and shown as text everywhere (terminal, panel, CLI); terminal sequences are neutralized.
- [ ] The session MCP server has only the read-only allowlist, and its gate refuses every non-read.
- [ ] No test starts a real harness, a real terminal program outside its temporary directory, or a detached process; `make e2e` starts no session.

### Phase 7 decisions

*Decided by the architect in the contract PR. The coordinator confirms or changes them before the lanes start. Product and safety choices are in "Open questions for the owner" instead.*

1. **Sessions are hosted by the starting process or a detached `ai host`, never by the console daemon.** `ai run`, `status`, `respond` and `cancel` then work without a daemon and against any `AGENTFLOW_HOME`, which the demo needs (the daemon registry ignores `AGENTFLOW_HOME`). The daemon only projects sessions and relays a person's answers.
2. **One prompt turn per session.** Follow-up turns and resume belong to phase 10 (`session/load`, Memento). Changing this later is additive.
3. **Asks and answers are files in the session store**, with an HMAC on allow answers. This needs no new IPC and works with or without a daemon. It is a same-user limit, stated in "who may answer".
4. **agentflow advertises `fs` and `terminal`** and nothing else, so harnesses that can route file and command work through agentflow's checks do.
5. **Only `allow_once` and `reject_once`** (with `reject_always` or `cancelled` as fallbacks). An `allow_always` scope lives in the agent and cannot be seen or bounded.
6. **Denials are `-32001` with fixed text**, missing files `-32002`. No path or agent text is echoed.
7. **Session text lives only in the agentflow home's session store.** Events carry codes and counts, and the projection reads the store, as the harnesses projection reads the daemon's report.
8. **Control-file writes and secret-file reads always ask.** The ACP security page names the first; the second keeps secrets from reaching the model's provider without a person.
9. **Agent-started sessions get a `confirm` ceiling.** It bounds what an agent's session can do without a person, independent of the agent-side confirmation, which is a heuristic.
10. **Cost is shown, never enforced alone.** It is agent-reported and optional; the time limits bound spending.
11. **Proxy variables are withheld**, as in phases 5 and 6, because they can carry credentials. A person behind a proxy can add them per harness only through an amendment.
12. **No contract Go files in the contract PR** (phase 6 decision 10). Lane 7.1 lands first, then 7.2.

### Open questions for the owner

Each changes what a person sees or what agentflow risks. The recommendation is what the contract above assumes; a different answer is an amendment before the lanes it names start.

1. **Which `HOME` does a session get?** *Recommendation: the real account home (`ai.sessions.home: real`), as the phase 6 sign-in check does, with the variable list above.* The harness can then sign in, and the person's own client hooks apply to it; its own tools can also reach the person's git, `gh` and cloud credential files. *Alternative:* an isolated session home by default: safer, but the harness may not be signed in, and lane 7.2 must first verify that `claude-acp` and `codex-acp` work that way. Changes: lane 7.2's environment and the default of `ai.sessions.home`.
2. **Accept that the harness's own tools are not gated?** *Recommendation: accept, with the mitigations in "what agentflow controls"* (task worktree only, `fs` and `terminal` advertised, unasked tool calls reported and cancelled outside the worktree, changed files listed, a person-chosen mode per harness, no tokens in the environment). *Alternative:* refuse to start a harness unless the person set `ai.sessions.modes` for it, so they consciously pick its most careful mode. Changes: lane 7.3's `ai.run` checks.
3. **Pass a harness its own API-key variables?** *Recommendation: only for harnesses the person lists in `ai.sessions.pass_credentials` (default none), only that harness's verified `credential_env` names, never to terminals.* Adding one is a loosening edit. *Alternative:* never pass them (people who sign in with a key cannot use sessions), or always pass them. Changes: lane 7.2's environment and one setting.
4. **May agents start sessions?** *Recommendation: yes, but off by default (`ai.sessions.agent_start`)*; when on, always confirmed and with the `confirm` ceiling, depth 1 and the store cap. *Alternative:* on by default, or person-only (the MCP tool then always refuses). Changes: the default of one setting and the e2e expectation.
5. **May an agent allow a permission request?** *Recommendation: no.* `agentflow_ai_respond` and `ai respond` in an agent session can reject or cancel only; allow is the person's, in the console, at the foreground terminal or with `ai respond --allow`. This narrows the plan's "`ai respond` works from the CLI and MCP" to rejecting for MCP. *Alternative A:* also drop `ai respond --allow` (console and foreground prompt only), removing one heuristic layer. *Alternative B:* let an agent allow through its own gated confirmation; that rests on the agent's prompt owner, the heuristic phase 6 warned about. Changes: lane 7.3's `ai.respond`.
6. **What happens when the harness acts without asking, inside the worktree?** *Recommendation: warn (`ai.sessions.unasked: warn`); always cancel when the tool call names a place outside the worktree, in `.git` or a control file.* Harnesses commonly run harmless commands without asking, and cancelling those would end most sessions. *Alternative:* cancel by default. Changes: one default.
7. **Repository agent configuration at session start** (hooks in `.claude/settings.json`, servers in `.mcp.json`, and the like, which the harness loads itself). *Recommendation: warn in `ai run`'s confirmation, listing the control files that differ from the default branch's merge base or are untracked.* *Alternative:* refuse to start while any such file differs, unless the person passes `--allow-changed-config` in a terminal. Changes: lane 7.3's confirmation and checks.
8. **Transcripts and cost.** *Recommendation: keep transcripts for 7 days (at most 50 sessions, 200 MiB), 0600, with best-effort redaction; show cost but set no cost cap.* *Alternatives:* transcripts off by default (the panel then shows a session only while it runs); or a default cost cap such as 5 USD enforced only when the harness reports cost in USD (a harness that does not report is then unlimited, which can mislead). Changes: two defaults, or one new setting.
9. **Sessions only in linked worktrees, never the primary checkout?** *Recommendation: yes* (`primary_checkout`, with the fix `agentflow start <task> --worktree`), so the person's main checkout is never changed by an agent and every session's work is a branch. *Alternative:* allow the primary checkout with a warning. Changes: one check in lane 7.3.
10. **Start sessions from the AI panel?** *Recommendation: not in phase 7*: the panel watches, answers and cancels; starting stays in the CLI and MCP, where the confirmation summary is shown. *Alternative:* a Start form that posts to the console, with the same summary and confirmation. Changes: a route (7.2) and a form (7.4).
11. **What may the harness do through agentflow's MCP server?** *Recommendation: the read-only allowlist only.* Writes such as commits and PRs come in phase 8 as prepared drafts a person confirms. *Alternative:* writes that wait for a person in the console, like a plugin's writes (phase 5 decision 5); this needs a new approval path for `ai-session:` records. Changes: lane 7.3's allowlist, and possibly a policy amendment.
12. **Windows.** *Recommendation: accept for phase 7 that stopping a harness or a terminal kills only the child, not its children (no job objects, phase 5 decision 9), and that the detached host uses `DETACHED_PROCESS`.* *Alternative:* add job objects through `golang.org/x/sys/windows` (already a dependency; new platform code). Changes: lane 7.2's Windows files.

### Phase 7 consequences

- A person can run any ready ACP harness for one prompt in a task worktree and watch it from the terminal or the console, and every file and command request that reaches agentflow is checked and either allowed by the person's policy, asked of the person, or denied.
- No agent, plugin, repository or session can allow what only a person may allow, through any route agentflow offers; the remaining gap is a deliberately hostile same-user process, which the section names.
- The harness's own tools stay outside agentflow's checks. The person sees when they acted without asking, and every change lands in a worktree branch to review.
- Session text stays in the person's agentflow home, never in a repository's `.git`, and expires.
- Phase 8 builds features on `aisession.Manager`; phase 9's `delegate` reuses the host and the chain; phase 10 adds history and resume on the session store.
