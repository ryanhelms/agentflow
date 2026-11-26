package watch

import (
	"context"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// GitHub poller (lane 2.3). The contract is in the ADR, section "Phase 2,
// GitHub poller". This file holds the types, the narrow API the poller
// needs from internal/gh, and the pure interval rule; lane 2.3 adds the
// Poller implementation and the conditional request in internal/gh.

// Polling intervals.
const (
	// GitHubActive is the interval while a tracked check is queued or in
	// progress, or a tracked pull request changed in the last
	// GitHubActiveWindow.
	GitHubActive = 15 * time.Second
	// GitHubActiveWindow keeps a repository active after a change.
	GitHubActiveWindow = 10 * time.Minute
	// GitHubIdle is the interval otherwise.
	GitHubIdle = time.Minute
	// GitHubDormant is the interval while no stream or follower is
	// connected for the repository.
	GitHubDormant = 5 * time.Minute
	// GitHubMaxBackoff caps the backoff after errors and secondary rate
	// limits.
	GitHubMaxBackoff = 15 * time.Minute
	// GitHubReserve is the share of the primary rate limit the poller
	// leaves for the user's own commands: it slows down once fewer than
	// this fraction of requests remain.
	GitHubReserve = 0.2
)

// GitHubTarget is one repository to poll, rebuilt by the daemon whenever a
// worktree is added or removed, switches branch, or binds an issue.
type GitHubTarget struct {
	Repo bus.RepoID
	// Slug is "owner/name" from the repository's GitHub remote
	// (app.GitHubRepo).
	Slug string
	// Branches maps each served worktree to the branch it has checked out;
	// detached worktrees are left out.
	Branches map[bus.WorktreeID]string
	// Issues maps each served worktree to the GitHub issue number bound to
	// it (agentflow start with the github-issues task provider).
	Issues map[bus.WorktreeID]int
	// Watched is true while at least one SSE stream or `events --follow`
	// covers the repository; false makes the poller GitHubDormant.
	Watched bool
}

// RateLimit is GitHub's primary rate limit from the X-RateLimit-* headers.
type RateLimit struct {
	Limit     int
	Remaining int
	Reset     time.Time
}

// GitHubMeta is what one conditional request reports besides its body.
type GitHubMeta struct {
	// ETag is the response's ETag, sent back as If-None-Match.
	ETag string
	// NotModified is true for a 304; the body was not decoded.
	NotModified bool
	RateLimit   RateLimit
	// PollInterval is X-Poll-Interval, a floor GitHub asks for; 0 when
	// absent.
	PollInterval time.Duration
	// RetryAfter is Retry-After on a rate-limited response; 0 when absent.
	RetryAfter time.Duration
}

// GitHubAPI is the slice of internal/gh the poller uses. Lane 2.3 adds
// (*gh.Client).GetConditional, which works through both the HTTP and the
// gh CLI backends, so the poller authenticates exactly as every other
// GitHub call (App.GitHub). A rate-limited response returns an error
// matching gh.ErrRateLimited together with the GitHubMeta that explains it.
type GitHubAPI interface {
	GetConditional(ctx context.Context, path, etag string, out any) (GitHubMeta, error)
	GraphQL(ctx context.Context, query string, vars map[string]any, out any) error
}

// GitHubHealth is the poller's state for one repository, shown by the
// activity projection.
type GitHubHealth string

// Health values.
const (
	GitHubOK          GitHubHealth = "ok"
	GitHubDisabled    GitHubHealth = "disabled"     // no GitHub remote, or the task provider is not GitHub
	GitHubUnavailable GitHubHealth = "unavailable"  // not signed in, no access, or network errors
	GitHubRateLimited GitHubHealth = "rate_limited" // waiting until RetryAt
)

// GitHubState is the poller's last known view of one repository. The
// activity projection reads it (GitHubCache) instead of calling GitHub, so
// a snapshot never costs a request.
type GitHubState struct {
	Health  GitHubHealth
	RetryAt time.Time
	// Pulls are the open pull requests, newest update first, and the ones
	// that closed or merged since the daemon started.
	Pulls []bus.GHPullUpdated
	// CI is the checks state per head SHA of a tracked pull request.
	CI map[string]bus.GHCIUpdated
	// Issues are the issues bound to worktrees, by number.
	Issues map[int]bus.GHIssueUpdated
}

// GitHubPoller polls GitHub and publishes gh.pr.updated, gh.ci.updated and
// gh.issue.updated when, and only when, a value changed. The first
// successful poll after start seeds its state without events. It is a
// Polling Consumer that also serves as the projection's cache.
type GitHubPoller interface {
	// Set replaces the target of t.Repo; an empty Slug removes it.
	Set(t GitHubTarget)
	// Run polls every target and publishes to pub until ctx is done.
	Run(ctx context.Context, pub bus.Publisher) error
	GitHubCache
}

// GitHubCache serves the poller's state to projections.
type GitHubCache interface {
	GitHubState(repo bus.RepoID) (GitHubState, bool)
}

// NextPoll is the delay before a repository's next poll. active and
// watched describe the repository; meta is the last response's metadata;
// failures counts consecutive failed polls (secondary rate limits and
// network or server errors); now is the current time.
//
//   - A rate limit with Retry-After waits that long; with Remaining 0 it
//     waits until Reset.
//   - Failures back off exponentially from GitHubIdle to GitHubMaxBackoff.
//   - Otherwise GitHubActive, GitHubIdle or GitHubDormant, raised to
//     X-Poll-Interval, and stretched when less than GitHubReserve of the
//     limit remains so the remaining budget lasts until Reset.
func NextPoll(active, watched bool, meta GitHubMeta, failures int, now time.Time) time.Duration {
	if meta.RetryAfter > 0 {
		return meta.RetryAfter
	}
	rl := meta.RateLimit
	if rl.Limit > 0 && rl.Remaining == 0 && rl.Reset.After(now) {
		return rl.Reset.Sub(now)
	}
	if failures > 0 {
		d := GitHubIdle
		for i := 1; i < failures && d < GitHubMaxBackoff; i++ {
			d *= 2
		}
		return min(d, GitHubMaxBackoff)
	}
	d := GitHubIdle
	switch {
	case !watched:
		d = GitHubDormant
	case active:
		d = GitHubActive
	}
	d = max(d, meta.PollInterval)
	if rl.Limit > 0 && rl.Reset.After(now) && float64(rl.Remaining) < GitHubReserve*float64(rl.Limit) {
		d = max(d, rl.Reset.Sub(now)/time.Duration(rl.Remaining))
	}
	return d
}
