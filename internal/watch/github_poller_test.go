package watch_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/gh"
	"github.com/ryanhelms/agentflow/internal/watch"
)

var (
	t0   = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	repo = bus.RepoID("0123456789abcdef")
	// featureWT is a served worktree on branch "feature", bound to issue 35.
	featureWT = bus.WorktreeID("feature-0a1b2c3d")
)

const slug = "acme/repo"

// ghHarness runs one GitHubPoller against a fake GitHub with a fake clock.
type ghHarness struct {
	t      *testing.T
	clock  *fakeClock
	gh     *fakeGitHub
	rec    *recorder
	poller watch.GitHubPoller
	target watch.GitHubTarget
	cancel context.CancelFunc
	done   chan error

	apiMu sync.Mutex
	apis  map[string]*fakeGitHub // by slug
}

func newGHHarness(t *testing.T, setup func(f *fakeGitHub)) *ghHarness {
	t.Helper()
	f := newFakeGitHub(t, slug)
	if setup != nil {
		f.set(setup)
	}
	h := &ghHarness{
		t: t, clock: newFakeClock(t0), gh: f, rec: &recorder{t: t},
		apis: map[string]*fakeGitHub{slug: f},
		target: watch.GitHubTarget{
			Repo:     repo,
			Slug:     slug,
			Branches: map[bus.WorktreeID]string{featureWT: "feature"},
			Issues:   map[bus.WorktreeID]int{featureWT: 35},
			Watched:  true,
		},
	}
	h.poller = watch.NewGitHubPoller(watch.GitHubOptions{
		API: func(_ context.Context, tg watch.GitHubTarget) (watch.GitHubAPI, error) {
			h.apiMu.Lock()
			defer h.apiMu.Unlock()
			return h.apis[tg.Slug].client(), nil
		},
		Classify: gh.Classify,
		Clock:    h.clock,
		OnError:  func(err error) { t.Logf("poller: %v", err) },
	})
	return h
}

// start sets the target, runs the poller and waits for its first cycle.
func (h *ghHarness) start() {
	h.t.Helper()
	h.poller.Set(h.target)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- h.poller.Run(ctx, h.rec) }()
	h.t.Cleanup(h.stop)
	h.waitCreated(2) // Timer(0) for the first poll, then the timer after it
}

func (h *ghHarness) stop() {
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		h.t.Error("poller did not stop")
	}
}

func (h *ghHarness) waitCreated(n int) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if created, _ := h.clock.stats(); created >= n {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("poll loop did not reach timer %d", n)
		}
		time.Sleep(time.Millisecond)
	}
}

// delay is the wait the loop asked for after its latest cycle.
func (h *ghHarness) delay() time.Duration {
	_, d := h.clock.stats()
	return d
}

// next advances to the next scheduled poll and waits for its cycle to end.
func (h *ghHarness) next() time.Duration {
	h.t.Helper()
	created, d := h.clock.stats()
	h.clock.Advance(d)
	h.waitCreated(created + 1)
	return h.delay()
}

func (h *ghHarness) state() watch.GitHubState {
	h.t.Helper()
	s, ok := h.poller.GitHubState(repo)
	if !ok {
		h.t.Fatal("no state for the repository")
	}
	return s
}

func requests(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("requests =\n  %q\nwant\n  %q", got, want)
	}
}

