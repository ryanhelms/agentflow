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
---
# Event bus, message store and event stream

## Status

Accepted for phase 1 of the ACP platform program, and amended for phase 2 (section "Phase 2") and phase 3 (section "Phase 3", at the end). Later phases amend this ADR before they add event types or change a contract. A lane that needs a change stops and asks the coordinator for an amendment; it never changes an exported contract on its own.

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

Reserved families, each added by its phase's amendment: `approval.*` and `hook.*`, `commit.created`, `gh.*` (phase 2, now registered: see "Phase 2, Event types"); `op.*` (phase 3, now registered: see "Phase 3 event types"); `plugin.*` (phase 5); `harness.*` (phase 6); `ai.session.*` (phase 7).

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
