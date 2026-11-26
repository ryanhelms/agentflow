package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// repoWork is everything one repository's poll goroutine remembers between
// cycles: ETags, the last decoded bodies and the values events compare
// against. Only that goroutine touches it; the published copy for readers is
// GitHubState.
type repoWork struct {
	slug string

	listETag    string
	listed      map[int]bool       // the numbers in open
	listFull    bool               // the page was full, so an open pull request may lie beyond it
	open        []int              // open pull request numbers, most recently updated first
	pulls       map[int]*pullRec   // open pull requests and the recent closed ones
	enrich      map[int]int        // pull requests waiting for GraphQL, with the tries so far
	goneChecked map[int]string     // closed lookups that found nothing, by list ETag
	changedAt   map[int]time.Time  // when a tracked pull request last changed
	ci          map[int]*ciRec     // checks of tracked heads, by pull request number
	ciFetch     map[string]*ciBody // last check bodies and ETags, by head SHA
	issues      map[int]*bus.GHIssueUpdated
	issueETag   map[int]string
	issueGone   map[int]time.Time // 404 issues: leave alone until

	// seeded is true once a section has completed one cycle. Until then
	// its changes only fill the cache.
	seeded struct{ pulls, ci, issues bool }

	failures     int
	lastStart    time.Time
	blockedUntil time.Time
	health       GitHubHealth
	retryAt      time.Time
}

func newRepoWork() *repoWork {
	return &repoWork{
		pulls:       map[int]*pullRec{},
		enrich:      map[int]int{},
		goneChecked: map[int]string{},
		changedAt:   map[int]time.Time{},
		ci:          map[int]*ciRec{},
		ciFetch:     map[string]*ciBody{},
		issues:      map[int]*bus.GHIssueUpdated{},
		issueETag:   map[int]string{},
		issueGone:   map[int]time.Time{},
		health:      GitHubOK,
	}
}

// ciRec is the checks state of one tracked pull request head.
type ciRec struct{ bus.GHCIUpdated }

// ciBody caches the two check responses of one commit.
type ciBody struct {
	runsETag, statusETag string
	runs                 []checkRunWire
	statuses             []statusWire
}

// githubCycle is one poll of one repository.
type githubCycle struct {
	p   *githubPoller
	ctx context.Context
	w   *repoWork
	t   GitHubTarget
	api GitHubAPI
	now time.Time

	meta    GitHubMeta // the headers that decide the next delay
	failed  bool       // some request failed
	limited bool       // some failure was a rate limit
	stopped bool       // a failure makes further requests pointless

	wasSeeded struct{ pulls, ci, issues bool } // the sections' state before this cycle

	pullChanges  map[int]bus.PullState // pull requests to report, with their previous state
	ciChanges    []int
	issueChanges []int
	enrichNow    map[int]bool
}

// poll runs one cycle for rp and returns the delay before the next one.
func (p *githubPoller) poll(run *pollRun, rp *repoPoll) time.Duration {
	rp.mu.Lock()
	t := cloneTarget(rp.target)
	rp.mu.Unlock()
	w := rp.work
	if w.slug != t.Slug {
		*w = *newRepoWork()
		w.slug = t.Slug
	}
	now := p.clock.Now()
	w.lastStart = now
	cy := &githubCycle{
		p: p, ctx: run.ctx, w: w, t: t, now: now,
		pullChanges: map[int]bus.PullState{},
		enrichNow:   map[int]bool{},
	}
	cy.wasSeeded = w.seeded
	api, err := p.opts.API(run.ctx, t)
	if err != nil {
		cy.fail(err, GitHubMeta{})
	} else {
		cy.api = api
		cy.pulls()
		if !cy.stopped {
			cy.checks()
		}
		if !cy.stopped {
			cy.issuesSection()
		}
		cy.queueEnrich()
		if !cy.stopped {
			cy.enrichPulls()
		}
	}
	if run.ctx.Err() != nil {
		return 0
	}
	cy.trimClosed()

	events := cy.events()
	delay := cy.finish()

	rp.mu.Lock()
	previous, had := rp.state.Health, rp.have
	rp.state = w.snapshot()
	rp.have = true
	state := rp.state
	rp.mu.Unlock()
	for _, e := range events {
		if _, err := run.pub.Publish(run.ctx, e); err != nil && run.ctx.Err() == nil {
			p.report(err)
		}
	}
	// The first poll and every health change publish nothing else, so the
	// activity projection would keep its old health without this.
	if !had {
		p.publishHealth(run, rp.repo, "", state)
	} else if state.Health != previous {
		p.publishHealth(run, rp.repo, previous, state)
	}
	return delay
}

