package webui

import (
	"context"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/watch"
)

// Phase 2 projection states (lane 2.4). The contract is in the ADR,
// section "Phase 2, Projections". Each type here is the JSON of one
// projection's state for one key; changing a shape needs an amendment.

// Phase 2 projection names.
const (
	ProjectionActivity  = "activity"
	ProjectionApprovals = "approvals"
	ProjectionTabs      = "tabs"
)

// Phase 2 list caps.
const (
	MaxActivityRecent  = 20 // ActivityState.Recent
	MaxApprovalsRecent = 10 // ApprovalsState.Recent
	// ApprovalsRecentAge drops resolved and expired approvals from
	// ApprovalsState.Recent after this long.
	ApprovalsRecentAge = 24 * time.Hour
	// TabGrace keeps a tab attached this long after the last stream that
	// listed it closed, so a reconnecting SharedWorker does not lose it.
	TabGrace = 10 * time.Second
)

// ActivityState is the "activity" state. Key: the repository (Worktree
// empty). Topics: gh.**, commit.created, hook.ran, git.head.changed,
// git.worktrees.changed. GitHub data comes from the poller's cache
// (watch.GitHubCache), Recent from the repository's store; computing it
// never calls GitHub. Agent Commander items stay on GET /api/activity in
// phase 2.
type ActivityState struct {
	// Pulls are open pull requests, ready to merge first (clean and not
	// draft), then by most recent update, as /api/activity orders them.
	Pulls []ActivityPull `json:"pulls"`
	// Issues are the issues bound to this repository's worktrees.
	Issues []ActivityIssue `json:"issues"`
	// Recent lists, newest first, commit.created events and hook.ran
	// events whose outcome is blocked or failed; at most MaxActivityRecent.
	Recent []ActivityEntry `json:"recent"`
	GitHub ActivityGitHub  `json:"github"`
	// Handoff is the same plain-text note /api/activity returns, for the
	// handoff panel.
	Handoff string `json:"handoff"`
}

// ActivityPull is one pull request with its checks.
type ActivityPull struct {
	bus.GHPullUpdated
	// Worktree is the served worktree whose branch is HeadRef.
	Worktree bus.WorktreeID `json:"worktree,omitempty"`
	CI       bus.CIState    `json:"ci,omitempty"`
	Failed   []string       `json:"failed,omitempty"`
	// DependsOn is the open pull request whose head is this one's base.
	DependsOn int `json:"depends_on,omitempty"`
}

// ActivityIssue is one bound issue.
type ActivityIssue struct {
	bus.GHIssueUpdated
	Worktree bus.WorktreeID `json:"worktree,omitempty"`
}

// ActivityEntry is one recent event, reduced to fixed fields.
type ActivityEntry struct {
	EventID  string         `json:"event_id"`
	Type     bus.Type       `json:"type"`
	Time     time.Time      `json:"time"`
	Worktree bus.WorktreeID `json:"worktree,omitempty"`
	// Commit is set for commit.created, Hook for hook.ran.
	Commit *bus.CommitCreated `json:"commit,omitempty"`
	Hook   *bus.HookRan       `json:"hook,omitempty"`
}

// ActivityGitHub is the poller's health for the repository.
type ActivityGitHub struct {
	State   watch.GitHubHealth `json:"state"`
	RetryAt *time.Time         `json:"retry_at,omitempty"`
}

// ApprovalsState is the "approvals" state. Key: the repository. Topics:
// approval.**. State reads the approval records of the repository's scope
// (ApprovalSource), never the events alone, so a missed event is repaired
// by the next one or a snapshot.
type ApprovalsState struct {
	// Open are pending and approved operations, oldest first.
	Open []ApprovalItem `json:"open"`
	// Recent are consumed, cancelled and expired ones younger than
	// ApprovalsRecentAge, newest first, at most MaxApprovalsRecent.
	Recent []ApprovalItem `json:"recent"`
}

// ApprovalItem is one approval record's public view. It carries no bearer
// token, fingerprint or prompt-owner session: answering still goes through
// the existing approve flow, which checks the bearer.
type ApprovalItem struct {
	bus.Approval
	State bus.ApprovalState `json:"state"`
	// Expired is true for a pending or approved record past ExpiresAt that
	// the journal has not marked yet (watch.ObservedExpiry).
	Expired bool `json:"expired,omitempty"`
}

// ApprovalSource lists the approval records of one served repository.
// Lane 2.1 implements it over policy's journal directory (read-only, by
// policy.ScopeKey); tests use a fake.
type ApprovalSource interface {
	Approvals(ctx context.Context, repo bus.RepoID) ([]ApprovalItem, error)
}

// TabsState is the "tabs" state, which replaces GET /api/tabs/events. Key:
// one worktree (the tabs showing it). Topics: ui.tabs.changed, published
// by the tab hub when a tab attaches or detaches or a navigation starts or
// ends.
//
// A tab attaches with POST /w/<id>/api/tabs/attach {tab}, under its own
// worktree session and Agent Commander capability exactly as the old
// stream checked them, and stays attached while some event stream of the
// origin lists it in a tab=<32 hex> query parameter (at most 16), plus
// TabGrace. It still acknowledges with POST /api/tabs/ack and leaves with
// POST /api/tabs/leave.
type TabsState struct {
	Tabs []TabView `json:"tabs"`
	// Navigation is the pending navigation for one of Tabs, or null. Only
	// the tab whose ID equals Navigation.Tab acts on it, then acknowledges
	// it; the hub clears it on the ack or after two seconds.
	Navigation *TabNavigation `json:"navigation"`
}

// TabView is one attached tab.
type TabView struct {
	Tab string `json:"tab"`
	// Attached is when the tab attached; the newest tab is asked first.
	Attached time.Time `json:"attached"`
}

// TabNavigation is one navigation request, as the old stream's
// "navigate" event carried it. Target is already chosen for this tab (the
// scoped target when the tab's Commander scope matches), so the browser
// never sees another tab's capability.
type TabNavigation struct {
	ID  string `json:"id"`
	Tab string `json:"tab"`
	Destination
	Target string `json:"target,omitempty"`
}
