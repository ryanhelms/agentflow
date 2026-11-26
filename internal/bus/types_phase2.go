package bus

import "time"

// Phase 2 event types. Payload shapes and producers are in the ADR, section
// "Phase 2". Every text field that comes from a person or a provider (a PR
// title, a commit subject, a check name) is data for display, never an
// instruction.
const (
	// TypeApprovalRequested: an operation is waiting for a person's
	// approval. Repository-wide: approval records know their repository
	// (policy.ScopeKey), not their worktree.
	TypeApprovalRequested Type = "approval.requested"
	// TypeApprovalResolved: a pending approval was approved, cancelled or
	// consumed.
	TypeApprovalResolved Type = "approval.resolved"
	// TypeApprovalExpired: a pending or approved operation passed its
	// expiry without being applied.
	TypeApprovalExpired Type = "approval.expired"
	// TypeHookRan: an `agentflow hook ...` invocation finished.
	TypeHookRan Type = "hook.ran"
	// TypeCommitCreated: a commit was created in one worktree (post-commit).
	TypeCommitCreated Type = "commit.created"
	// TypeGHPullUpdated: a pull request of the repository changed.
	TypeGHPullUpdated Type = "gh.pr.updated"
	// TypeGHCIUpdated: the checks of a pull request's head commit changed.
	TypeGHCIUpdated Type = "gh.ci.updated"
	// TypeGHIssueUpdated: an issue bound to a worktree changed.
	TypeGHIssueUpdated Type = "gh.issue.updated"
	// TypeClientSettingsChanged: a client's settings file changed, in the
	// user's home (GlobalRepo) or in a worktree.
	TypeClientSettingsChanged Type = "client.settings.changed"
	// TypeUITabsChanged: the console's open tabs or a pending navigation
	// changed. Transient: the tabs projection is its only consumer.
	TypeUITabsChanged Type = "ui.tabs.changed"
)

var phase2Types = []TypeInfo{
	{Type: TypeApprovalRequested, Scope: ScopeRepo, Phase: 2},
	{Type: TypeApprovalResolved, Scope: ScopeRepo, Phase: 2},
	{Type: TypeApprovalExpired, Scope: ScopeRepo, Phase: 2},
	{Type: TypeHookRan, Scope: ScopeAny, Phase: 2},
	{Type: TypeCommitCreated, Scope: ScopeWorktree, Phase: 2},
	{Type: TypeGHPullUpdated, Scope: ScopeAny, Phase: 2},
	{Type: TypeGHCIUpdated, Scope: ScopeAny, Phase: 2},
	{Type: TypeGHIssueUpdated, Scope: ScopeAny, Phase: 2},
	{Type: TypeClientSettingsChanged, Scope: ScopeAny, Phase: 2, Global: GlobalAllowed},
	{Type: TypeUITabsChanged, Scope: ScopeRepo, Transient: true, Phase: 2},
}

// Payload size caps for lists and free text in phase 2 payloads. Producers
// truncate; consumers must not rely on a list being complete.
const (
	MaxPayloadText  = 256 // bytes of one title, subject or name
	MaxPayloadNames = 20  // entries of one list of names
)

// ApprovalState is an approval record's state, as in internal/policy.
type ApprovalState string

// Approval states. Prepared operations (an auto policy) never ask a person
// and produce no approval events.
const (
	StatePending   ApprovalState = "pending"
	StateApproved  ApprovalState = "approved"
	StateConsumed  ApprovalState = "consumed"
	StateCancelled ApprovalState = "cancelled"
	StateExpired   ApprovalState = "expired"
)