// fail records a failed request. meta is what its response said.
func (cy *githubCycle) fail(err error, meta GitHubMeta) {
	if cy.ctx.Err() != nil {
		cy.stopped = true
		return
	}
	cy.failed = true
	kind := FailureOther
	if cy.p.opts.Classify != nil {
		kind = cy.p.opts.Classify(err)
	}
	if meta.RetryAfter > 0 || (meta.RateLimit.Limit > 0 && meta.RateLimit.Remaining == 0) {
		kind = FailureRateLimited
	}
	cy.observe(meta)
	switch kind {
	case FailureRateLimited:
		cy.limited = true
		cy.stopped = true
	case FailureAuth:
		cy.stopped = true
	}
	cy.p.report(err)
}

// failureKind classifies err without recording it.
func (cy *githubCycle) failureKind(err error) GitHubFailure {
	if cy.p.opts.Classify == nil {
		return FailureOther
	}
	return cy.p.opts.Classify(err)
}

// observe folds one response's headers into the cycle's meta.
func (cy *githubCycle) observe(m GitHubMeta) {
	cy.p.seeRate(m, cy.now)
	if m.RateLimit.Limit > 0 {
		cy.meta.RateLimit = m.RateLimit
	}
	cy.meta.PollInterval = max(cy.meta.PollInterval, m.PollInterval)
	cy.meta.RetryAfter = max(cy.meta.RetryAfter, m.RetryAfter)
}

// get is one conditional request. It reports whether out holds a fresh body.
// A failure is recorded; ok is then false and notModified false.
func (cy *githubCycle) get(path, etag string, out any) (meta GitHubMeta, fresh bool, err error) {
	meta, err = cy.api.GetConditional(cy.ctx, path, etag, out)
	if err != nil {
		return meta, false, err
	}
	cy.observe(meta)
	return meta, !meta.NotModified, nil
}

// ---- pull requests ----

func (cy *githubCycle) pulls() {
	w := cy.w
	path := fmt.Sprintf("/repos/%s/pulls?state=open&sort=updated&direction=desc&per_page=%d", w.slug, githubPageSize)
	var list []pullWire
	meta, fresh, err := cy.get(path, w.listETag, &list)
	if err != nil {
		cy.fail(err, meta)
		cy.stopped = true // everything else hangs off the list
		return
	}
	if fresh {
		cy.applyList(list, meta.ETag)
	}
	cy.resolveGone()
	if !cy.failed {
		w.seeded.pulls = true
	}
}

// applyList replaces the open pull requests with list.
func (cy *githubCycle) applyList(list []pullWire, etag string) {
	w := cy.w
	w.listETag = etag
	w.open = w.open[:0]
	listed := map[int]bool{}
	for _, pw := range list {
		rec := pw.rec()
		rec.State = bus.PullOpen
		n := rec.Number
		listed[n] = true
		w.open = append(w.open, n)
		prev, known := w.pulls[n]
		if known && prev.State == bus.PullOpen {
			rec.ReviewDecision, rec.MergeState = prev.ReviewDecision, prev.MergeState
		}
		if !known || prev.State != bus.PullOpen || !samePull(prev.GHPullUpdated, rec.GHPullUpdated) {
			var was bus.PullState
			if known {
				was = prev.State
			}
			cy.pullChanges[n] = was
			cy.enrichNow[n] = true
		}
		w.pulls[n] = &rec
	}
	w.listFull = len(list) >= githubPageSize
	w.listed = listed
}

