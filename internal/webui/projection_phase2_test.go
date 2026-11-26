package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/watch"
	"github.com/ryanhelms/agentflow/internal/worktree"
)

// quietWatcher publishes nothing, so tests see only the events they send.
type quietWatcher struct{}

func (quietWatcher) Add(watch.Target) error                             { return nil }
func (quietWatcher) Remove(bus.RepoID, bus.WorktreeID) error            { return nil }
func (quietWatcher) Mode(bus.RepoID, bus.WorktreeID) (watch.Mode, bool) { return "", false }
func (quietWatcher) Run(ctx context.Context, _ bus.Publisher) error {
	<-ctx.Done()
	return nil
}

type fakeGitHub struct {
	mu     sync.Mutex
	states map[bus.RepoID]watch.GitHubState
}

func (f *fakeGitHub) GitHubState(repo bus.RepoID) (watch.GitHubState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.states[repo]
	return s, ok
}

func (f *fakeGitHub) set(repo bus.RepoID, s watch.GitHubState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[repo] = s
}

type fakeApprovals struct {
	mu    sync.Mutex
	items map[bus.RepoID][]ApprovalItem
	asked []bus.RepoID
}

func (f *fakeApprovals) Approvals(_ context.Context, repo bus.RepoID) ([]ApprovalItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, repo)
	return slices.Clone(f.items[repo]), nil
}

func (f *fakeApprovals) set(repo bus.RepoID, items []ApprovalItem) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[repo] = items
}

// liveConsole is one worktree served by a real event runtime: a real git
// repository and store, a fake GitHub cache, fake approval records, a
// test global store and no file watcher.
type liveConsole struct {
	rt        *eventRuntime
	ev        *repoEvents
	srv       *server
	streams   *streamFixture
	global    *storetest.Store
	gh        *fakeGitHub
	approvals *fakeApprovals
	dir       string
	worktree  bus.WorktreeID
}

func newLiveConsole(t *testing.T) *liveConsole {
	t.Helper()
	dir := eventRepo(t)
	ctx := context.Background()
	r, _, err := inspectWorktree(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	c := &liveConsole{dir: dir, worktree: bus.WorktreeID(r.ID), global: storetest.New(bus.GlobalRepo),
		gh: &fakeGitHub{states: map[bus.RepoID]watch.GitHubState{}}, approvals: &fakeApprovals{items: map[bus.RepoID][]ApprovalItem{}}}
	oldWatcher, oldGlobal, oldExts := newWatcher, openGlobalStore, runtimeExtensions
	newWatcher = func() (watch.Watcher, error) { return quietWatcher{}, nil }
	openGlobalStore = func(context.Context) (store.Store, error) { return c.global, nil }
	runtimeExtensions = nil
	t.Cleanup(func() { newWatcher, openGlobalStore, runtimeExtensions = oldWatcher, oldGlobal, oldExts })
	rt, stop, err := startEventRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rt.setGitHubCache(c.gh)
	rt.setApprovalSource(c.approvals)
	ev, err := rt.attach(r)
	if err != nil {
		t.Fatal(err)
	}
	c.rt, c.ev = rt, ev
	c.srv = &server{repo: &gitx.Repo{Dir: dir}, session: randomKey(), instanceKey: randomKey(), rememberKey: randomKey(), events: ev, worktreePath: "/w/" + r.ID + "/", tabs: &tabHub{}}
	ts := httptest.NewServer(c.srv.handler())
	c.srv.origin = ts.URL
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
		stop()
	})
	c.streams = &streamFixture{t: t, srv: map[bus.RepoID]*server{ev.id: c.srv}}
	return c
}

func (c *liveConsole) open(t *testing.T, query string, headers ...string) (*sseClient, int) {
	t.Helper()
	c.streams.t = t
	return c.streams.open(c.ev.id, query, headers...)
}