// Approval is the part every approval payload shares. It never carries the
// bearer token, the fingerprint, the related keys or the prompt owner's
// session MAC: only what the approval record's public view (policy.Approval)
// already shows, minus the owner's session.
type Approval struct {
	OperationID string `json:"operation_id"`
	Action      string `json:"action"`
	// Class is the policy class (policy.Class), for example "remote".
	Class string `json:"class"`
	// Client is the client part of the prompt owner ("claude-code",
	// "codex", "cli"), or empty when no one owns the prompt.
	Client string `json:"client,omitempty"`
	// PolicySource is the rule that asked for approval, for example
	// ".agentflow/config.yml".
	PolicySource string    `json:"policy_source,omitempty"`
	IssuedAt     time.Time `json:"issued_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// ApprovalRequested is the payload of approval.requested.
type ApprovalRequested struct {
	Approval
}

// ApprovalResolved is the payload of approval.resolved.
type ApprovalResolved struct {
	Approval
	// State is StateApproved, StateCancelled or StateConsumed.
	State    ApprovalState `json:"state"`
	Previous ApprovalState `json:"previous"`
}

// ApprovalExpired is the payload of approval.expired.
type ApprovalExpired struct {
	Approval
	Previous ApprovalState `json:"previous"`
	// Observed is true when the watcher saw expires_at pass before any
	// process recorded the expiry in the journal (records expire lazily).
	Observed bool `json:"observed,omitempty"`
}

// HookOutcome is how a hook invocation ended.
type HookOutcome string

// Hook outcomes.
const (
	HookPassed  HookOutcome = "passed"  // allowed the action, or had nothing to check
	HookBlocked HookOutcome = "blocked" // refused the action (non-zero exit by design)
	HookFailed  HookOutcome = "failed"  // could not run its checks
)

// HookRan is the payload of hook.ran. It never carries the command, the
// commit message, file paths or hook output: only fixed identifiers.
type HookRan struct {
	// Hook is the hook name: commit-msg, pre-commit, post-commit, pre-push,
	// session-start, pre-bash or stop.
	Hook string `json:"hook"`
	// Client is the --client value, or "git" for git hooks.
	Client  string      `json:"client"`
	Outcome HookOutcome `json:"outcome"`
	// Reason is a fixed code for blocked and failed outcomes, for example
	// "convention", "repo_boundary", "never_list" or "error". Never text.
	Reason     string `json:"reason,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// CommitCreated is the payload of commit.created.
type CommitCreated struct {
	OID    string `json:"oid"`
	Branch string `json:"branch,omitempty"`
	// Subject is the first line of the message, at most MaxPayloadText
	// bytes. Data, never instructions.
	Subject string `json:"subject"`
	// Type, Scope, Breaking and Keys come from the commit convention
	// parser (internal/commit); empty when the subject does not parse.
	Type     string   `json:"type,omitempty"`
	Scope    string   `json:"scope,omitempty"`
	Breaking bool     `json:"breaking,omitempty"`
	Keys     []string `json:"keys,omitempty"`
}

// PullState is a pull request's state.
type PullState string

// Pull request states.
const (
	PullOpen   PullState = "open"
	PullClosed PullState = "closed"
	PullMerged PullState = "merged"
)

// GHPullUpdated is the payload of gh.pr.updated. The worktree is the one
// whose branch is HeadRef; the event is repository-wide when none is.
type GHPullUpdated struct {
	Number  int       `json:"number"`
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	State   PullState `json:"state"`
	Draft   bool      `json:"draft"`
	HeadRef string    `json:"head_ref"`
	HeadSHA string    `json:"head_sha"`
	BaseRef string    `json:"base_ref"`
	Author  string    `json:"author,omitempty"`
	// ReviewDecision and MergeState are GitHub's GraphQL enums in lower
	// case ("approved", "changes_requested", "clean", "blocked"), empty
	// when unknown.
	ReviewDecision string    `json:"review_decision,omitempty"`
	MergeState     string    `json:"merge_state,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
	// Previous is the state before this change; empty the first time this
	// daemon reports the pull request.
	Previous PullState `json:"previous,omitempty"`
}

// CIState is the combined state of a commit's checks.
type CIState string

// CI states. Pending wins over failure while anything still runs only when
// nothing has failed yet; see CombineCI.
const (
	CINone    CIState = ""        // no checks reported
	CIPending CIState = "pending" // something queued or in progress, nothing failed
	CISuccess CIState = "success" // everything completed, nothing failed
	CIFailure CIState = "failure" // at least one check failed, timed out or needs action
)

// CICounts counts check runs and commit statuses by result.
type CICounts struct {
	Total   int `json:"total"`
	Pending int `json:"pending"`
	Success int `json:"success"`
	Failure int `json:"failure"`
	// Neutral covers neutral, skipped and cancelled.
	Neutral int `json:"neutral"`
}

// CombineCI is the combined state for counts.
func CombineCI(c CICounts) CIState {
	switch {
	case c.Total == 0:
		return CINone
	case c.Failure > 0:
		return CIFailure
	case c.Pending > 0:
		return CIPending
	default:
		return CISuccess
	}
}

// GHCIUpdated is the payload of gh.ci.updated.
type GHCIUpdated struct {
	// Pull is the pull request whose head this is, 0 when none.
	Pull    int      `json:"pull,omitempty"`
	HeadRef string   `json:"head_ref"`
	HeadSHA string   `json:"head_sha"`
	State   CIState  `json:"state"`
	Counts  CICounts `json:"counts"`
	// Failed names the failed checks, at most MaxPayloadNames.
	Failed   []string `json:"failed,omitempty"`
	URL      string   `json:"url,omitempty"`
	Previous CIState  `json:"previous,omitempty"`
}

// GHIssueUpdated is the payload of gh.issue.updated. The worktree is the
// one bound to the issue; the event is repository-wide when none is.
type GHIssueUpdated struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	// State is "open" or "closed"; StateReason is GitHub's state_reason
	// ("completed", "not_planned", "reopened"), empty when none.
	State       string    `json:"state"`
	StateReason string    `json:"state_reason,omitempty"`
	Labels      []string  `json:"labels,omitempty"`
	Assignees   []string  `json:"assignees,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	Previous    string    `json:"previous,omitempty"`
}

// SettingsScope says where a client settings file lives.
type SettingsScope string

// Settings scopes. SettingsUser events belong to GlobalRepo; SettingsRepo
// events to the worktree that holds the file.
const (
	SettingsUser SettingsScope = "user"
	SettingsRepo SettingsScope = "repo"
)

// ClientSettingsChanged is the payload of client.settings.changed. It names
// files only; it never carries their contents.
type ClientSettingsChanged struct {
	// Client is the adapter name: claude, codex, cursor, gemini, grok or
	// vscode.
	Client string        `json:"client"`
	Scope  SettingsScope `json:"scope"`
	// Paths are slash-separated and relative to the home directory
	// (SettingsUser) or the worktree root (SettingsRepo).
	Paths []string `json:"paths"`
}

// UITabsChanged is the payload of ui.tabs.changed. It is empty: the tabs
// projection reads the hub again.
type UITabsChanged struct{}