// resolveGone asks about pull requests that left the open list: they were
// merged or closed. A full page may also have pushed an old one out of view,
// so a closed lookup that finds nothing keeps such a pull request open.
func (cy *githubCycle) resolveGone() {
	w := cy.w
	for _, n := range slices.Sorted(maps.Keys(w.pulls)) {
		rec := w.pulls[n]
		if rec.State != bus.PullOpen || w.listed[n] {
			continue
		}
		if tag, ok := w.goneChecked[n]; ok && tag == w.listETag {
			continue
		}
		closed, found := rec.GHPullUpdated, false
		var got []pullWire
		if rec.headLabel != "" {
			path := fmt.Sprintf("/repos/%s/pulls?head=%s&state=closed&per_page=1", w.slug, url.QueryEscape(rec.headLabel))
			meta, _, err := cy.get(path, "", &got)
			if err != nil {
				cy.fail(err, meta)
				if cy.stopped {
					return
				}
				continue
			}
			if len(got) == 1 && got[0].Number == n {
				found = true
				gr := got[0].rec()
				closed.State = gr.State
				if !gr.UpdatedAt.IsZero() {
					closed.UpdatedAt = gr.UpdatedAt
				}
			}
		}
		if !found && w.listFull {
			w.goneChecked[n] = w.listETag
			continue
		}
		if !found {
			// The list was complete, so it is not open; the details are lost.
			closed.State = bus.PullClosed
		}
		closed.MergeState = ""
		cy.pullChanges[n] = bus.PullOpen
		delete(w.enrich, n)
		w.pulls[n] = &pullRec{GHPullUpdated: closed, headRepo: rec.headRepo, headLabel: rec.headLabel}
	}
}

// trimClosed keeps only the most recent closed pull requests.
func (cy *githubCycle) trimClosed() {
	w := cy.w
	var closed []int
	for n, r := range w.pulls {
		if r.State != bus.PullOpen {
			closed = append(closed, n)
		}
	}
	if len(closed) <= githubKeepClosed {
		return
	}
	slices.SortFunc(closed, func(a, b int) int { return w.pulls[b].UpdatedAt.Compare(w.pulls[a].UpdatedAt) })
	for _, n := range closed[githubKeepClosed:] {
		delete(w.pulls, n)
		delete(w.goneChecked, n)
		delete(w.changedAt, n)
	}
}

// queueEnrich remembers which pull requests need GraphQL, so a cycle that
// stops early still enriches them next time.
func (cy *githubCycle) queueEnrich() {
	for n := range cy.enrichNow {
		if _, ok := cy.w.enrich[n]; !ok {
			cy.w.enrich[n] = 0
		}
	}
}

