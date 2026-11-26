package webui

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Event stream contract (ADR event-driven-platform, "SSE wire format").
// Lane 1.4 implements the endpoint in stream.go and the projections in
// projection_*.go.
const (
	// EventStreamPath is served under each worktree's console prefix
	// (/w/<id>/api/events), behind the existing Bearer authorization.
	EventStreamPath = "/api/events"
	// StreamHeartbeat is the interval of ": ping" comment frames.
	StreamHeartbeat = 15 * time.Second
	// StreamRetry is sent once as "retry:" in milliseconds.
	StreamRetry = 1000
	// MaxStreamTopics caps the topic query parameters of one stream.
	MaxStreamTopics = 64
	// MaxStreamsPerOrigin caps concurrent streams per console origin;
	// more get 429.
	MaxStreamsPerOrigin = 8
)

// SSE event names. Every frame is "event: <name>\nid: <cursor>\ndata:
// <one line of JSON>\n\n"; resync frames carry no id.
const (
	FrameSnapshot = "snapshot"
	FrameDelta    = "delta"
	FrameResync   = "resync"
)

// ProjectionKey addresses one projection state: one worktree, or one
// repository when Worktree is empty.
type ProjectionKey struct {
	Repo     bus.RepoID     `json:"repo"`
	Worktree bus.WorktreeID `json:"worktree,omitempty"`
}

// Projection is a read model the console shows, kept current from events.
// Phase 1 events are notifications, not the source of truth: State reads
// git and configuration again, so a missed event is repaired by the next
// one or by a snapshot. The projector caches each key's last state and
// sends it only when the JSON changed.
type Projection interface {
	// Name is the projection's wire name, for example "overview".
	Name() string
	// Topics are the events that can change this projection.
	Topics() []bus.Topic
	// Keys returns the states e may have changed. A repository-wide event
	// returns the key of every worktree of its repository.
	Keys(e bus.Event) []ProjectionKey
	// State computes the current state of key as a JSON object, or JSON
	// null when key no longer exists (the client removes it).
	State(ctx context.Context, key ProjectionKey) (json.RawMessage, error)
}

// ProjectionState is one projection state on the wire.
type ProjectionState struct {
	Projection string         `json:"projection"`
	Repo       bus.RepoID     `json:"repo"`
	Worktree   bus.WorktreeID `json:"worktree,omitempty"`
	// State replaces the client's copy for this key; null removes it.
	State json.RawMessage `json:"state"`
}

// SnapshotFrame is the data of a snapshot frame: every state the stream's
// topics select. The client replaces its whole store with it.
type SnapshotFrame struct {
	// Cursor equals the frame's id: the store heads the snapshot reflects.
	Cursor string            `json:"cursor"`
	States []ProjectionState `json:"states"`
}

// DeltaFrame is the data of a delta frame: one event and the states it
// changed. Replayed events (after Last-Event-ID) carry no states; the
// snapshot that follows the replay brings states up to date.
type DeltaFrame struct {
	Event  bus.Event         `json:"event"`
	States []ProjectionState `json:"states"`
}

// ResyncReason says why the server discarded the client's state.
type ResyncReason string

// Resync reasons.
const (
	ResyncOverflow      ResyncReason = "overflow"       // the stream's subscription overflowed
	ResyncCursorExpired ResyncReason = "cursor_expired" // Last-Event-ID is older than retention
	ResyncReplayLimit   ResyncReason = "replay_limit"   // more than store.MaxReplay events missed
	ResyncTopics        ResyncReason = "topics"         // Last-Event-ID names a repository the topics do not cover
)

// ResyncFrame is the data of a resync frame. A snapshot frame follows on
// the same connection.
type ResyncFrame struct {
	Reason ResyncReason `json:"reason"`
}
