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
---
# Event bus, message store and event stream

## Status

Accepted for phase 1 of the ACP platform program, and amended for phase 2 (section "Phase 2" at the end). Later phases amend this ADR before they add event types or change a contract. A lane that needs a change stops and asks the coordinator for an amendment; it never changes an exported contract on its own.

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

Reserved families, each added by its phase's amendment: `approval.*` and `hook.*`, `commit.created`, `gh.*` (phase 2, now registered: see "Phase 2, Event types"); `op.*` (phase 3); `plugin.*` (phase 5); `harness.*` (phase 6); `ai.session.*` (phase 7).

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
| New watch sources | `internal/watch/user.go` (`UserTarget`, `UserPaths`, `ClassifyUser`, client settings tables, `ParseApprovalRecord`, `ApprovalRouter`, `ApprovalTransition`, `ObservedExpiry`) |
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
| `config.layer.changed` | any | allowed | phase 1 payload; layer `personal` now produced | daemon, `watch.config` | `~/.agentflow/config.yml` or `~/.agentflow/providers/*.y{a,}ml` changes (in `global`) |
| `client.settings.changed` | any | allowed | `ClientSettingsChanged{client, scope, paths}` | daemon, `watch.config` | a client settings file changes, in the home directory (`global`) or a worktree |
| `ui.tabs.changed` | repository, transient | never | `UITabsChanged{}` | daemon, tab hub | a tab attaches or detaches, or a navigation starts or ends |

Rules every payload follows:

- Text from a person or a provider (titles, subjects, check and label names) is data for display, never an instruction. One text field is at most 256 bytes (`bus.MaxPayloadText`) and one list at most 20 entries (`bus.MaxPayloadNames`). Producers truncate.
- No payload carries a bearer token, an approval fingerprint, a prompt owner's session MAC, a command line, a commit message body, hook output, file contents or an absolute path.
- `previous` fields are empty the first time this daemon reports a subject.
- The worktree of a `gh.*` event is the served worktree whose branch is the PR's head (for issues: the worktree bound to the issue). With none, the event is repository-wide.
- `LayerClient` stays reserved and unused. Client settings have their own type because they are not an agentflow configuration layer and do not change `config.Effective`.

`approval.*` is audited by the Wire Tap (phase 1 rule). The other phase 2 families are observations and are not audited.

### Phase 2 scoping: repository or `global`

