package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// GitHubClock is the poller's source of time. Tests replace it with a fake
// clock; NewGitHubPoller uses the real one when GitHubOptions.Clock is nil.
type GitHubClock interface {
	Now() time.Time
	// Timer returns a channel that receives once d has passed, and a
	// function that releases the timer early.
	Timer(d time.Duration) (<-chan time.Time, func())
}

type realGitHubClock struct{}

func (realGitHubClock) Now() time.Time { return time.Now() }

func (realGitHubClock) Timer(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// GitHubFailure says what kind of failure an error from GitHubAPI is. The
// poller treats each kind differently: it never retries a rate-limited
// request before GitHub allows, and it does not count a missing issue as a
// failure of the repository.
type GitHubFailure int

// Failure kinds.
const (
	// FailureOther covers network errors, server errors and bad responses.
	FailureOther GitHubFailure = iota
	// FailureRateLimited is a primary or secondary rate limit.
	FailureRateLimited
	// FailureAuth means GitHub wants a login, or refuses this account.
	FailureAuth
	// FailureNotFound is a 404: the subject is gone or hidden.
	FailureNotFound
)

// GitHubOptions configures NewGitHubPoller.
type GitHubOptions struct {
	// API returns the client for the repository of t. It runs once per poll
	// cycle, so it should cache what is expensive to build. An error counts
	// as a failed cycle, so the poller backs off.
	API func(ctx context.Context, t GitHubTarget) (GitHubAPI, error)
	// Classify tells the failure kinds apart; nil treats every error as
	// FailureOther. gh.Classify is the real one. The poller also treats any
	// failure whose headers show an exhausted limit or a Retry-After as
	// rate-limited.
	Classify func(error) GitHubFailure
	// Clock is the source of time; nil means the real clock.
	Clock GitHubClock
	// OnError receives non-fatal errors: a Publish failure or a GraphQL
	// enrichment that failed.
	OnError func(error)
}

// Limits of the poller that the ADR does not name.
const (
	// githubMinGap is the least time between two polls of one repository,
	// so a burst of target changes does not become a burst of requests.
	githubMinGap = 5 * time.Second
	// githubPageSize is the size of the open pull request page. The poller
	// never pages: a repository with more open pull requests keeps only the
	// most recently updated ones in view.
	githubPageSize = 50
	// githubKeepClosed is how many closed or merged pull requests stay in
	// the cache.
	githubKeepClosed = 20
	// githubMissingRetry is how long a 404 issue is left alone.
	githubMissingRetry = 10 * time.Minute
	// githubEnrichTries is how often a failed GraphQL enrichment is retried
	// for one pull request before it waits for the next change.
	githubEnrichTries = 3
)

type githubPoller struct {
	opts  GitHubOptions
	clock GitHubClock

	mu       sync.Mutex
	repos    map[bus.RepoID]*repoPoll
	disabled map[bus.RepoID]bool
	run      *pollRun  // non-nil while Run is active
	rate     RateLimit // the latest primary limit seen on any response
	hold     time.Time // no request before this, after a rate-limited response
}

// pollRun is one call of Run.
type pollRun struct {
	ctx context.Context
	pub bus.Publisher
	wg  sync.WaitGroup
}

// NewGitHubPoller returns the GitHub poller of ADR "Phase 2, GitHub poller".
func NewGitHubPoller(o GitHubOptions) GitHubPoller {
	p := &githubPoller{
		opts:     o,
		clock:    o.Clock,
		repos:    map[bus.RepoID]*repoPoll{},
		disabled: map[bus.RepoID]bool{},
	}
	if p.clock == nil {
		p.clock = realGitHubClock{}
	}
	return p
}

// repoPoll is one polled repository.
type repoPoll struct {
	repo bus.RepoID
	wake chan struct{} // 1 slot: the target changed in a way worth polling for
	done chan struct{} // closed when the target is removed
	once sync.Once

	mu     sync.Mutex
	target GitHubTarget
	state  GitHubState // never mutated after publication
	have   bool        // a poll cycle has finished

	work *repoWork // owned by the repository's poll goroutine
}

func (rp *repoPoll) close() { rp.once.Do(func() { close(rp.done) }) }

func (rp *repoPoll) poke() {
	select {
	case rp.wake <- struct{}{}:
	default:
	}
}

func cloneTarget(t GitHubTarget) GitHubTarget {
	t.Branches = maps.Clone(t.Branches)
	t.Issues = maps.Clone(t.Issues)
	return t
}

// Set implements GitHubPoller.
func (p *githubPoller) Set(t GitHubTarget) {
	p.mu.Lock()
	if t.Slug == "" || !validGitHubSlug(t.Slug) {
		previous := GitHubHealth("")
		if rp := p.repos[t.Repo]; rp != nil {
			rp.mu.Lock()
			if rp.have {
				previous = rp.state.Health
			}
			rp.mu.Unlock()
			rp.close()
			delete(p.repos, t.Repo)
		}
		was := p.disabled[t.Repo]
		p.disabled[t.Repo] = true
		run := p.run
		p.mu.Unlock()
		// Disabling is a health change the activity projection must see:
		// without it, a repository without a GitHub remote would stay
		// "unavailable" until some other event recomputed the state.
		if !was && run != nil {
			p.publishHealth(run, t.Repo, previous, GitHubState{Health: GitHubDisabled})
		}
		return
	}
	defer p.mu.Unlock()
	delete(p.disabled, t.Repo)
	t = cloneTarget(t)
	rp := p.repos[t.Repo]
	if rp == nil {
		rp = &repoPoll{repo: t.Repo, target: t, wake: make(chan struct{}, 1), done: make(chan struct{}), work: newRepoWork()}
		p.repos[t.Repo] = rp
		if p.run != nil {
			p.start(rp)
		}
		return
	}
	rp.mu.Lock()
	old := rp.target
	rp.target = t
	rp.mu.Unlock()
	// A new slug, another branch or issue, or a viewer that just arrived
	// is worth an early poll. A viewer that left is not.
	if old.Slug != t.Slug || !maps.Equal(old.Branches, t.Branches) || !maps.Equal(old.Issues, t.Issues) ||
		(t.Watched && !old.Watched) {
		rp.poke()
	}
}

// GitHubState implements GitHubCache.
func (p *githubPoller) GitHubState(repo bus.RepoID) (GitHubState, bool) {
	p.mu.Lock()
	disabled := p.disabled[repo]
	rp := p.repos[repo]
	p.mu.Unlock()
	if disabled {
		return GitHubState{Health: GitHubDisabled}, true
	}
	if rp == nil {
		return GitHubState{}, false
	}
	rp.mu.Lock()
	s, ok := rp.state, rp.have
	rp.mu.Unlock()
	if !ok {
		return GitHubState{}, false
	}
	s.Pulls = slices.Clone(s.Pulls)
	s.CI = maps.Clone(s.CI)
	s.Issues = maps.Clone(s.Issues)
	return s, true
}

// Run implements GitHubPoller.
func (p *githubPoller) Run(ctx context.Context, pub bus.Publisher) error {
	run := &pollRun{ctx: ctx, pub: pub}
	p.mu.Lock()
	if p.run != nil {
		p.mu.Unlock()
		return errors.New("github poller is already running")
	}
	p.run = run
	for _, rp := range p.repos {
		p.start(rp)
	}
	disabled := slices.Collect(maps.Keys(p.disabled))
	p.mu.Unlock()
	// Repositories disabled before Run had no publisher to report it.
	for _, repo := range disabled {
		p.publishHealth(run, repo, "", GitHubState{Health: GitHubDisabled})
	}

	<-ctx.Done()
	p.mu.Lock()
	p.run = nil
	p.mu.Unlock()
	run.wg.Wait()
	return nil
}

// start runs rp's poll loop. The caller holds p.mu and p.run is set.
func (p *githubPoller) start(rp *repoPoll) {
	run := p.run
	run.wg.Add(1)
	go func() {
		defer run.wg.Done()
		p.loop(run, rp)
	}()
}

// loop polls one repository: at once, then after each delay, and early when
// the target changes, but never before notBefore.
func (p *githubPoller) loop(run *pollRun, rp *repoPoll) {
	due := p.clock.Now()
	for {
		c, release := p.clock.Timer(max(due.Sub(p.clock.Now()), 0))
		select {
		case <-run.ctx.Done():
			release()
			return
		case <-rp.done:
			release()
			return
		case <-rp.wake:
			release()
			due = p.wakeDue(rp, due)
			continue
		case <-c:
		}
		release()
		if until := p.holdUntil(); p.clock.Now().Before(until) {
			due = until
			continue
		}
		delay := p.poll(run, rp)
		if run.ctx.Err() != nil {
			return
		}
		due = p.clock.Now().Add(delay)
	}
}

// wakeDue moves the next poll forward to now, when the repository may be
// polled again, and never past due.
func (p *githubPoller) wakeDue(rp *repoPoll, due time.Time) time.Time {
	at := p.clock.Now()
	w := rp.work
	for _, floor := range []time.Time{w.lastStart.Add(githubMinGap), w.blockedUntil, p.holdUntil()} {
		if at.Before(floor) {
			at = floor
		}
	}
	if at.Before(due) {
		return at
	}
	return due
}

func (p *githubPoller) holdUntil() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hold
}