// enrichPulls asks GraphQL for the review decision and merge state of the
// pull requests that changed, in one query. GraphQL has no ETag, so nothing
// else is ever asked.
func (cy *githubCycle) enrichPulls() {
	w := cy.w
	var nums []int
	for _, n := range slices.Sorted(maps.Keys(w.enrich)) {
		if r := w.pulls[n]; r == nil || r.State != bus.PullOpen {
			delete(w.enrich, n)
			continue
		}
		nums = append(nums, n)
	}
	if len(nums) == 0 {
		return
	}
	owner, name, _ := strings.Cut(w.slug, "/")
	var q strings.Builder
	q.WriteString("query($owner:String!,$name:String!){repository(owner:$owner,name:$name){")
	for _, n := range nums {
		fmt.Fprintf(&q, "p%d:pullRequest(number:%d){reviewDecision mergeStateStatus}", n, n)
	}
	q.WriteString("}}")
	var out struct {
		Repository map[string]*struct {
			ReviewDecision   *string `json:"reviewDecision"`
			MergeStateStatus *string `json:"mergeStateStatus"`
		} `json:"repository"`
	}
	err := cy.api.GraphQL(cy.ctx, q.String(), map[string]any{"owner": owner, "name": name}, &out)
	if err != nil {
		switch cy.failureKind(err) {
		case FailureRateLimited, FailureAuth:
			cy.fail(err, GitHubMeta{})
			return
		}
		// A bad pull request must not hold the repository back. Try a few
		// more times, then wait for the next change.
		for _, n := range nums {
			if w.enrich[n]++; w.enrich[n] >= githubEnrichTries {
				delete(w.enrich, n)
			}
		}
		cy.p.report(fmt.Errorf("review decision and merge state: %w", err))
		return
	}
	for _, n := range nums {
		delete(w.enrich, n)
		node := out.Repository["p"+strconv.Itoa(n)]
		if node == nil {
			continue
		}
		rec := w.pulls[n]
		review, merge := lowerEnum(node.ReviewDecision), lowerEnum(node.MergeStateStatus)
		if rec.ReviewDecision != review || rec.MergeState != merge {
			rec.ReviewDecision, rec.MergeState = review, merge
			if _, ok := cy.pullChanges[n]; !ok {
				cy.pullChanges[n] = rec.State
			}
		}
	}
}

// ---- checks ----

// tracked returns the open pull requests whose head is a branch some served
// worktree has checked out, by number, with the worktree that owns it.
func (cy *githubCycle) tracked() map[int]bus.WorktreeID {
	out := map[int]bus.WorktreeID{}
	for _, n := range cy.w.open {
		rec := cy.w.pulls[n]
		if rec == nil || !strings.EqualFold(rec.headRepo, cy.w.slug) || !shaPattern.MatchString(rec.HeadSHA) {
			continue
		}
		if wt, ok := worktreeOfBranch(cy.t.Branches, rec.HeadRef); ok {
			out[n] = wt
		}
	}
	return out
}

// worktreeOfBranch returns the first worktree, by ID, on branch.
func worktreeOfBranch(branches map[bus.WorktreeID]string, branch string) (bus.WorktreeID, bool) {
	var found bus.WorktreeID
	ok := false
	for wt, b := range branches {
		if b == branch && (!ok || wt < found) {
			found, ok = wt, true
		}
	}
	return found, ok
}

func (cy *githubCycle) checks() {
	w := cy.w
	tracked := cy.tracked()
	heads := map[string]bool{}
	for n := range tracked {
		heads[w.pulls[n].HeadSHA] = true
	}
	for n := range w.ci {
		if _, ok := tracked[n]; !ok {
			delete(w.ci, n)
		}
	}
	for sha := range w.ciFetch {
		if !heads[sha] {
			delete(w.ciFetch, sha)
		}
	}
	complete := true
	for _, n := range slices.Sorted(maps.Keys(tracked)) {
		rec := w.pulls[n]
		body := w.ciFetch[rec.HeadSHA]
		if body == nil {
			body = &ciBody{}
			w.ciFetch[rec.HeadSHA] = body
		}
		if !cy.fetchChecks(rec.HeadSHA, body) {
			complete = false
			if cy.stopped {
				break
			}
			continue
		}
		counts, failed := countChecks(body.runs, body.statuses)
		next := &ciRec{bus.GHCIUpdated{
			Pull:    n,
			HeadRef: rec.HeadRef,
			HeadSHA: rec.HeadSHA,
			State:   bus.CombineCI(counts),
			Counts:  counts,
			Failed:  failed,
			URL:     rec.URL + "/checks",
		}}
		prev, had := w.ci[n]
		if had && prev.HeadSHA == next.HeadSHA && prev.State == next.State && prev.Counts == next.Counts && slices.Equal(prev.Failed, next.Failed) {
			continue
		}
		if had {
			next.Previous = prev.State
			cy.enrichNow[n] = true // checks decide the merge state
		}
		w.ci[n] = next
		cy.ciChanges = append(cy.ciChanges, n)
	}
	if complete {
		w.seeded.ci = true
	}
}