func typesOf(events []bus.Event) []bus.Type {
	var out []bus.Type
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

// quiet is the usual repository: two open pull requests, one on the served
// branch with passing checks, and one bound issue. Nothing updated recently,
// so the poller is idle.
func quiet(f *fakeGitHub) {
	f.pulls = []map[string]any{
		pullFixture(slug, 1, "feature", sha("a"), t0.Add(-time.Hour)),
		pullFixture(slug, 2, "other", sha("b"), t0.Add(-2*time.Hour)),
	}
	f.runs[sha("a")] = []map[string]any{runFixture("build", "completed", "success"), runFixture("test", "completed", "success")}
	f.statuses[sha("a")] = []map[string]any{{"context": "legacy", "state": "success"}}
	f.issues[35] = issueFixture(slug, 35, "open", t0.Add(-3*time.Hour))
	f.graphql = map[int][2]string{1: {"APPROVED", "CLEAN"}, 2: {"REVIEW_REQUIRED", "BLOCKED"}}
}

// The first poll records what GitHub says without events. A later cycle with
// nothing new costs one conditional request per resource and no GraphQL.
func TestGitHubPollerSeedsSilentlyAndUnchangedCyclesCostOnlyConditionalRequests(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.start()

	requests(t, h.gh.take(), "pulls 200", "runs:aaaa 200", "status:aaaa 200", "issue:35 200", "graphql:1,2 200")
	if got := h.rec.take(); len(got) != 0 {
		t.Fatalf("first poll published %v", typesOf(got))
	}
	if d := h.delay(); d != watch.GitHubIdle {
		t.Fatalf("delay after the first poll = %v, want %v", d, watch.GitHubIdle)
	}
	s := h.state()
	if s.Health != watch.GitHubOK || len(s.Pulls) != 2 {
		t.Fatalf("state = %+v", s)
	}
	p := s.Pulls[0]
	if p.Number != 1 || p.State != bus.PullOpen || p.HeadRef != "feature" || p.ReviewDecision != "approved" || p.MergeState != "clean" || p.Previous != "" {
		t.Fatalf("pull 1 = %+v", p)
	}
	if s.Pulls[1].ReviewDecision != "review_required" || s.Pulls[1].MergeState != "blocked" {
		t.Fatalf("pull 2 = %+v", s.Pulls[1])
	}
	ci := s.CI[sha("a")]
	if ci.Pull != 1 || ci.State != bus.CISuccess || ci.Counts != (bus.CICounts{Total: 3, Success: 3}) {
		t.Fatalf("ci = %+v", ci)
	}
	if _, ok := s.CI[sha("b")]; ok {
		t.Fatal("checks of an untracked head were polled")
	}
	if is := s.Issues[35]; is.State != "open" || is.Title != "Issue 35" || !slices.Equal(is.Labels, []string{"bug"}) {
		t.Fatalf("issue = %+v", is)
	}

	for range 3 {
		h.next()
		requests(t, h.gh.take(), "pulls 304", "runs:aaaa 304", "status:aaaa 304", "issue:35 304")
		if got := h.rec.take(); len(got) != 0 {
			t.Fatalf("unchanged cycle published %v", typesOf(got))
		}
	}
}

// Only a changed pull request is asked about in GraphQL, and its event
// carries the new review decision and merge state.
func TestGitHubPollerEnrichesOnlyChangedPulls(t *testing.T) {
	h := newGHHarness(t, func(f *fakeGitHub) {
		quiet(f)
		f.pulls = append(f.pulls, pullFixture(slug, 3, "third", sha("c"), t0.Add(-4*time.Hour)))
		f.graphql[3] = [2]string{"", "CLEAN"}
	})
	h.start()
	h.gh.take()
	h.rec.take()

	h.gh.set(func(f *fakeGitHub) {
		f.pulls[1]["title"] = "Pull 2, renamed"
		f.pulls[1]["updated_at"] = t0.Add(-time.Minute).Format(time.RFC3339)
		f.graphql[2] = [2]string{"CHANGES_REQUESTED", "BLOCKED"}
	})
	h.next()
	requests(t, h.gh.take(), "pulls 200", "runs:aaaa 304", "status:aaaa 304", "issue:35 304", "graphql:2 200")
	events := h.rec.take()
	if len(events) != 1 || events[0].Type != bus.TypeGHPullUpdated {
		t.Fatalf("events = %v", typesOf(events))
	}
	e := events[0]
	p := payload[bus.GHPullUpdated](t, e)
	if p.Number != 2 || p.Title != "Pull 2, renamed" || p.Previous != bus.PullOpen || p.ReviewDecision != "changes_requested" || p.MergeState != "blocked" {
		t.Fatalf("payload = %+v", p)
	}
	if e.Worktree != "" || e.Repo != repo || e.Source != "watch.github" {
		t.Fatalf("envelope = %+v", e)
	}

	// A new pull request is reported with no previous state.
	h.gh.set(func(f *fakeGitHub) {
		f.pulls = append([]map[string]any{pullFixture(slug, 4, "fourth", sha("d"), t0.Add(time.Minute))}, f.pulls...)
		f.graphql[4] = [2]string{"", "DRAFT"}
	})
	h.next()
	requests(t, h.gh.take(), "pulls 200", "runs:aaaa 304", "status:aaaa 304", "issue:35 304", "graphql:4 200")
	events = h.rec.take()
	if len(events) != 1 {
		t.Fatalf("events = %v", typesOf(events))
	}
	if p := payload[bus.GHPullUpdated](t, events[0]); p.Number != 4 || p.Previous != "" || p.MergeState != "draft" {
		t.Fatalf("payload = %+v", p)
	}
}

// A tracked head's checks changing publishes gh.ci.updated on the worktree,
// and refreshes that pull request's merge state in GraphQL.
func TestGitHubPollerReportsChecksAndSpeedsUpWhilePending(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.start()
	h.gh.take()

	h.gh.set(func(f *fakeGitHub) {
		f.runs[sha("a")][1] = runFixture("test", "in_progress", "")
		f.graphql[1] = [2]string{"APPROVED", "BLOCKED"}
	})
	delay := h.next()
	requests(t, h.gh.take(), "pulls 304", "runs:aaaa 200", "status:aaaa 304", "issue:35 304", "graphql:1 200")
	if delay != watch.GitHubActive {
		t.Fatalf("delay while a check runs = %v, want %v", delay, watch.GitHubActive)
	}
	events := h.rec.take()
	if !slices.Equal(typesOf(events), []bus.Type{bus.TypeGHPullUpdated, bus.TypeGHCIUpdated}) {
		t.Fatalf("events = %v", typesOf(events))
	}
	pr := payload[bus.GHPullUpdated](t, events[0])
	if pr.Number != 1 || pr.MergeState != "blocked" || pr.Previous != bus.PullOpen || events[0].Worktree != featureWT {
		t.Fatalf("pull = %+v on %q", pr, events[0].Worktree)
	}
	ci := payload[bus.GHCIUpdated](t, events[1])
	if ci.Pull != 1 || ci.State != bus.CIPending || ci.Previous != bus.CISuccess || ci.HeadSHA != sha("a") ||
		ci.Counts != (bus.CICounts{Total: 3, Pending: 1, Success: 2}) || events[1].Worktree != featureWT {
		t.Fatalf("ci = %+v on %q", ci, events[1].Worktree)
	}

	// The check fails: the failure wins, and names the check.
	h.gh.set(func(f *fakeGitHub) { f.runs[sha("a")][1] = runFixture("test", "completed", "failure") })
	h.next()
	h.gh.take()
	events = h.rec.take()
	var failed *bus.GHCIUpdated
	for _, e := range events {
		if e.Type == bus.TypeGHCIUpdated {
			v := payload[bus.GHCIUpdated](t, e)
			failed = &v
		}
	}
	if failed == nil || failed.State != bus.CIFailure || failed.Previous != bus.CIPending || !slices.Equal(failed.Failed, []string{"test"}) {
		t.Fatalf("ci after failure = %+v", failed)
	}
}

// A pull request that leaves the open list is looked up once, and reported
// as merged. Nothing else is asked.
func TestGitHubPollerReportsMergedPull(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.start()
	h.gh.take()

	h.gh.set(func(f *fakeGitHub) {
		f.pulls = f.pulls[:1] // pull 2 ("other") is gone
		merged := pullFixture(slug, 2, "other", sha("b"), t0.Add(-time.Minute))
		merged["state"] = "closed"
		merged["merged_at"] = t0.Add(-time.Minute).Format(time.RFC3339)
		f.closed["acme:other"] = []map[string]any{merged}
	})
	h.next()
	requests(t, h.gh.take(), "pulls 200", "closed:acme:other 200", "runs:aaaa 304", "status:aaaa 304", "issue:35 304")
	events := h.rec.take()
	if len(events) != 1 {
		t.Fatalf("events = %v", typesOf(events))
	}
	if p := payload[bus.GHPullUpdated](t, events[0]); p.Number != 2 || p.State != bus.PullMerged || p.Previous != bus.PullOpen || p.MergeState != "" {
		t.Fatalf("payload = %+v", p)
	}
	s := h.state()
	if len(s.Pulls) != 2 || s.Pulls[0].State != bus.PullOpen || s.Pulls[1].State != bus.PullMerged || s.Pulls[1].Number != 2 {
		t.Fatalf("pulls = %+v", s.Pulls)
	}

	// The list is unchanged, so nothing is asked again.
	h.next()
	requests(t, h.gh.take(), "pulls 304", "runs:aaaa 304", "status:aaaa 304", "issue:35 304")
}

// A page of open pull requests can push an old one out of view. When the
// closed lookup finds nothing, the pull request stays open and is not asked
// about again until the list changes.
func TestGitHubPollerKeepsPullBeyondFullPage(t *testing.T) {
	h := newGHHarness(t, func(f *fakeGitHub) {
		quiet(f)
		f.pulls = nil
		for n := 1; n <= 50; n++ {
			branch := "b" + strconv.Itoa(n)
			f.pulls = append(f.pulls, pullFixture(slug, n, branch, sha("e"), t0.Add(-time.Duration(n)*time.Hour)))
		}
		f.pulls[0]["head"].(map[string]any)["ref"] = "feature"
		f.pulls[0]["head"].(map[string]any)["sha"] = sha("a")
		f.pulls[0]["head"].(map[string]any)["label"] = "acme:feature"
	})
	h.start()
	h.gh.take()

	// Pull 50 is pushed out of the page by a newer one.
	h.gh.set(func(f *fakeGitHub) {
		f.pulls = append([]map[string]any{pullFixture(slug, 51, "b51", sha("e"), t0.Add(time.Minute))}, f.pulls[:49]...)
	})
	h.next()
	got := h.gh.take()
	if !slices.Contains(got, "closed:acme:b50 200") {
		t.Fatalf("requests = %q, want a closed lookup for pull 50", got)
	}
	for _, e := range h.rec.take() {
		if p := payload[bus.GHPullUpdated](t, e); p.Number == 50 {
			t.Fatalf("pull 50 was reported as %+v", p)
		}
	}
	if s := h.state(); !slices.ContainsFunc(s.Pulls, func(p bus.GHPullUpdated) bool { return p.Number == 50 && p.State == bus.PullOpen }) {
		t.Fatal("pull 50 left the cache")
	}
	h.next()
	for _, r := range h.gh.take() {
		if strings.HasPrefix(r, "closed:") {
			t.Fatalf("asked again: %s", r)
		}
	}
}

func TestGitHubPollerReportsIssueChanges(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.start()
	h.gh.take()

	h.gh.set(func(f *fakeGitHub) {
		f.issues[35]["state"] = "closed"
		f.issues[35]["state_reason"] = "completed"
		f.issues[35]["updated_at"] = t0.Add(time.Minute).Format(time.RFC3339)
		f.issues[35]["labels"] = []map[string]any{{"name": "bug"}, {"name": "done"}}
	})
	h.next()
	requests(t, h.gh.take(), "pulls 304", "runs:aaaa 304", "status:aaaa 304", "issue:35 200")
	events := h.rec.take()
	if len(events) != 1 || events[0].Type != bus.TypeGHIssueUpdated || events[0].Worktree != featureWT {
		t.Fatalf("events = %+v", events)
	}
	is := payload[bus.GHIssueUpdated](t, events[0])
	if is.Number != 35 || is.State != "closed" || is.StateReason != "completed" || is.Previous != "open" || !slices.Equal(is.Labels, []string{"bug", "done"}) || !slices.Equal(is.Assignees, []string{"dev"}) {
		t.Fatalf("issue = %+v", is)
	}
}

// An issue that does not exist is not a failure of the repository, and is
// left alone for a while.
func TestGitHubPollerSkipsMissingIssue(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.target.Issues = map[bus.WorktreeID]int{featureWT: 99}
	h.start()
	requests(t, h.gh.take(), "pulls 200", "runs:aaaa 200", "status:aaaa 200", "issue:99 404", "graphql:1,2 200")
	if s := h.state(); s.Health != watch.GitHubOK || len(s.Issues) != 0 {
		t.Fatalf("state = %+v", s)
	}
	if d := h.delay(); d != watch.GitHubIdle {
		t.Fatalf("delay = %v", d)
	}
	h.next()
	requests(t, h.gh.take(), "pulls 304", "runs:aaaa 304", "status:aaaa 304")
	h.clock.Advance(githubMissingRetryForTest)
	h.gh.set(func(f *fakeGitHub) { f.issues[99] = issueFixture(slug, 99, "open", t0) })
	h.next()
	requests(t, h.gh.take(), "pulls 304", "runs:aaaa 304", "status:aaaa 304", "issue:99 200")
	if events := h.rec.take(); len(events) != 1 || events[0].Type != bus.TypeGHIssueUpdated {
		t.Fatalf("events = %v", typesOf(events))
	}
}

// githubMissingRetryForTest is a little over the poller's 404 retry wait.
const githubMissingRetryForTest = 11 * time.Minute

// Text from GitHub is cut to the payload limits.
func TestGitHubPollerTruncatesPayloads(t *testing.T) {
	long := strings.Repeat("é", 200) // 400 bytes
	var labels []map[string]any
	for i := range 30 {
		labels = append(labels, map[string]any{"name": "label-" + strconv.Itoa(i) + long})
	}
	h := newGHHarness(t, quiet)
	h.start()
	h.gh.take()
	h.gh.set(func(f *fakeGitHub) {
		f.pulls[0]["title"] = long
		f.pulls[0]["updated_at"] = t0.Add(time.Minute).Format(time.RFC3339)
		f.issues[35]["labels"] = labels
		f.issues[35]["updated_at"] = t0.Add(time.Minute).Format(time.RFC3339)
	})
	h.next()
	for _, e := range h.rec.take() {
		switch e.Type {
		case bus.TypeGHPullUpdated:
			if p := payload[bus.GHPullUpdated](t, e); len(p.Title) > bus.MaxPayloadText || p.Title == "" || !strings.HasPrefix(long, p.Title) {
				t.Fatalf("title is %d bytes", len(p.Title))
			}
		case bus.TypeGHIssueUpdated:
			is := payload[bus.GHIssueUpdated](t, e)
			if len(is.Labels) != bus.MaxPayloadNames {
				t.Fatalf("%d labels", len(is.Labels))
			}
			for _, l := range is.Labels {
				if len(l) > bus.MaxPayloadText {
					t.Fatalf("label is %d bytes", len(l))
				}
			}
		}
	}
}

// A pull request from a fork whose branch has the name of a served branch is
// not tied to the worktree, and its checks are not polled.
func TestGitHubPollerIgnoresForkWithSameBranchName(t *testing.T) {
	h := newGHHarness(t, func(f *fakeGitHub) {
		quiet(f)
		fork := pullFixture(slug, 3, "feature", sha("f"), t0.Add(-time.Hour))
		fork["head"].(map[string]any)["repo"] = map[string]any{"full_name": "someone/repo"}
		fork["head"].(map[string]any)["label"] = "someone:feature"
		f.pulls = append(f.pulls, fork)
		f.graphql[3] = [2]string{"", "CLEAN"}
	})
	h.start()
	for _, r := range h.gh.take() {
		if strings.Contains(r, "ffff") {
			t.Fatalf("polled the fork's checks: %s", r)
		}
	}
	h.gh.set(func(f *fakeGitHub) {
		f.pulls[2]["title"] = "changed"
		f.pulls[2]["updated_at"] = t0.Add(time.Minute).Format(time.RFC3339)
	})
	h.next()
	events := h.rec.take()
	if len(events) != 1 || events[0].Worktree != "" {
		t.Fatalf("events = %+v", events)
	}
}

// ---- intervals ----

func TestGitHubPollerIntervals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *fakeGitHub)
		watch bool
		want  time.Duration
	}{
		{"idle", quiet, true, watch.GitHubIdle},
		{"unwatched", quiet, false, watch.GitHubDormant},
		{"pending check", func(f *fakeGitHub) {
			quiet(f)
			f.runs[sha("a")][0] = runFixture("build", "queued", "")
		}, true, watch.GitHubActive},
		{"recent update of a tracked pull request", func(f *fakeGitHub) {
			quiet(f)
			f.pulls[0]["updated_at"] = t0.Add(-5 * time.Minute).Format(time.RFC3339)
		}, true, watch.GitHubActive},
		{"recent update of an untracked pull request", func(f *fakeGitHub) {
			quiet(f)
			f.pulls[1]["updated_at"] = t0.Add(-time.Minute).Format(time.RFC3339)
		}, true, watch.GitHubIdle},
		{"update older than the window", func(f *fakeGitHub) {
			quiet(f)
			f.pulls[0]["updated_at"] = t0.Add(-watch.GitHubActiveWindow - time.Second).Format(time.RFC3339)
		}, true, watch.GitHubIdle},
		{"poll interval floor", func(f *fakeGitHub) {
			quiet(f)
			f.headers["X-Poll-Interval"] = "120"
		}, true, 2 * time.Minute},
		{"poll interval floor above active", func(f *fakeGitHub) {
			quiet(f)
			f.runs[sha("a")][0] = runFixture("build", "queued", "")
			f.headers["X-Poll-Interval"] = "30"
		}, true, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGHHarness(t, tc.setup)
			h.target.Watched = tc.watch
			h.start()
			if got := h.delay(); got != tc.want {
				t.Fatalf("delay = %v, want %v", got, tc.want)
			}
		})
	}
}