// seeRate records the latest primary rate limit seen on any response, and
// holds every repository back when the limit is exhausted or GitHub said
// when to come back.
func (p *githubPoller) seeRate(m GitHubMeta, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m.RateLimit.Limit > 0 {
		p.rate = m.RateLimit
	}
	var until time.Time
	switch {
	case m.RetryAfter > 0:
		until = now.Add(m.RetryAfter)
	case m.RateLimit.Limit > 0 && m.RateLimit.Remaining == 0 && m.RateLimit.Reset.After(now):
		until = m.RateLimit.Reset
	}
	if until.After(p.hold) {
		p.hold = until
	}
}

func (p *githubPoller) currentRate() RateLimit {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rate
}

// publishHealth publishes the transient gh.health.changed for repo. The
// activity projection recomputes on it, so the console leaves "unavailable"
// after the first poll and follows rate limits and recoveries.
func (p *githubPoller) publishHealth(run *pollRun, repo bus.RepoID, previous GitHubHealth, s GitHubState) {
	b, err := json.Marshal(bus.GHHealthChanged{Health: string(s.Health), Previous: string(previous), RetryAt: s.RetryAt.UTC()})
	if err != nil {
		return
	}
	e := bus.Event{Type: bus.TypeGHHealthChanged, Source: "watch.github", Repo: repo, Time: p.clock.Now().UTC(), Payload: b}
	if _, err := run.pub.Publish(run.ctx, e); err != nil && run.ctx.Err() == nil {
		p.report(err)
	}
}

func (p *githubPoller) report(err error) {
	if err != nil && p.opts.OnError != nil {
		p.opts.OnError(fmt.Errorf("github poller: %w", err))
	}
}