// fetchChecks refreshes body for sha with two conditional requests.
func (cy *githubCycle) fetchChecks(sha string, body *ciBody) bool {
	var runs checkRunsWire
	meta, fresh, err := cy.get(fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", cy.w.slug, sha), body.runsETag, &runs)
	if err != nil {
		cy.fail(err, meta)
		return false
	}
	if fresh {
		body.runs, body.runsETag = runs.CheckRuns, meta.ETag
	}
	var status combinedStatusWire
	meta, fresh, err = cy.get(fmt.Sprintf("/repos/%s/commits/%s/status?per_page=100", cy.w.slug, sha), body.statusETag, &status)
	if err != nil {
		cy.fail(err, meta)
		return false
	}
	if fresh {
		body.statuses, body.statusETag = status.Statuses, meta.ETag
	}
	return true
}

// ---- issues ----

func (cy *githubCycle) issuesSection() {
	w := cy.w
	bound := map[int]bool{}
	for _, n := range cy.t.Issues {
		bound[n] = true
	}
	for n := range w.issues {
		if !bound[n] {
			delete(w.issues, n)
			delete(w.issueETag, n)
		}
	}
	complete := true
	for _, n := range slices.Sorted(maps.Keys(bound)) {
		if until, gone := w.issueGone[n]; gone && cy.now.Before(until) {
			continue
		}
		var iw issueWire
		meta, fresh, err := cy.get(fmt.Sprintf("/repos/%s/issues/%d", w.slug, n), w.issueETag[n], &iw)
		if err != nil {
			if cy.failureKind(err) == FailureNotFound {
				w.issueGone[n] = cy.now.Add(githubMissingRetry)
				delete(w.issues, n)
				delete(w.issueETag, n)
				cy.observe(meta)
				continue
			}
			complete = false
			cy.fail(err, meta)
			if cy.stopped {
				break
			}
			continue
		}
		delete(w.issueGone, n)
		if !fresh {
			continue
		}
		w.issueETag[n] = meta.ETag
		next := iw.payload()
		prev, had := w.issues[n]
		if had && sameIssue(*prev, next) {
			continue
		}
		if had {
			next.Previous = prev.State
		}
		w.issues[n] = &next
		cy.issueChanges = append(cy.issueChanges, n)
	}
	if complete {
		w.seeded.issues = true
	}
}

// ---- results ----

// finish records the cycle's health and returns the delay before the next
// cycle, from NextPoll.
func (cy *githubCycle) finish() time.Duration {
	w := cy.w
	if cy.failed {
		w.failures++
	} else {
		w.failures = 0
	}
	meta := cy.meta
	meta.RateLimit = cy.p.currentRate()
	active := false
	tracked := cy.tracked()
	for n := range tracked {
		rec := w.pulls[n]
		if ci := w.ci[n]; ci != nil && ci.State == bus.CIPending {
			active = true
		}
		if cy.now.Sub(latest(rec.UpdatedAt, w.changedAt[n])) < GitHubActiveWindow {
			active = true
		}
	}
	delay := NextPoll(active, cy.t.Watched, meta, w.failures, cy.now)
	switch {
	case !cy.failed:
		w.health, w.retryAt = GitHubOK, time.Time{}
	case cy.limited:
		w.health, w.retryAt = GitHubRateLimited, cy.now.Add(delay)
	default:
		w.health, w.retryAt = GitHubUnavailable, cy.now.Add(delay)
	}
	w.blockedUntil = time.Time{}
	if cy.failed || meta.RetryAfter > 0 || (meta.RateLimit.Limit > 0 && meta.RateLimit.Remaining == 0) {
		w.blockedUntil = cy.now.Add(delay)
	}
	return delay
}

// events builds the events for what changed in this cycle, in a fixed order:
// pull requests, then checks, then issues, each by number. A section that
// has not completed a cycle yet produces none.
func (cy *githubCycle) events() []bus.Event {
	w := cy.w
	var out []bus.Event
	seededPulls, seededCI, seededIssues := cy.seededBefore()
	if seededPulls {
		for _, n := range slices.Sorted(maps.Keys(cy.pullChanges)) {
			rec := w.pulls[n]
			if rec == nil {
				continue
			}
			rec.Previous = cy.pullChanges[n]
			var wt bus.WorktreeID
			if strings.EqualFold(rec.headRepo, w.slug) {
				wt, _ = worktreeOfBranch(cy.t.Branches, rec.HeadRef)
			}
			if wt != "" {
				w.changedAt[n] = cy.now
			}
			out = append(out, cy.event(bus.TypeGHPullUpdated, wt, rec.GHPullUpdated))
		}
	}
	if seededCI {
		slices.Sort(cy.ciChanges)
		for _, n := range cy.ciChanges {
			ci := w.ci[n]
			if ci == nil {
				continue
			}
			wt, _ := worktreeOfBranch(cy.t.Branches, ci.HeadRef)
			w.changedAt[n] = cy.now
			out = append(out, cy.event(bus.TypeGHCIUpdated, wt, ci.GHCIUpdated))
		}
	}
	if seededIssues {
		slices.Sort(cy.issueChanges)
		for _, n := range cy.issueChanges {
			is := w.issues[n]
			if is == nil {
				continue
			}
			var wt bus.WorktreeID
			for id, bound := range cy.t.Issues {
				if bound == n && (wt == "" || id < wt) {
					wt = id
				}
			}
			out = append(out, cy.event(bus.TypeGHIssueUpdated, wt, *is))
		}
	}
	return out
}

// seededBefore reports which sections had completed a cycle before this one.
// The seeded flags are set during the cycle, so the cycle remembers the
// values it started with.
func (cy *githubCycle) seededBefore() (pulls, ci, issues bool) {
	return cy.wasSeeded.pulls, cy.wasSeeded.ci, cy.wasSeeded.issues
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func (cy *githubCycle) event(t bus.Type, wt bus.WorktreeID, payload any) bus.Event {
	b, err := json.Marshal(payload)
	if err != nil {
		b = []byte("{}")
	}
	return bus.Event{Type: t, Source: "watch.github", Repo: cy.t.Repo, Worktree: wt, Time: cy.now.UTC(), Payload: b}
}

// snapshot is the state projections read.
func (w *repoWork) snapshot() GitHubState {
	s := GitHubState{
		Health:  w.health,
		RetryAt: w.retryAt,
		CI:      map[string]bus.GHCIUpdated{},
		Issues:  map[int]bus.GHIssueUpdated{},
	}
	for _, n := range w.open {
		if r := w.pulls[n]; r != nil {
			s.Pulls = append(s.Pulls, r.GHPullUpdated)
		}
	}
	// Then the open pull requests a full page pushed out of view, and the
	// closed ones, each newest update first.
	var beyond, closed []*pullRec
	for n, r := range w.pulls {
		switch {
		case r.State != bus.PullOpen:
			closed = append(closed, r)
		case !w.listed[n]:
			beyond = append(beyond, r)
		}
	}
	for _, group := range [][]*pullRec{beyond, closed} {
		slices.SortFunc(group, func(a, b *pullRec) int {
			if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
				return c
			}
			return a.Number - b.Number
		})
		for _, r := range group {
			s.Pulls = append(s.Pulls, r.GHPullUpdated)
		}
	}
	for _, ci := range w.ci {
		s.CI[ci.HeadSHA] = ci.GHCIUpdated
	}
	for n, is := range w.issues {
		s.Issues[n] = *is
	}
	return s
}
