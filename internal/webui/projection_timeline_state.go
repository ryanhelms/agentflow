package webui

import (
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Phase 3 timeline projection state (lane 3.5). The contract is in the ADR,
// section "Phase 3, Timeline projection". Changing a shape needs an
// amendment.

// ProjectionTimeline is the timeline projection's name.
const ProjectionTimeline = "timeline"

// Timeline caps.
const (
	// MaxTimelineOperations caps TimelineState.Operations.
	MaxTimelineOperations = 50
	// MaxTimelineSteps caps TimelineOperation.Steps; older steps are
	// dropped first, and the operation's Truncated is set.
	MaxTimelineSteps = 12
	// TimelineAge drops an operation whose last step is older.
	TimelineAge = 7 * 24 * time.Hour
)

// TimelineState is the "timeline" state. Key: the repository (Worktree
// empty). Topics: op.**, approval.**. It is rebuilt from the repository's
// store (at most store.MaxReplay events back), then kept up to date from
// the live events. Operations are newest first by Updated.
type TimelineState struct {
	Operations []TimelineOperation `json:"operations"`
	// Truncated is true when operations past MaxTimelineOperations or
	// older than the store's replay window were left out.
	Truncated bool `json:"truncated,omitempty"`
}

// TimelineOperation is one operation: every op.* and approval.* event with
// its correlation ID (approval.* events by their operation ID).
type TimelineOperation struct {
	// ID is the operation ID, the events' CorrelationID.
	ID string `json:"id"`
	// Operation, Action, Class and Client come from the first op.* event.
	Operation string `json:"operation"`
	Action    string `json:"action,omitempty"`
	Class     string `json:"class,omitempty"`
	Client    string `json:"client"`
	// Worktree is the console worktree ID the operation ran in, or "".
	Worktree bus.WorktreeID `json:"worktree,omitempty"`
	Subject  string         `json:"subject,omitempty"`
	Task     string         `json:"task,omitempty"`
	Branch   string         `json:"branch,omitempty"`
	// State is the latest step's state: requested, suggested,
	// awaiting_approval, prepared, applied, failed, or from an approval
	// event approved, cancelled or expired.
	State string `json:"state"`
	// Code is the last op.failed or op.suggested code.
	Code string `json:"code,omitempty"`
	// Approval is the approval's state, when the operation has one.
	Approval *TimelineApproval `json:"approval,omitempty"`
	Links    []bus.OpLink      `json:"links,omitempty"`
	Started  time.Time         `json:"started"`
	Updated  time.Time         `json:"updated"`
	// Steps are the operation's events, oldest first.
	Steps     []TimelineStep `json:"steps"`
	Truncated bool           `json:"truncated,omitempty"`
}

// TimelineApproval is the approval part of a timeline operation.
type TimelineApproval struct {
	State     string     `json:"state"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// TimelineStep is one event of an operation. Seq locates the event in
// `agentflow events`. The operation's audit line carries its ID
// (TimelineOperation.ID), which is how the timeline links to it.
type TimelineStep struct {
	EventID string    `json:"event_id"`
	Seq     uint64    `json:"seq,omitempty"`
	Type    bus.Type  `json:"type"`
	Time    time.Time `json:"time"`
	State   string    `json:"state"`
	Code    string    `json:"code,omitempty"`
}
