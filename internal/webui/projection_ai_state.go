package webui

import (
	"time"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/aisession"
)

// Phase 7 AI projection state (lane 7.2). The contract is in the ADR,
// section "Phase 7 AI projection". Changing a shape needs an amendment.
// Every string is the session store's sanitized text; paths under the OS
// home are shown as "~/...".

const ProjectionAI = "ai"

// AIState is the "ai" state: this repository's sessions and drafts,
// newest first, and the draft features (phase 8, ADR "Phase 8 AI
// projection and console routes").
type AIState struct {
	Sessions   []AISession `json:"sessions"` // at most 20; drafts are not listed here
	Running    int         `json:"running"`  // across the user, drafts included
	MaxRunning int         `json:"max_running"`
	Drafts     []AIDraft   `json:"drafts"`   // this repository's, newest first, at most 20
	Features   []AIFeature `json:"features"` // every feature, in ai.Features() order
}

// AIFeature says whether the console can offer "Draft with AI".
type AIFeature struct {
	ID          string `json:"id"`
	Label       string `json:"label"` // agentflow's words
	Harness     string `json:"harness,omitempty"`
	HarnessName string `json:"harness_name,omitempty"`
	Available   bool   `json:"available"`
	Code        string `json:"code,omitempty"` // feature_off, not_ready, repo_disabled, ...
	Fix         string `json:"fix,omitempty"`
}

// AIDraft is one draft session and, once finished, its result.
type AIDraft struct {
	Session     string       `json:"session"`
	Feature     string       `json:"feature"`
	Harness     string       `json:"harness"`
	HarnessName string       `json:"harness_name"`
	Worktree    string       `json:"worktree"` // "~/..."
	WorktreeID  string       `json:"worktree_id"`
	Branch      string       `json:"branch,omitempty"`
	State       string       `json:"state"` // starting, running, cancelling, ready, invalid, failed, cancelled
	Code        string       `json:"code,omitempty"`
	StartedBy   string       `json:"started_by"` // person, agent
	CreatedAt   time.Time    `json:"created_at"`
	EndedAt     *time.Time   `json:"ended_at,omitempty"`
	Manifest    *ai.Manifest `json:"manifest"`
	Draft       *ai.Draft    `json:"draft,omitempty"`    // set once finished
	Denied      int          `json:"denied"`             // requests the draft profile denied
	Warnings    []ai.Issue   `json:"warnings,omitempty"` // worktree_changed and the draft's own
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
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	ToolKind string   `json:"tool_kind,omitempty"`
	Title    string   `json:"title,omitempty"` // the agent's words
	Paths    []string `json:"paths,omitempty"`
	Line     string   `json:"line,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
	EnvNames []string `json:"env_names,omitempty"`
	// Env is a terminal ask's environment entries with their values, and
	// Program the resolved program it would start; ResolvedPaths, when set,
	// is where each of Paths leads through a link. Like Diff they come from
	// the 0600 ask file only and reach the page only in this state.
	Env           []AIEnv    `json:"env,omitempty"`
	Program       string     `json:"program,omitempty"`
	ResolvedPaths []string   `json:"resolved_paths,omitempty"`
	Options       []AIOption `json:"options,omitempty"`
	// Diff, for an fs_write ask: what the write would change (phase 7
	// amendment). Not redacted.
	Diff        *AIDiff   `json:"diff,omitempty"`
	Reason      string    `json:"reason"`
	CanAllow    bool      `json:"can_allow"`
	ExpiresAt   time.Time `json:"expires_at"`
	Fingerprint string    `json:"fingerprint"`
}

// AIEnv is one environment entry of a terminal ask.
type AIEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type AIOption struct {
	ID   string `json:"id"`
	Name string `json:"name"` // the agent's words
	Kind string `json:"kind"`
}
