package webui

import (
	"time"

	"github.com/ryanhelms/agentflow/internal/aisession"
)

// Phase 7 AI projection state (lane 7.2). The contract is in the ADR,
// section "Phase 7 AI projection". Changing a shape needs an amendment.
// Every string is the session store's sanitized text; paths under the OS
// home are shown as "~/...".

const ProjectionAI = "ai"

// AIState is the "ai" state: this repository's sessions, newest first.
type AIState struct {
	Sessions   []AISession `json:"sessions"` // at most 20
	Running    int         `json:"running"`  // across the user
	MaxRunning int         `json:"max_running"`
}

type AISession struct {
	ID           string             `json:"id"`
	Harness      string             `json:"harness"`
	HarnessName  string             `json:"harness_name"`
	Worktree     string             `json:"worktree"` // "~/..."
	WorktreeID   string             `json:"worktree_id"`
	Branch       string             `json:"branch,omitempty"`
	State        string             `json:"state"`
	Code         string             `json:"code,omitempty"`
	StartedBy    string             `json:"started_by"` // person, agent
	Client       string             `json:"client,omitempty"`
	Ceiling      string             `json:"ceiling,omitempty"`
	Home         string             `json:"home"`
	CreatedAt    time.Time          `json:"created_at"`
	EndedAt      *time.Time         `json:"ended_at,omitempty"`
	Prompt       string             `json:"prompt"` // at most 4 KiB
	Usage        *AIUsage           `json:"usage,omitempty"`
	Plan         []AIPlanEntry      `json:"plan,omitempty"`
	Items        []AIItem           `json:"items"`      // the last 200 items or 256 KiB, oldest first
	Truncated    bool               `json:"truncated"`  // older items exist
	ToolCalls    []AIToolCall       `json:"tool_calls"` // the last 50
	Terminals    []AITerminal       `json:"terminals,omitempty"`
	Asks         []AIAsk            `json:"asks,omitempty"` // waiting only
	ChangedFiles []string           `json:"changed_files,omitempty"`
	Counters     aisession.Counters `json:"counters"`
}

type AIUsage struct {
	Used         uint64  `json:"used"`
	Size         uint64  `json:"size"`
	CostAmount   float64 `json:"cost_amount,omitempty"`
	CostCurrency string  `json:"cost_currency,omitempty"`
	// CostReported is always the agent's own figure.
	CostReported bool `json:"cost_reported"`
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
	ID     string `json:"id"`
	Line   string `json:"line"`   // POSIX-quoted command
	Cwd    string `json:"cwd"`    // worktree-relative
	Output string `json:"output"` // the last 64 KiB, sequences removed
	Exit   *int   `json:"exit,omitempty"`
	Signal string `json:"signal,omitempty"`
}

type AIAsk struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	ToolKind string     `json:"tool_kind,omitempty"`
	Title    string     `json:"title,omitempty"` // the agent's words
	Paths    []string   `json:"paths,omitempty"`
	Line     string     `json:"line,omitempty"`
	Cwd      string     `json:"cwd,omitempty"`
	EnvNames []string   `json:"env_names,omitempty"`
	Options  []AIOption `json:"options,omitempty"`
	// Diff, for an fs_write ask: what the write would change (phase 7
	// amendment). Not redacted.
	Diff        *AIDiff   `json:"diff,omitempty"`
	Reason      string    `json:"reason"`
	CanAllow    bool      `json:"can_allow"`
	ExpiresAt   time.Time `json:"expires_at"`
	Fingerprint string    `json:"fingerprint"`
}

type AIOption struct {
	ID   string `json:"id"`
	Name string `json:"name"` // the agent's words
	Kind string `json:"kind"`
}