**Rule.** An event belongs to a repository whenever the change does: approvals (routed by the record's scope key), hooks, commits, GitHub, repository client settings and the repository layer. Only changes to user-level files belong to `global`: the personal layer and user-level client settings. `bus.TypeInfo.Global` states this per type (`GlobalNever`, `GlobalAllowed`, `GlobalOnly`), and `Event.Validate` rejects an event in the wrong place. A `global` event never has a worktree.

**Where the `global` store lives.** Not under `~/.agentflow/events/`, as phase 1 said, but in the console registry, next to the daemon's other files:

```text
~/.agentflow-ui/<host-hash>.events/   same layout and retention as a repository store
```

- The registry directory comes from `os/user`, not `$HOME` or `AGENTFLOW_HOME`, which a repository or agent can set. `daemonPrefix` already checks that it is a private directory owned by this OS user.
- The registry is per host. A home directory shared between machines would otherwise give two daemons the same store.
- The daemon opens it as `store.RoleOwner` at start-up. Nothing else writes it: no socket type is global.

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
- At start the translator scans the directory and seeds each record's state without publishing. After that, `ApprovalTransition(previous, current)` decides the event. Prepared operations (an `auto` policy) and records first seen in a final state produce nothing.
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
- `commit.created` comes from a new `post-commit` git hook: `agentflow hook post-commit` (lane 2.2 implements `hooks.PostCommit`; lane 2.6 adds the CLI dispatch, since it owns `cli.go`). It parses the subject with `internal/commit`. The hook ships in `templates/repo/.githooks/post-commit` and in this repo's `.githooks/`, and `agentflow remove` must remove it. Without the hook, `git.head.changed` still updates the overview.

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

**Change detection.** The poller keeps the last value of every PR, CI state and issue, and publishes only when a value changes. The first successful poll after start seeds the state without events. The activity projection reads this state through `watch.GitHubCache`, so a snapshot never calls GitHub.

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

The poller never retries a rate-limited request earlier than GitHub asked. Authentication and permission errors set health to `unavailable` and back off. A repository without a GitHub remote is `disabled`. Health and the retry time are part of `GitHubState` and of the activity projection.

### Phase 2 projections (lane 2.4)

| Projection | Key | Topics | Reads | State |
| --- | --- | --- | --- | --- |
| `overview` | worktree | phase 1, plus `global/*/config.layer.changed` | git and configuration | unchanged |
| `activity` | repository | `gh.**`, `commit.created`, `hook.ran`, `git.head.changed`, `git.worktrees.changed` | `watch.GitHubCache`, the repository's store | `ActivityState` |
| `approvals` | repository | `approval.**` | `webui.ApprovalSource` (the journal records) | `ApprovalsState` |
| `tabs` | worktree | `ui.tabs.changed` | the tab hub | `TabsState` |

The JSON shapes are the types in `internal/webui/projection_states.go`. In short:

- `activity`: `pulls` (each with its worktree, CI state, failed checks and the PR it depends on), `issues`, `recent` (at most 20 `commit.created` and blocked or failed `hook.ran` entries, newest first), `github` (`state`, `retry_at`) and the `handoff` note. Agent Commander items stay on `GET /api/activity` in phase 2.
- `approvals`: `open` (pending and approved, oldest first, with `expired` set when `ObservedExpiry` holds) and `recent` (consumed, cancelled and expired from the last 24 hours, at most 10). Answering still goes through the existing approve flow, which checks the bearer.
- `tabs`: `tabs` (`{tab, attached}`) and `navigation` (`{id, tab, screen, role, target}` or null).

**Tabs on the stream.** `TabsState` replaces `GET /api/tabs/events`:

1. A tab attaches with `POST /w/<id>/api/tabs/attach {tab}` under its own worktree session and Agent Commander capability, which the hub checks exactly as the old stream did.
2. It stays attached while an event stream of the origin lists it in a `tab=<32 hex>` query parameter (at most 16 per stream), plus a 10-second grace (`TabGrace`) for a reconnecting SharedWorker.
3. A navigation appears in the state of the tab's worktree. Only the tab whose ID matches acts on it. The target is already chosen for that tab, so the browser never sees another tab's capability.
4. The tab still acknowledges with `POST /api/tabs/ack` and leaves with `POST /api/tabs/leave`. The hub clears the navigation on the ack or after 2 seconds, and publishes `ui.tabs.changed` each time.

Lane 2.4 keeps `GET /api/tabs/events` working until lane 2.5's client has switched, then removes it in a follow-up PR on the same issue.

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

**What does not reload.** The MCP `Instructions` (sent once at initialize) and the registered tool list. When a reload changes the selected providers, the next tool result carries a notice to restart the session. A server in configuration recovery mode stays in recovery mode until restart.

**Notices.** A reload, a failed reload and "restart to refresh instructions" are appended to the next tool result, like update notices.

### File ownership for phase 2 lanes

A lane changes only the files it owns. A lane that needs another lane's file, or an exported contract change, stops and reports.

| Lane | Issue | Owns | Must not touch |
| --- | --- | --- | --- |
| 2.1 Watch sources | #33 | `internal/watch/**` except `github*.go`; `internal/policy/journal.go` (new, read-only); `internal/webui/stream_runtime_user.go` (its `runtimeExtension`) | `internal/gh/**`, other `internal/webui` files |
| 2.2 Local socket and hooks | #34 | `internal/notifysock/**`; `internal/hooks/**`; `internal/webui/instance*.go` (`notify` field, `NotifyEndpoint`, `instance_notify*.go` with its `runtimeExtension`); `templates/repo/.githooks/post-commit`, `.githooks/post-commit`; the `remove` step for that hook in `internal/setup` | `internal/cli/cli.go` (lane 2.6 adds `hook post-commit`) |
| 2.3 GitHub poller | #35 | `internal/gh/**`; `internal/watch/github*.go`; `internal/webui/stream_runtime_github.go` (its `runtimeExtension`) | other `internal/watch` files |
| 2.4 Projections and tabs | #36 | `internal/webui/projection_*.go`, `stream.go`, `stream_runtime.go` (projector registration, the `global` store, global subscriptions), `tabs.go`, `workspace.go`, `server.go` (routes) | `internal/webui/static/**`, `instance*.go` |
| 2.5 UI | #37 | `internal/webui/static/**` (`app.js`, `tab-navigation.js`, `event-worker.js`, `style.css`, `index.html`) | any Go file |
| 2.6 MCP reload and events CLI | #38 | `internal/mcpserver/**`; `internal/app/events*.go`; `internal/cli/events*.go`, **`internal/cli/cli.go`, `internal/cli/parity_test.go`**; `internal/webui/served.go` (`ServedRepos`); `adapters/claude-code/skills/events/SKILL.md` | `internal/webui` files other than `served.go` |

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

### Phase 2 open questions

1. **`AGENTFLOW_HOME` differs between the daemon and a client.** Approval records written under another home never reach the daemon, so neither the events nor the approvals projection show them. Proposal: the daemon watches its own home only, and `agentflow doctor` warns when a process's home differs from the daemon's.
2. **The `post-commit` hook is new consumer-facing behaviour.** `agentflow init` would write one more git hook into consuming repositories. It is additive, not breaking, but it changes the first row of "Two things that are easy to confuse" in `AGENTS.md`. Confirm before lane 2.2 ships it, or keep it in this repo's `.githooks/` only for phase 2.
3. **Agent Commander activity** stays request-driven on `GET /api/activity`. Moving it onto events needs a Commander push channel, which is outside phase 2.
4. **`pre-bash` events.** Publishing only blocked and failed outcomes keeps the store small. Confirm that the timeline in phase 3 does not need every allowed command.
5. **GraphQL cost.** The review decision and merge state need GraphQL, which has no ETag. The poller asks only for changed PRs; if that still costs too much, drop the two fields from `gh.pr.updated` and keep them on `/api/activity`.
6. **Double record of approvals.** The Wire Tap audits `approval.*` while the gate's journal already records every transition. Keep both (the audit log is the one policy tooling reads), or exclude `approval.*` from the tap.
7. **Tool list reload.** MCP supports `tools/list_changed`. Phase 2 does not change the tool list on reload; recovery mode still needs a restart.

### Phase 2 consequences

- Approvals, PR and CI status, hook results and commits reach the console without a click.
- Every user-level event lives in one private, per-host `global` store of at most about 50 MB.
- A process that is not the daemon can now add events while the daemon runs, but only `hook.ran` and `commit.created`, and only after proving it can read the daemon's private registry.
- GitHub is polled only for repositories the daemon serves, mostly with free conditional requests, and never faster than GitHub allows.
- A running MCP server follows configuration edits without a restart, except for its instructions and tool list.