// Under 20% of the primary limit, the poller slows so the rest lasts until
// the limit resets.
func TestGitHubPollerStretchesBelowTwentyPercent(t *testing.T) {
	reset := t0.Add(time.Hour)
	h := newGHHarness(t, func(f *fakeGitHub) {
		quiet(f)
		f.headers["X-RateLimit-Limit"] = "5000"
		f.headers["X-RateLimit-Remaining"] = "4000"
		f.headers["X-RateLimit-Reset"] = strconv.FormatInt(reset.Unix(), 10)
	})
	h.start()
	if d := h.delay(); d != watch.GitHubIdle {
		t.Fatalf("with 80%% left: %v", d)
	}
	h.gh.set(func(f *fakeGitHub) {
		f.headers["X-RateLimit-Remaining"] = "10"
		f.pulls[1]["title"] = "touched" // a 200, not a 304
	})
	got := h.next()
	// The cycle ran at the current fake time, so the rest of the hour is
	// spread over the ten requests that remain.
	if want := reset.Sub(h.clock.Now()) / 10; got != want {
		t.Fatalf("with 10%% left: %v, want %v", got, want)
	}
	if s := h.state(); s.Health != watch.GitHubOK {
		t.Fatalf("health = %s", s.Health)
	}
}

// ---- failures ----