func (c *liveConsole) publish(t *testing.T, e bus.Event) bus.Event {
	t.Helper()
	if e.Source == "" {
		e.Source = "test"
	}
	out, err := c.rt.bus.Publish(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (c *liveConsole) projection(t *testing.T, name string) Projection {
	t.Helper()
	for _, p := range c.ev.projector.projections {
		if p.Name() == name {
			return p
		}
	}
	t.Fatalf("no %s projection", name)
	return nil
}

func stateOf[T any](t *testing.T, p Projection, key ProjectionKey) T {
	t.Helper()
	raw, err := p.State(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func payload(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// findState returns the state of projection name for worktree in states.
func findState(states []ProjectionState, name string, worktree bus.WorktreeID) (ProjectionState, bool) {
	for _, s := range states {
		if s.Projection == name && s.Worktree == worktree {
			return s, true
		}
	}
	return ProjectionState{}, false
}

// nextState reads deltas until one carries projection name's state for
// worktree.
func nextState[T any](t *testing.T, c *sseClient, name string, worktree bus.WorktreeID) (T, DeltaFrame) {
	t.Helper()
	for range 20 {
		d := decode[DeltaFrame](t, c.expect(t, FrameDelta))
		if s, ok := findState(d.States, name, worktree); ok {
			var v T
			if err := json.Unmarshal(s.State, &v); err != nil {
				t.Fatalf("decode %s: %v", s.State, err)
			}
			return v, d
		}
	}
	t.Fatalf("no %s state", name)
	var zero T
	return zero, DeltaFrame{}
}

func TestApprovalsProjection(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	item := func(id string, state bus.ApprovalState, issued, expires time.Duration) ApprovalItem {
		return ApprovalItem{Approval: bus.Approval{OperationID: id, Action: "push", Class: "remote", IssuedAt: now.Add(issued), ExpiresAt: now.Add(expires)}, State: state}
	}
	records := []ApprovalItem{
		item("op-late", bus.StatePending, -10*time.Minute, time.Hour),
		item("op-lapsed", bus.StatePending, -time.Hour, -30*time.Minute),
		item("op-approved", bus.StateApproved, -2*time.Hour, time.Hour),
		item("op-old", bus.StateCancelled, -25*time.Hour, -24*time.Hour),
		item("op-prepared", "prepared", -time.Minute, time.Hour),
	}
	for i := range 12 {
		records = append(records, item(fmt.Sprintf("op-done-%02d", i), bus.StateConsumed, -time.Duration(i+1)*time.Minute, time.Hour))
	}
	src := &fakeApprovals{items: map[bus.RepoID][]ApprovalItem{repoA: records, repoB: {item("op-b", bus.StatePending, 0, time.Hour)}}}
	ev := &repoEvents{id: repoA, worktrees: map[bus.WorktreeID]registeredWorktree{}}
	p := &approvalsProjection{repo: ev, source: func() ApprovalSource { return src }, now: func() time.Time { return now }}

	st := stateOf[ApprovalsState](t, p, ProjectionKey{Repo: repoA})
	var open []string
	for _, it := range st.Open {
		open = append(open, fmt.Sprintf("%s:%v", it.OperationID, it.Expired))
	}
	if strings.Join(open, " ") != "op-approved:false op-lapsed:true op-late:false" {
		t.Fatalf("open = %v", open)
	}
	if len(st.Recent) != MaxApprovalsRecent || st.Recent[0].OperationID != "op-done-00" || st.Recent[9].OperationID != "op-done-09" {
		t.Fatalf("recent = %+v", st.Recent)
	}
	for _, it := range st.Recent {
		if it.OperationID == "op-old" {
			t.Fatal("recent kept an approval older than a day")
		}
	}
	// Only this repository's records are read; another key is null.
	if slices.ContainsFunc(src.asked, func(r bus.RepoID) bool { return r != repoA }) {
		t.Fatalf("asked for %v", src.asked)
	}
	if raw, _ := p.State(context.Background(), ProjectionKey{Repo: repoB}); string(raw) != "null" {
		t.Fatalf("other repository state = %s", raw)
	}
	if keys := p.Keys(bus.Event{Type: bus.TypeApprovalRequested, Repo: repoA}); len(keys) != 1 || keys[0] != (ProjectionKey{Repo: repoA}) {
		t.Fatalf("keys = %v", keys)
	}
	for _, e := range []bus.Event{{Type: bus.TypeApprovalRequested, Repo: repoB}, {Type: bus.TypeGitHeadChanged, Repo: repoA, Worktree: "main"}} {
		if keys := p.Keys(e); len(keys) != 0 {
			t.Fatalf("keys for %+v = %v", e, keys)
		}
	}
	none := &approvalsProjection{repo: ev, source: func() ApprovalSource { return nil }, now: time.Now}
	if raw, _ := none.State(context.Background(), ProjectionKey{Repo: repoA}); string(raw) != `{"open":[],"recent":[]}` {
		t.Fatalf("state without a journal = %s", raw)
	}
}

func TestActivityProjection(t *testing.T) {
	c := newLiveConsole(t)
	gitIn(t, c.dir, "checkout", "-q", "-b", "feat-x")
	if err := os.MkdirAll(filepath.Join(c.dir, ".agentflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, filepath.FromSlash(worktree.RecordFile)), []byte("issue: ryanhelms/agentflow#36\nbranch: feat-x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := c.projection(t, ProjectionActivity)
	key := ProjectionKey{Repo: c.ev.id}

	if st := stateOf[ActivityState](t, p, key); st.GitHub.State != watch.GitHubUnavailable || len(st.Pulls) != 0 || st.Recent == nil {
		t.Fatalf("state before the first poll = %+v", st)
	}

	base := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	retry := base.Add(time.Hour)
	c.gh.set(c.ev.id, watch.GitHubState{
		Health:  watch.GitHubRateLimited,
		RetryAt: retry,
		Pulls: []bus.GHPullUpdated{
			{Number: 5, Title: "draft", State: bus.PullOpen, Draft: true, MergeState: "clean", HeadRef: "draft", BaseRef: "main", UpdatedAt: base.Add(4 * time.Minute)},
			{Number: 2, Title: "feature", State: bus.PullOpen, MergeState: "blocked", ReviewDecision: "changes_requested", HeadRef: "feat-x", HeadSHA: "aaa", BaseRef: "main", UpdatedAt: base.Add(3 * time.Minute), URL: "https://github.com/o/r/pull/2"},
			{Number: 4, Title: "merged", State: bus.PullMerged, HeadRef: "old", BaseRef: "main", UpdatedAt: base.Add(2 * time.Minute)},
			{Number: 3, Title: "stacked", State: bus.PullOpen, MergeState: "clean", HeadRef: "stack", BaseRef: "feat-x", UpdatedAt: base.Add(time.Minute)},
		},
		CI:     map[string]bus.GHCIUpdated{"aaa": {Pull: 2, HeadSHA: "aaa", State: bus.CIFailure, Failed: []string{"lint"}}},
		Issues: map[int]bus.GHIssueUpdated{36: {Number: 36, Title: "Phase 2.4", State: "open"}, 7: {Number: 7, Title: "other", State: "closed"}},
	})
	for i := range MaxActivityRecent + 2 {
		c.publish(t, bus.Event{Type: bus.TypeCommitCreated, Repo: c.ev.id, Worktree: c.worktree, Payload: payload(t, bus.CommitCreated{OID: fmt.Sprintf("%040d", i), Subject: fmt.Sprintf("feat: change %d", i)})})
	}
	passed := bus.Event{Type: bus.TypeHookRan, Repo: c.ev.id, Payload: payload(t, bus.HookRan{Hook: "pre-bash", Client: "claude-code", Outcome: bus.HookPassed})}
	if keys := p.Keys(passed); len(keys) != 0 {
		t.Fatalf("a passed hook recomputes activity: %v", keys)
	}
	c.publish(t, passed)
	blocked := c.publish(t, bus.Event{Type: bus.TypeHookRan, Repo: c.ev.id, Worktree: c.worktree, Payload: payload(t, bus.HookRan{Hook: "commit-msg", Client: "git", Outcome: bus.HookBlocked, Reason: "convention"})})
	if keys := p.Keys(blocked); len(keys) != 1 || keys[0] != key {
		t.Fatalf("blocked hook keys = %v", keys)
	}

	st := stateOf[ActivityState](t, p, key)
	var order []string
	for _, pr := range st.Pulls {
		order = append(order, fmt.Sprintf("#%d:%s:%s:%d", pr.Number, pr.Worktree, pr.CI, pr.DependsOn))
	}
	if strings.Join(order, " ") != "#3:::2 #5:::0 #2:"+string(c.worktree)+":failure:0" {
		t.Fatalf("pulls = %v", order)
	}
	if !slices.Equal(st.Pulls[2].Failed, []string{"lint"}) {
		t.Fatalf("failed checks = %v", st.Pulls[2].Failed)
	}
	if len(st.Issues) != 2 || st.Issues[0].Number != 7 || st.Issues[0].Worktree != "" || st.Issues[1].Number != 36 || st.Issues[1].Worktree != c.worktree {
		t.Fatalf("issues = %+v", st.Issues)
	}
	if st.GitHub.State != watch.GitHubRateLimited || st.GitHub.RetryAt == nil || !st.GitHub.RetryAt.Equal(retry) {
		t.Fatalf("github = %+v", st.GitHub)
	}
	if len(st.Recent) != MaxActivityRecent || st.Recent[0].Hook == nil || st.Recent[0].Hook.Reason != "convention" || st.Recent[0].EventID != blocked.ID ||
		st.Recent[1].Commit == nil || st.Recent[1].Commit.Subject != "feat: change 21" || st.Recent[MaxActivityRecent-1].Commit.Subject != "feat: change 3" {
		t.Fatalf("recent = %+v", st.Recent)
	}
	for _, want := range []string{"#3 stacked — stack into feat-x · clean · depends on #2", "#2 feature — feat-x into main · blocked · changes requested https://github.com/o/r/pull/2"} {
		if !strings.Contains(st.Handoff, want) {
			t.Fatalf("handoff %q lacks %q", st.Handoff, want)
		}
	}
	if strings.Contains(st.Handoff, "#4") {
		t.Fatalf("handoff lists a merged pull request: %q", st.Handoff)
	}
}

func TestStreamGlobalScoping(t *testing.T) {
	for _, raw := range []string{"global/*/config.**", "global/*/client.**", "global/*/config.layer.changed", "global/*/client.settings.changed"} {
		topics, _, err := streamTopics([]string{raw}, repoA)
		if err != nil || topics[0].Repo != bus.GlobalRepo {
			t.Fatalf("%s: %v %v", raw, topics, err)
		}
	}
	for raw, want := range map[string]int{
		"global/*/**":                  http.StatusForbidden,
		"global/*/git.**":              http.StatusForbidden,
		"global/*/approval.**":         http.StatusForbidden,
		"global/*/gh.**":               http.StatusForbidden,
		"global/*/*.layer.changed":     http.StatusForbidden,
		"global/*/configx.**":          http.StatusForbidden,
		"global/main/config.**":        http.StatusBadRequest,
		string(repoB) + "/*/config.**": http.StatusForbidden,
	} {
		if _, status, err := streamTopics([]string{raw}, repoA); err == nil || status != want {
			t.Fatalf("%s: status %d, %v; want %d", raw, status, err, want)
		}
	}
	for _, c := range []bus.Cursor{{bus.GlobalRepo: 2}, {repoA: 1, bus.GlobalRepo: 2}} {
		if _, _, err := streamCursor(c.String(), repoA); err != nil {
			t.Fatalf("cursor %s: %v", c, err)
		}
	}
	if _, status, err := streamCursor(bus.Cursor{repoB: 1, bus.GlobalRepo: 2}.String(), repoA); err == nil || status != http.StatusForbidden {
		t.Fatalf("cursor with another repository: %d %v", status, err)
	}

	// The same rules over HTTP.
	c := newLiveConsole(t)
	for _, tc := range []struct {
		query   string
		headers []string
		want    int
	}{
		{"?topic=" + url.QueryEscape("global/*/config.**"), nil, http.StatusOK},
		{"?topic=" + url.QueryEscape("global/*/**"), nil, http.StatusForbidden},
		{"?topic=" + url.QueryEscape("global/*/approval.**"), nil, http.StatusForbidden},
		{"", []string{"Last-Event-ID", bus.Cursor{bus.GlobalRepo: 1}.String()}, http.StatusOK},
		{"", []string{"Last-Event-ID", bus.Cursor{repoB: 1, bus.GlobalRepo: 1}.String()}, http.StatusForbidden},
	} {
		s, status := c.open(t, tc.query, tc.headers...)
		if status != tc.want {
			t.Fatalf("%s %v: status %d, want %d", tc.query, tc.headers, status, tc.want)
		}
		if s != nil {
			s.resp.Body.Close()
		}
	}
}

// A personal-layer change reaches every repository stream as the overview
// states it changed, whether or not the stream follows global; a stream
// that follows global also gets the event itself, cursors and replay.
func TestStreamGlobalConfigEvents(t *testing.T) {
	c := newLiveConsole(t)
	repoTopics := "?topic=" + url.QueryEscape("*/*/git.**") + "&topic=" + url.QueryEscape("*/*/config.**")
	plain, _ := c.open(t, repoTopics)
	plain.expect(t, FrameSnapshot)
	following, _ := c.open(t, repoTopics+"&topic="+url.QueryEscape("global/*/config.**"))
	following.expect(t, FrameSnapshot)

	// An unrelated global event changes no state: neither stream hears it.
	c.publish(t, bus.Event{Type: bus.TypeClientSettingsChanged, Repo: bus.GlobalRepo, Payload: payload(t, bus.ClientSettingsChanged{Client: "claude", Scope: bus.SettingsUser, Paths: []string{".claude/settings.json"}})})
	plain.quiet(t, 150*time.Millisecond)

	// The overview is recomputed from git and configuration; an untracked
	// file makes the recomputed state differ without depending on how the
	// personal layer is validated.
	if err := os.WriteFile(filepath.Join(c.dir, "note.txt"), []byte("n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := c.publish(t, bus.Event{Type: bus.TypeConfigLayerChanged, Repo: bus.GlobalRepo, Payload: payload(t, bus.ConfigLayerChanged{Layer: bus.LayerPersonal, Paths: []string{"config.yml"}})})
	if e.Seq != 2 {
		t.Fatalf("global event seq = %d; the global store must hold it", e.Seq)
	}
	for _, s := range []*sseClient{plain, following} {
		ov, d := nextState[OverviewState](t, s, "overview", c.worktree)
		if d.Event.Repo != bus.GlobalRepo || d.Event.ID != e.ID || ov.Untracked != 1 {
			t.Fatalf("delta = %+v, overview = %+v", d.Event, ov)
		}
	}
	// The plain stream never acknowledges global positions it does not follow.
	if got := decode[SnapshotFrame](t, func() sseFrame {
		s, _ := c.open(t, repoTopics)
		return s.expect(t, FrameSnapshot)
	}()); strings.Contains(got.Cursor, "global") {
		t.Fatalf("plain snapshot cursor = %s", got.Cursor)
	}
	snap, _ := c.open(t, "?topic="+url.QueryEscape("global/*/config.**"))
	if fr := snap.expect(t, FrameSnapshot); !strings.Contains(fr.id, "global=2") {
		t.Fatalf("following snapshot id = %s", fr.id)
	}

	// Replay from a global position sends the missed global event.
	c.publish(t, bus.Event{Type: bus.TypeGitRefsChanged, Repo: c.ev.id})
	replay, _ := c.open(t, "?topic="+url.QueryEscape("global/*/config.**")+"&topic="+url.QueryEscape("*/*/git.**"), "Last-Event-ID", bus.Cursor{c.ev.id: 1, bus.GlobalRepo: 1}.String())
	d := replay.expect(t, FrameDelta)
	if delta := decode[DeltaFrame](t, d); delta.Event.ID != e.ID || !strings.Contains(d.id, "global=2") {
		t.Fatalf("replay = %s %s", d.id, d.data)
	}
	if fr := replay.expect(t, FrameSnapshot); !strings.Contains(fr.id, "global=2") {
		t.Fatalf("snapshot after replay = %s", fr.id)
	}
}

func TestStreamRejectsBadTabs(t *testing.T) {
	c := newLiveConsole(t)
	many := strings.Repeat("&tab="+strings.Repeat("a", 32), 1)
	for i := range maxStreamTabs {
		many += fmt.Sprintf("&tab=%032x", i+1)
	}
	for query, want := range map[string]int{
		"?tab=nothex":                     http.StatusBadRequest,
		"?tab=" + strings.Repeat("A", 32): http.StatusBadRequest,
		"?" + many[1:]:                    http.StatusBadRequest,
		"?tab=" + strings.Repeat("a", 32): http.StatusOK,
	} {
		s, status := c.open(t, query)
		if status != want {
			t.Fatalf("%s: status %d, want %d", query, status, want)
		}
		if s != nil {
			s.resp.Body.Close()
		}
	}
}

func TestTabsOverEventStream(t *testing.T) {
	old := tabGrace
	tabGrace = 300 * time.Millisecond
	t.Cleanup(func() { tabGrace = old })
	c := newLiveConsole(t)
	const id = "0123456789abcdef0123456789abcdef"
	s := c.srv
	attach := func(tab string, key string) int {
		return request(t, s, http.MethodPost, "/api/tabs/attach", s.origin, key, map[string]string{"tab": tab}).Code
	}
	if code := attach(id, ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous attach = %d", code)
	}
	if code := attach("not-a-tab", "session"); code != http.StatusBadRequest {
		t.Fatalf("bad tab attach = %d", code)
	}
	if code := request(t, s, http.MethodPost, "/api/tabs/attach", "http://evil.localhost", "session", map[string]string{"tab": id}).Code; code != http.StatusForbidden {
		t.Fatalf("cross-origin attach = %d", code)
	}

	stream, _ := c.open(t, "?topic="+url.QueryEscape("*/*/ui.**")+"&tab="+id)
	snap := decode[SnapshotFrame](t, stream.expect(t, FrameSnapshot))
	if st, ok := findState(snap.States, ProjectionTabs, c.worktree); !ok || string(st.State) != `{"tabs":[],"navigation":null}` {
		t.Fatalf("snapshot = %+v", snap.States)
	}
	if code := attach(id, "session"); code != http.StatusNoContent {
		t.Fatalf("attach = %d", code)
	}
	tabs, _ := nextState[TabsState](t, stream, ProjectionTabs, c.worktree)
	if len(tabs.Tabs) != 1 || tabs.Tabs[0].Tab != id || tabs.Navigation != nil {
		t.Fatalf("tabs = %+v", tabs)
	}

	// A held tab outlives the grace period.
	time.Sleep(2 * tabGrace)
	if st := s.tabRoutes().tabsState(s.worktreePath); len(st.Tabs) != 1 {
		t.Fatalf("held tab detached: %+v", st)
	}

	// Navigation arrives in the tabs state, and the ack clears it.
	d := Destination{Screen: "providers", Role: "devops.checks"}
	result := make(chan tabOutcome, 1)
	go func() { result <- s.tabRoutes().navigateTo(context.Background(), d, false, "") }()
	tabs, _ = nextState[TabsState](t, stream, ProjectionTabs, c.worktree)
	nav := tabs.Navigation
	if nav == nil || nav.Tab != id || nav.Destination != d || nav.ID == "" || nav.Target != "" {
		t.Fatalf("navigation = %+v", nav)
	}
	if code := request(t, s, http.MethodPost, "/api/tabs/ack", s.origin, "session", map[string]any{"tab": strings.Repeat("f", 32), "id": nav.ID, "handled": true}).Code; code != http.StatusNotFound {
		t.Fatalf("another tab's ack = %d", code)
	}
	if code := request(t, s, http.MethodPost, "/api/tabs/ack", s.origin, "session", map[string]any{"tab": id, "id": nav.ID, "handled": true}).Code; code != http.StatusOK {
		t.Fatalf("ack = %d", code)
	}
	if got := <-result; got != tabAccepted {
		t.Fatalf("outcome = %v", got)
	}
	if tabs, _ = nextState[TabsState](t, stream, ProjectionTabs, c.worktree); tabs.Navigation != nil || len(tabs.Tabs) != 1 {
		t.Fatalf("after ack = %+v", tabs)
	}

	// An unanswered navigation clears after two seconds.
	start := time.Now()
	if got := s.tabRoutes().navigateTo(context.Background(), d, false, ""); got != tabMissing || time.Since(start) < 2*time.Second {
		t.Fatalf("unanswered navigation = %v after %v", got, time.Since(start))
	}
	if st := s.tabRoutes().tabsState(s.worktreePath); st.Navigation != nil {
		t.Fatalf("navigation not cleared: %+v", st.Navigation)
	}

	// Leaving detaches at once.
	if code := request(t, s, http.MethodPost, "/api/tabs/leave", s.origin, "session", map[string]string{"tab": id}).Code; code != http.StatusNoContent {
		t.Fatalf("leave = %d", code)
	}
	for {
		tabs, _ = nextState[TabsState](t, stream, ProjectionTabs, c.worktree)
		if len(tabs.Tabs) == 0 {
			break
		}
	}

	// Without a stream that lists it, a tab lasts only the grace period,
	// and a stream that closes gives the same grace.
	stream.resp.Body.Close()
	if code := attach(id, "session"); code != http.StatusNoContent {
		t.Fatalf("attach = %d", code)
	}
	waitTabs(t, s, 0)
	held, _ := c.open(t, "?tab="+id)
	held.expect(t, FrameSnapshot)
	if code := attach(id, "session"); code != http.StatusNoContent {
		t.Fatalf("attach = %d", code)
	}
	held.resp.Body.Close()
	time.Sleep(tabGrace / 3)
	if st := s.tabRoutes().tabsState(s.worktreePath); len(st.Tabs) != 1 {
		t.Fatalf("tab detached before the grace ended: %+v", st)
	}
	waitTabs(t, s, 0)
}

func waitTabs(t *testing.T, s *server, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(s.tabRoutes().tabsState(s.worktreePath).Tabs) != n {
		if time.Now().After(deadline) {
			t.Fatalf("tabs = %+v, want %d", s.tabRoutes().tabsState(s.worktreePath), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// GET /api/state reads the worktree without refreshing the index, which
// the watcher would report as git.index.changed on every page load.
func TestStateDoesNotRewriteTheIndex(t *testing.T) {
	s := testServer(t)
	file := filepath.Join(s.repo.Dir, "a.txt")
	if err := os.WriteFile(file, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, s.repo.Dir, "add", "a.txt")
	gitIn(t, s.repo.Dir, "commit", "-q", "-m", "chore: add a")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(s.repo.Dir, ".git", "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	rec := request(t, s, http.MethodGet, "/api/state", "", "session", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("state = %d %s", rec.Code, rec.Body)
	}
	var body struct {
		DirtyCount int `json:"dirty_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.DirtyCount != 1 {
		t.Fatalf("dirty_count = %d, %v", body.DirtyCount, err)
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("/api/state rewrote the index: %v -> %v", before.ModTime(), after.ModTime())
	}
}

// An approval.* event recomputes the approvals state from the records of
// this repository only, and the stream carries no approval of another.
func TestApprovalsOverEventStream(t *testing.T) {
	c := newLiveConsole(t)
	stream, _ := c.open(t, "?topic="+url.QueryEscape("*/*/approval.**"))
	snap := decode[SnapshotFrame](t, stream.expect(t, FrameSnapshot))
	if st, ok := findState(snap.States, ProjectionApprovals, ""); !ok || string(st.State) != `{"open":[],"recent":[]}` {
		t.Fatalf("snapshot = %+v", snap.States)
	}
	now := time.Now().UTC()
	op := bus.Approval{OperationID: "op_0123456789abcdef0123456789abcdef", Action: "push", Class: "remote", Client: "claude-code", IssuedAt: now, ExpiresAt: now.Add(time.Hour)}
	c.approvals.set(c.ev.id, []ApprovalItem{{Approval: op, State: bus.StatePending}})
	c.approvals.set(repoB, []ApprovalItem{{Approval: bus.Approval{OperationID: "op_other"}, State: bus.StatePending}})
	c.publish(t, bus.Event{Type: bus.TypeApprovalRequested, Repo: c.ev.id, Payload: payload(t, bus.ApprovalRequested{Approval: op})})
	st, d := nextState[ApprovalsState](t, stream, ProjectionApprovals, "")
	if len(st.Open) != 1 || st.Open[0].OperationID != op.OperationID || st.Open[0].Client != "claude-code" || d.Event.Type != bus.TypeApprovalRequested {
		t.Fatalf("approvals = %+v", st)
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "op_other") {
		t.Fatalf("delta carries another repository's approval: %s", raw)
	}
}