func TestGitHubPollerBacksOffAfterFailures(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.start()
	h.gh.take()
	h.gh.set(func(f *fakeGitHub) { f.fail["pulls"] = 502 })

	var delays []time.Duration
	for range 7 {
		delays = append(delays, h.next())
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	if !slices.Equal(delays, want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
	s := h.state()
	if s.Health != watch.GitHubUnavailable || s.RetryAt.IsZero() || len(s.Pulls) != 2 {
		t.Fatalf("state = %+v", s)
	}
	if !s.RetryAt.Equal(h.clock.Now().Add(15 * time.Minute)) {
		t.Fatalf("retry at %v", s.RetryAt)
	}
	if got := h.rec.take(); len(got) != 0 {
		t.Fatalf("failures published %v", typesOf(got))
	}
	// Only the pull request list was asked while it failed.
	for _, r := range h.gh.take() {
		if r != "pulls 502" {
			t.Fatalf("request after a failed list: %s", r)
		}
	}

	h.gh.set(func(f *fakeGitHub) { delete(f.fail, "pulls") })
	if d := h.next(); d != watch.GitHubIdle {
		t.Fatalf("delay after recovery = %v", d)
	}
	if s := h.state(); s.Health != watch.GitHubOK || !s.RetryAt.IsZero() {
		t.Fatalf("state after recovery = %+v", s)
	}
}

func TestGitHubPollerAuthFailureIsUnavailable(t *testing.T) {
	h := newGHHarness(t, func(f *fakeGitHub) {
		quiet(f)
		f.fail["pulls"] = 401
	})
	h.start()
	requests(t, h.gh.take(), "pulls 401")
	if s := h.state(); s.Health != watch.GitHubUnavailable || len(s.Pulls) != 0 {
		t.Fatalf("state = %+v", s)
	}
	if d := h.delay(); d != watch.GitHubIdle {
		t.Fatalf("delay = %v", d)
	}
}

// A secondary rate limit waits as long as Retry-After says, and publishes
// nothing.
func TestGitHubPollerHonoursRetryAfter(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.start()
	h.gh.take()
	h.gh.set(func(f *fakeGitHub) {
		f.fail["pulls"] = 403
		f.failHdr["Retry-After"] = "300"
	})
	if d := h.next(); d != 5*time.Minute {
		t.Fatalf("delay = %v, want Retry-After", d)
	}
	s := h.state()
	if s.Health != watch.GitHubRateLimited || !s.RetryAt.Equal(h.clock.Now().Add(5*time.Minute)) {
		t.Fatalf("state = %+v", s)
	}
	requests(t, h.gh.take(), "pulls 403")

	// A target change cannot bring the next poll forward.
	h.target.Branches = map[bus.WorktreeID]string{featureWT: "feature", "main": "main"}
	h.poller.Set(h.target)
	created, _ := h.clock.stats()
	h.waitCreated(created + 1)
	if d := h.delay(); d != 5*time.Minute {
		t.Fatalf("delay after a target change = %v, want 5m0s", d)
	}
	requests(t, h.gh.take())
}

// An exhausted primary limit waits for the reset, and holds every
// repository back, not only the one that hit it.
func TestGitHubPollerWaitsForResetAcrossRepositories(t *testing.T) {
	reset := t0.Add(20 * time.Minute)
	h := newGHHarness(t, func(f *fakeGitHub) {
		quiet(f)
		f.fail["pulls"] = 403
		f.failHdr["X-RateLimit-Limit"] = "5000"
		f.failHdr["X-RateLimit-Remaining"] = "0"
		f.failHdr["X-RateLimit-Reset"] = strconv.FormatInt(reset.Unix(), 10)
	})
	h.start()
	if d := h.delay(); d != 20*time.Minute {
		t.Fatalf("delay = %v, want the time to the reset", d)
	}
	if s := h.state(); s.Health != watch.GitHubRateLimited {
		t.Fatalf("health = %s", s.Health)
	}

	// A second repository added now makes no request before the reset.
	other := newFakeGitHub(t, "acme/other")
	other.set(func(f *fakeGitHub) {
		f.pulls = []map[string]any{pullFixture("acme/other", 9, "x", sha("9"), t0.Add(-time.Hour))}
		f.graphql[9] = [2]string{"", "CLEAN"}
	})
	otherRepo := bus.RepoID("fedcba9876543210")
	h.apiMu.Lock()
	h.apis["acme/other"] = other
	h.apiMu.Unlock()
	h.poller.Set(watch.GitHubTarget{Repo: otherRepo, Slug: "acme/other", Watched: true})
	time.Sleep(100 * time.Millisecond)
	if got := other.take(); len(got) != 0 {
		t.Fatalf("the second repository polled during the limit: %q", got)
	}

	// After the reset, it polls.
	created, _ := h.clock.stats()
	h.clock.Advance(20 * time.Minute)
	h.waitCreated(created + 2)
	if got := other.take(); len(got) == 0 {
		t.Fatal("the second repository did not poll after the reset")
	}
}

// A GraphQL failure does not fail the repository. The pull request is tried
// a few more times and then left until it changes.
func TestGitHubPollerEnrichmentFailureIsContained(t *testing.T) {
	h := newGHHarness(t, func(f *fakeGitHub) {
		quiet(f)
		f.gqlFail = true
	})
	h.start()
	if s := h.state(); s.Health != watch.GitHubOK || s.Pulls[0].ReviewDecision != "" {
		t.Fatalf("state = %+v", s)
	}
	if d := h.delay(); d != watch.GitHubIdle {
		t.Fatalf("delay = %v", d)
	}
	graphql := 0
	for _, r := range h.gh.take() {
		if strings.HasPrefix(r, "graphql") {
			graphql++
		}
	}
	for range 5 {
		h.next()
		for _, r := range h.gh.take() {
			if strings.HasPrefix(r, "graphql") {
				graphql++
			}
		}
	}
	h.gh.take()
	if graphql != 3 {
		t.Fatalf("GraphQL was tried %d times, want 3", graphql)
	}
}

// ---- targets and lifecycle ----

// A branch switch or a new viewer wakes the loop early, but never sooner
// than the minimum gap.
func TestGitHubPollerWakesOnTargetChange(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.start()
	h.gh.take()

	h.target.Branches = map[bus.WorktreeID]string{featureWT: "other"}
	h.poller.Set(h.target)
	created, _ := h.clock.stats()
	h.waitCreated(created + 1)
	if d := h.delay(); d != 5*time.Second {
		t.Fatalf("early poll in %v, want the 5s minimum gap", d)
	}
	h.next()
	got := h.gh.take()
	if !slices.Contains(got, "runs:bbbb 200") || slices.Contains(got, "runs:aaaa 304") {
		t.Fatalf("requests = %q: the new branch's checks were not polled", got)
	}
	if s := h.state(); s.CI[sha("b")].Pull != 2 || len(s.CI) != 1 {
		t.Fatalf("ci = %+v", s.CI)
	}
	// The new head's first checks are reported, with no previous state.
	events := h.rec.take()
	var ci *bus.GHCIUpdated
	for _, e := range events {
		if e.Type == bus.TypeGHCIUpdated {
			v := payload[bus.GHCIUpdated](t, e)
			ci = &v
		}
	}
	if ci == nil || ci.Pull != 2 || ci.Previous != "" {
		t.Fatalf("events = %v, ci = %+v", typesOf(events), ci)
	}
}

func TestGitHubPollerDisablesRepositoryWithoutSlug(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.start()
	h.gh.take()
	h.poller.Set(watch.GitHubTarget{Repo: repo})
	s, ok := h.poller.GitHubState(repo)
	if !ok || s.Health != watch.GitHubDisabled || len(s.Pulls) != 0 {
		t.Fatalf("state = %+v, %v", s, ok)
	}
	h.clock.Advance(time.Hour)
	time.Sleep(50 * time.Millisecond)
	if got := h.gh.take(); len(got) != 0 {
		t.Fatalf("polled a disabled repository: %q", got)
	}
	if _, ok := h.poller.GitHubState("ffffffffffffffff"); ok {
		t.Fatal("state for an unknown repository")
	}
}

func TestGitHubPollerRejectsUnsafeSlug(t *testing.T) {
	for _, bad := range []string{"../etc", "a/..", "a/b/c", "a b/c", "owner", "a/b?x=1"} {
		h := newGHHarness(t, quiet)
		h.target.Slug = bad
		h.poller.Set(h.target)
		if s, ok := h.poller.GitHubState(repo); !ok || s.Health != watch.GitHubDisabled {
			t.Errorf("slug %q: state = %+v, %v", bad, s, ok)
		}
	}
}

func TestGitHubPollerStateIsACopy(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.start()
	s := h.state()
	s.Pulls[0].Title = "mutated"
	delete(s.CI, sha("a"))
	again := h.state()
	if again.Pulls[0].Title == "mutated" || len(again.CI) != 1 {
		t.Fatal("a caller changed the cache")
	}
}

func TestGitHubPollerRunTwiceFails(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.start()
	if err := h.poller.Run(context.Background(), h.rec); err == nil {
		t.Fatal("second Run succeeded")
	}
}

func TestGitHubPollerAPIErrorBacksOff(t *testing.T) {
	f := newFakeGitHub(t, slug)
	clock := newFakeClock(t0)
	var calls int
	p := watch.NewGitHubPoller(watch.GitHubOptions{
		API: func(context.Context, watch.GitHubTarget) (watch.GitHubAPI, error) {
			calls++
			return nil, errors.New("gh is not installed")
		},
		Clock: clock,
	})
	p.Set(watch.GitHubTarget{Repo: repo, Slug: slug, Watched: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx, &recorder{t: t}) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run = %v", err)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n, _ := clock.stats(); n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no cycle")
		}
		time.Sleep(time.Millisecond)
	}
	if _, d := clock.stats(); d != time.Minute {
		t.Fatalf("delay = %v", d)
	}
	if s, ok := p.GitHubState(repo); !ok || s.Health != watch.GitHubUnavailable {
		t.Fatalf("state = %+v, %v", s, ok)
	}
	if calls != 1 || len(f.take()) != 0 {
		t.Fatalf("calls = %d", calls)
	}
}

// The first poll and every health change publish gh.health.changed, so the
// activity projection leaves "unavailable" without waiting for a data event.
// A cycle that keeps the same health publishes nothing.
func TestGitHubPollerPublishesHealthChanges(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.start()
	if got := h.rec.takeHealth(); len(got) != 1 || got[0].Health != "ok" || got[0].Previous != "" {
		t.Fatalf("first poll health = %+v", got)
	}
	h.next()
	if got := h.rec.takeHealth(); len(got) != 0 {
		t.Fatalf("unchanged cycle published %+v", got)
	}

	// Rate limited, then recovered.
	h.gh.set(func(f *fakeGitHub) {
		f.fail["pulls"] = 403
		f.failHdr["Retry-After"] = "300"
	})
	h.next()
	got := h.rec.takeHealth()
	if len(got) != 1 || got[0].Health != "rate_limited" || got[0].Previous != "ok" ||
		!got[0].RetryAt.Equal(h.clock.Now().Add(5*time.Minute)) {
		t.Fatalf("rate limited health = %+v", got)
	}
	h.gh.set(func(f *fakeGitHub) {
		delete(f.fail, "pulls")
		delete(f.failHdr, "Retry-After")
	})
	h.next()
	if got := h.rec.takeHealth(); len(got) != 1 || got[0].Health != "ok" || got[0].Previous != "rate_limited" || !got[0].RetryAt.IsZero() {
		t.Fatalf("recovered health = %+v", got)
	}

	// Unavailable, twice in a row: one event.
	h.gh.set(func(f *fakeGitHub) { f.fail["pulls"] = 502 })
	h.next()
	h.next()
	if got := h.rec.takeHealth(); len(got) != 1 || got[0].Health != "unavailable" || got[0].Previous != "ok" {
		t.Fatalf("unavailable health = %+v", got)
	}

	// Losing the GitHub remote disables the repository, once.
	h.poller.Set(watch.GitHubTarget{Repo: repo})
	h.poller.Set(watch.GitHubTarget{Repo: repo})
	if got := h.rec.takeHealth(); len(got) != 1 || got[0].Health != "disabled" || got[0].Previous != "unavailable" {
		t.Fatalf("disabled health = %+v", got)
	}
	if got := h.rec.take(); len(got) != 0 {
		t.Fatalf("health changes published data events %v", typesOf(got))
	}
}

// A repository disabled before Run starts is reported when it starts.
func TestGitHubPollerReportsDisabledBeforeRun(t *testing.T) {
	h := newGHHarness(t, quiet)
	h.poller.Set(watch.GitHubTarget{Repo: repo})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.poller.Run(ctx, h.rec) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run = %v", err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := h.rec.takeHealth(); len(got) > 0 {
			if len(got) != 1 || got[0].Health != "disabled" {
				t.Fatalf("health = %+v", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no health event for a disabled repository")
		}
		time.Sleep(time.Millisecond)
	}
}
