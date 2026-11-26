package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/bustest"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
	"github.com/ryanhelms/agentflow/internal/gitx"
)

const (
	repoA bus.RepoID = "0123456789abcdef"
	repoB bus.RepoID = "fedcba9876543210"
)

// fakeProjection serves fixed states per worktree.
type fakeProjection struct {
	repo      bus.RepoID
	mu        sync.Mutex
	worktrees []bus.WorktreeID
	states    map[bus.WorktreeID]string
}

func (f *fakeProjection) Name() string { return "overview" }
func (f *fakeProjection) Topics() []bus.Topic {
	return []bus.Topic{{Repo: f.repo, Worktree: bus.Any, Types: "git.**"}}
}
func (f *fakeProjection) Keys(e bus.Event) []ProjectionKey {
	if e.Repo != f.repo || !bus.MatchAny(f.Topics(), e) {
		return nil
	}
	if e.Worktree != "" {
		return []ProjectionKey{{Repo: f.repo, Worktree: e.Worktree}}
	}
	return f.AllKeys()
}
func (f *fakeProjection) AllKeys() []ProjectionKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []ProjectionKey
	for _, w := range f.worktrees {
		keys = append(keys, ProjectionKey{Repo: f.repo, Worktree: w})
	}
	return keys
}
func (f *fakeProjection) State(_ context.Context, k ProjectionKey) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.states[k.Worktree]; ok {
		return json.RawMessage(s), nil
	}
	return jsonNull, nil
}
func (f *fakeProjection) set(w bus.WorktreeID, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[w] = state
}

type streamFixture struct {
	t      *testing.T
	bus    *bustest.Bus
	stores map[bus.RepoID]*storetest.Store
	srv    map[bus.RepoID]*server
	ts     map[bus.RepoID]*httptest.Server
	proj   map[bus.RepoID]*fakeProjection
}

// newStreamFixture serves one console per repository, all on one bus, the
// way the daemon serves several clones.
func newStreamFixture(t *testing.T, readTimeout time.Duration, repos ...bus.RepoID) *streamFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f := &streamFixture{t: t, stores: map[bus.RepoID]*storetest.Store{}, srv: map[bus.RepoID]*server{}, ts: map[bus.RepoID]*httptest.Server{}, proj: map[bus.RepoID]*fakeProjection{}}
	var mu sync.Mutex
	f.bus = bustest.New(bus.Options{Interceptors: []bus.Interceptor{store.Interceptor(func(id bus.RepoID) (store.Store, bool) {
		mu.Lock()
		defer mu.Unlock()
		s, ok := f.stores[id]
		return s, ok
	})}})
	var wg sync.WaitGroup
	for _, repo := range repos {
		st := storetest.New(repo)
		mu.Lock()
		f.stores[repo] = st
		mu.Unlock()
		fp := &fakeProjection{repo: repo, worktrees: []bus.WorktreeID{"main", "feature-1a2b3c4d"}, states: map[bus.WorktreeID]string{"main": `{"branch":"main"}`, "feature-1a2b3c4d": `{"branch":"feature"}`}}
		ev := &repoEvents{id: repo, store: st, worktrees: map[bus.WorktreeID]registeredWorktree{}}
		ev.projector = newProjector(repo, []Projection{fp})
		sub, err := f.bus.Subscribe(bus.SubscribeOptions{Name: "projector"}, bus.Topic{Repo: repo, Worktree: bus.Any, Types: "**"})
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() { defer wg.Done(); ev.projector.run(ctx, sub) }()
		s := &server{repo: &gitx.Repo{Dir: t.TempDir()}, session: randomKey(), instanceKey: randomKey(), rememberKey: randomKey(), events: ev, worktreePath: "/w/main/"}
		ts := httptest.NewUnstartedServer(s.handler())
		ts.Config.ReadTimeout = readTimeout
		ts.Start()
		s.origin = ts.URL
		f.srv[repo], f.ts[repo], f.proj[repo] = s, ts, fp
	}
	t.Cleanup(func() {
		for _, ts := range f.ts {
			ts.CloseClientConnections()
			ts.Close()
		}
		cancel()
		_ = f.bus.Close()
		wg.Wait()
	})
	return f
}

func (f *streamFixture) publish(repo bus.RepoID, typ bus.Type, worktree bus.WorktreeID) bus.Event {
	f.t.Helper()
	e, err := f.bus.Publish(context.Background(), bus.Event{Type: typ, Source: "test", Repo: repo, Worktree: worktree})
	if err != nil {
		f.t.Fatal(err)
	}
	return e
}

type sseFrame struct {
	event, id, data string
	hasID           bool
}

type sseClient struct {
	resp   *http.Response
	frames chan sseFrame
	pings  chan struct{}
	retry  chan string
}

// open connects to repo's console. headers are name, value pairs.
func (f *streamFixture) open(repo bus.RepoID, query string, headers ...string) (*sseClient, int) {
	f.t.Helper()
	s := f.srv[repo]
	req, err := http.NewRequest(http.MethodGet, s.origin+EventStreamPath+query, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+grantToken(s.session, "tab/v1", time.Now().Add(time.Hour)))
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed below on refusal, otherwise in t.Cleanup after the reader goroutine
	if err != nil {
		f.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, resp.StatusCode
	}
	c := &sseClient{resp: resp, frames: make(chan sseFrame, 4096), pings: make(chan struct{}, 64), retry: make(chan string, 1)}
	go func() {
		defer close(c.frames)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var cur sseFrame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.event != "" {
					c.frames <- cur
				}
				cur = sseFrame{}
			case line == ": ping":
				select {
				case c.pings <- struct{}{}:
				default:
				}
			case strings.HasPrefix(line, "retry: "):
				c.retry <- strings.TrimPrefix(line, "retry: ")
			case strings.HasPrefix(line, "event: "):
				cur.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "), line == "id:":
				cur.id, cur.hasID = strings.TrimPrefix(strings.TrimPrefix(line, "id:"), " "), true
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	f.t.Cleanup(func() { resp.Body.Close() })
	return c, resp.StatusCode
}

func (c *sseClient) next(t *testing.T) sseFrame {
	t.Helper()
	select {
	case fr, ok := <-c.frames:
		if !ok {
			t.Fatal("stream closed")
		}
		return fr
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a frame")
	}
	return sseFrame{}
}

func (c *sseClient) expect(t *testing.T, event string) sseFrame {
	t.Helper()
	fr := c.next(t)
	if fr.event != event {
		t.Fatalf("frame = %s %s, want %s", fr.event, fr.data, event)
	}
	return fr
}

func (c *sseClient) quiet(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case fr := <-c.frames:
		t.Fatalf("unexpected frame %s %s", fr.event, fr.data)
	case <-time.After(d):
	}
}

func decode[T any](t *testing.T, fr sseFrame) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(fr.data), &v); err != nil {
		t.Fatalf("decode %s: %v", fr.data, err)
	}
	return v
}

func cursorOf(repo bus.RepoID, seq uint64) string { return bus.Cursor{repo: seq}.String() }

func TestStreamSnapshotThenDeltas(t *testing.T) {
	f := newStreamFixture(t, 0, repoA)
	c, _ := f.open(repoA, "")
	if r := <-c.retry; r != "1000" {
		t.Fatalf("retry = %q", r)
	}
	if ct := c.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	snap := c.expect(t, FrameSnapshot)
	s := decode[SnapshotFrame](t, snap)
	if !snap.hasID || snap.id != "" || s.Cursor != "" || len(s.States) != 2 {
		t.Fatalf("snapshot = %+v %+v", snap, s)
	}

	f.proj[repoA].set("main", `{"branch":"topic"}`)
	f.publish(repoA, bus.TypeGitHeadChanged, "main")
	d := c.expect(t, FrameDelta)
	delta := decode[DeltaFrame](t, d)
	if d.id != cursorOf(repoA, 1) || delta.Event.Seq != 1 || len(delta.States) != 1 || string(delta.States[0].State) != `{"branch":"topic"}` || delta.States[0].Worktree != "main" {
		t.Fatalf("delta = %s %s", d.id, d.data)
	}

	// An unchanged state is not sent again; the event still is.
	f.publish(repoA, bus.TypeGitIndexChanged, "main")
	d = c.expect(t, FrameDelta)
	if delta := decode[DeltaFrame](t, d); d.id != cursorOf(repoA, 2) || len(delta.States) != 0 {
		t.Fatalf("unchanged delta = %s %s", d.id, d.data)
	}

	// A removed key is sent once as null.
	f.proj[repoA].mu.Lock()
	delete(f.proj[repoA].states, "feature-1a2b3c4d")
	f.proj[repoA].mu.Unlock()
	f.publish(repoA, bus.TypeGitRefsChanged, "")
	d = c.expect(t, FrameDelta)
	delta = decode[DeltaFrame](t, d)
	if len(delta.States) != 1 || delta.States[0].Worktree != "feature-1a2b3c4d" || string(delta.States[0].State) != "null" {
		t.Fatalf("removal delta = %s", d.data)
	}
}

func TestStreamTopicsFilterEventsAndStates(t *testing.T) {
	f := newStreamFixture(t, 0, repoA)
	c, _ := f.open(repoA, "?topic="+url.QueryEscape("*/main/git.head.changed"))
	s := decode[SnapshotFrame](t, c.expect(t, FrameSnapshot))
	if len(s.States) != 1 || s.States[0].Worktree != "main" {
		t.Fatalf("snapshot states = %+v", s.States)
	}
	f.proj[repoA].set("feature-1a2b3c4d", `{"branch":"other"}`)
	f.publish(repoA, bus.TypeGitHeadChanged, "feature-1a2b3c4d") // other worktree: filtered
	f.publish(repoA, bus.TypeGitIndexChanged, "main")            // other type: filtered
	f.proj[repoA].set("main", `{"branch":"moved"}`)
	f.publish(repoA, bus.TypeGitHeadChanged, "main")
	d := c.expect(t, FrameDelta)
	if delta := decode[DeltaFrame](t, d); d.id != cursorOf(repoA, 3) || delta.Event.Type != bus.TypeGitHeadChanged || len(delta.States) != 1 {
		t.Fatalf("delta = %s %s", d.id, d.data)
	}
}

func TestStreamReplaysFromLastEventID(t *testing.T) {
	f := newStreamFixture(t, 0, repoA)
	for range 3 {
		f.publish(repoA, bus.TypeGitHeadChanged, "main")
	}
	c, _ := f.open(repoA, "", "Last-Event-ID", cursorOf(repoA, 1))
	for _, seq := range []uint64{2, 3} {
		d := c.expect(t, FrameDelta)
		delta := decode[DeltaFrame](t, d)
		if d.id != cursorOf(repoA, seq) || delta.Event.Seq != seq || delta.States == nil || len(delta.States) != 0 {
			t.Fatalf("replay = %s %s", d.id, d.data)
		}
	}
	snap := c.expect(t, FrameSnapshot)
	if snap.id != cursorOf(repoA, 3) || decode[SnapshotFrame](t, snap).Cursor != cursorOf(repoA, 3) {
		t.Fatalf("snapshot after replay = %+v", snap)
	}
	// Live events continue after the snapshot without repeating any.
	f.publish(repoA, bus.TypeGitHeadChanged, "main")
	if d := c.expect(t, FrameDelta); d.id != cursorOf(repoA, 4) {
		t.Fatalf("live delta id = %s", d.id)
	}
}

func TestStreamResyncsExpiredOrDistantCursors(t *testing.T) {
	f := newStreamFixture(t, 0, repoA)
	for range 4 {
		f.publish(repoA, bus.TypeGitHeadChanged, "main")
	}
	f.stores[repoA].Expire(2)
	c, _ := f.open(repoA, "", "Last-Event-ID", cursorOf(repoA, 1))
	if r := decode[ResyncFrame](t, c.expect(t, FrameResync)); r.Reason != ResyncCursorExpired {
		t.Fatalf("reason = %s", r.Reason)
	}
	if snap := c.expect(t, FrameSnapshot); snap.id != cursorOf(repoA, 4) {
		t.Fatalf("snapshot id = %s", snap.id)
	}

	// A cursor ahead of the store (the store was reset) is expired too.
	c, _ = f.open(repoA, "", "Last-Event-ID", cursorOf(repoA, 99))
	if r := decode[ResyncFrame](t, c.expect(t, FrameResync)); r.Reason != ResyncCursorExpired {
		t.Fatalf("future cursor reason = %s", r.Reason)
	}

	for range store.MaxReplay + 1 {
		if _, err := f.stores[repoA].Append(context.Background(), bus.Event{Type: bus.TypeGitIndexChanged, Source: "test", Repo: repoA, Worktree: "main"}.Normalize(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	c, _ = f.open(repoA, "", "Last-Event-ID", cursorOf(repoA, 3))
	if r := decode[ResyncFrame](t, c.expect(t, FrameResync)); r.Reason != ResyncReplayLimit {
		t.Fatalf("distant cursor reason = %s", r.Reason)
	}
	c.expect(t, FrameSnapshot)
}

func TestStreamResyncsOnOverflow(t *testing.T) {
	f := newStreamFixture(t, 0, repoA)
	c, _ := f.open(repoA, "")
	c.expect(t, FrameSnapshot)
	f.publish(repoA, bus.TypeGitHeadChanged, "main")
	c.expect(t, FrameDelta)
	f.srv[repoA].events.projector.broadcast(streamUpdate{resync: true})
	if r := decode[ResyncFrame](t, c.expect(t, FrameResync)); r.Reason != ResyncOverflow {
		t.Fatalf("reason = %s", r.Reason)
	}
	if fr := c.expect(t, FrameSnapshot); fr.id != cursorOf(repoA, 1) {
		t.Fatalf("snapshot id = %s", fr.id)
	}
	c.quiet(t, 100*time.Millisecond)
}

func TestProjectorCoalescesOverflowMarkers(t *testing.T) {
	p := newProjector(repoA, []Projection{&fakeProjection{repo: repoA, worktrees: []bus.WorktreeID{"main"}, states: map[bus.WorktreeID]string{"main": `{}`}}})
	l, stop := p.listen()
	defer stop()
	marker := bus.Event{Type: bus.TypeStreamOverflow, Source: "bus"}
	head := bus.Event{Type: bus.TypeGitHeadChanged, Source: "test", Repo: repoA, Worktree: "main", Seq: 5}
	index := bus.Event{Type: bus.TypeGitIndexChanged, Source: "test", Repo: repoA, Worktree: "main", Seq: 6}
	p.process(context.Background(), []bus.Event{marker, marker, head, index})
	var got []streamUpdate
	for len(l.ch) > 0 {
		got = append(got, <-l.ch)
	}
	// One resync for two markers; the key is computed once and its state
	// rides on the last event that touched it.
	if len(got) != 3 || !got[0].resync || got[1].event.Seq != 5 || len(got[1].states) != 0 || got[2].event.Seq != 6 || len(got[2].states) != 1 {
		t.Fatalf("updates = %+v", got)
	}
}

func TestProjectorSignalsSlowListeners(t *testing.T) {
	p := newProjector(repoA, nil)
	l, stop := p.listen()
	defer stop()
	for i := range listenerBuffer + 1 {
		p.broadcast(streamUpdate{event: bus.Event{Seq: uint64(i + 1)}})
	}
	select {
	case <-l.lost:
	default:
		t.Fatal("a dropped update did not signal the listener")
	}
}

func TestStreamRejections(t *testing.T) {
	f := newStreamFixture(t, 0, repoA, repoB)
	many := strings.Repeat("&topic="+url.QueryEscape("*/*/git.**"), MaxStreamTopics+1)
	for _, c := range []struct {
		name    string
		query   string
		headers []string
		want    int
	}{
		{"no session", "", []string{"Authorization", ""}, http.StatusUnauthorized},
		{"bad session", "", []string{"Authorization", "Bearer " + grantToken(randomKey(), "tab/v1", time.Now().Add(time.Hour))}, http.StatusUnauthorized},
		{"cross origin", "", []string{"Origin", "http://evil.localhost"}, http.StatusForbidden},
		{"malformed topic", "?topic=" + url.QueryEscape("not a topic"), nil, http.StatusBadRequest},
		{"too many topics", "?" + many[1:], nil, http.StatusBadRequest},
		{"other repository topic", "?topic=" + url.QueryEscape(string(repoB)+"/*/**"), nil, http.StatusForbidden},
		{"global topic", "?topic=" + url.QueryEscape("global/*/**"), nil, http.StatusForbidden},
		{"mixed topics", "?topic=" + url.QueryEscape("*/*/git.**") + "&topic=" + url.QueryEscape(string(repoB)+"/main/**"), nil, http.StatusForbidden},
		{"malformed cursor", "", []string{"Last-Event-ID", "v1:nope"}, http.StatusBadRequest},
		{"other repository cursor", "", []string{"Last-Event-ID", cursorOf(repoB, 3)}, http.StatusForbidden},
		{"cursor covering another repository", "", []string{"Last-Event-ID", bus.Cursor{repoA: 1, repoB: 3}.String()}, http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, status := f.open(repoA, c.query, c.headers...); status != c.want {
				t.Fatalf("status = %d, want %d", status, c.want)
			}
		})
	}

	// A session of one console never authenticates another clone's console.
	other := f.srv[repoB]
	req, _ := http.NewRequest(http.MethodGet, f.srv[repoA].origin+EventStreamPath, nil)
	req.Header.Set("Authorization", "Bearer "+grantToken(other.session, "tab/v1", time.Now().Add(time.Hour)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-clone session status = %d", resp.StatusCode)
	}
}

func TestStreamNeverCarriesAnotherRepository(t *testing.T) {
	f := newStreamFixture(t, 0, repoA, repoB)
	c, _ := f.open(repoA, "?topic="+url.QueryEscape("*/*/**"))
	snap := decode[SnapshotFrame](t, c.expect(t, FrameSnapshot))
	for _, s := range snap.States {
		if s.Repo != repoA {
			t.Fatalf("snapshot leaked %+v", s)
		}
	}
	f.publish(repoB, bus.TypeGitHeadChanged, "main")
	f.publish(repoB, bus.TypeGitRefsChanged, "")
	f.publish(repoA, bus.TypeGitHeadChanged, "main")
	d := c.expect(t, FrameDelta)
	delta := decode[DeltaFrame](t, d)
	if delta.Event.Repo != repoA || d.id != cursorOf(repoA, 1) || strings.Contains(d.data, string(repoB)) {
		t.Fatalf("delta = %s %s", d.id, d.data)
	}
	c.quiet(t, 100*time.Millisecond)

	// Replay reads only this repository's store, whatever the cursor.
	c, _ = f.open(repoA, "", "Last-Event-ID", cursorOf(repoA, 1))
	if snap := c.expect(t, FrameSnapshot); snap.id != cursorOf(repoA, 1) || strings.Contains(snap.data, string(repoB)) {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestStreamConnectionLimitPerOrigin(t *testing.T) {
	f := newStreamFixture(t, 0, repoA)
	var open []*sseClient
	for range MaxStreamsPerOrigin {
		c, status := f.open(repoA, "")
		if c == nil {
			t.Fatalf("stream refused with %d", status)
		}
		c.expect(t, FrameSnapshot)
		open = append(open, c)
	}
	if _, status := f.open(repoA, ""); status != http.StatusTooManyRequests {
		t.Fatalf("stream %d status = %d", MaxStreamsPerOrigin+1, status)
	}
	open[0].resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, status := f.open(repoA, "")
		if c != nil {
			c.expect(t, FrameSnapshot)
			return
		}
		if status != http.StatusTooManyRequests || time.Now().After(deadline) {
			t.Fatalf("after closing one stream: %d", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStreamPingsAndOutlivesReadTimeout(t *testing.T) {
	old := streamHeartbeat
	streamHeartbeat = 50 * time.Millisecond
	t.Cleanup(func() { streamHeartbeat = old })
	f := newStreamFixture(t, 150*time.Millisecond, repoA)
	c, _ := f.open(repoA, "")
	c.expect(t, FrameSnapshot)
	for range 2 {
		select {
		case <-c.pings:
		case <-time.After(2 * time.Second):
			t.Fatal("no ping")
		}
	}
	time.Sleep(300 * time.Millisecond) // past the server's ReadTimeout
	f.publish(repoA, bus.TypeGitHeadChanged, "main")
	c.expect(t, FrameDelta)
}

func TestStreamUnavailableWithoutEvents(t *testing.T) {
	s := testServer(t)
	rec := request(t, s, http.MethodGet, EventStreamPath, "", "session", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestStreamTopicParsing(t *testing.T) {
	topics, _, err := streamTopics(nil, repoA)
	if err != nil || len(topics) != 1 || topics[0].String() != fmt.Sprintf("%s/*/**", repoA) {
		t.Fatalf("default topics = %v, %v", topics, err)
	}
	topics, _, err = streamTopics([]string{"*/main/config.**"}, repoA)
	if err != nil || topics[0].Repo != repoA || topics[0].Worktree != "main" {
		t.Fatalf("topics = %v, %v", topics, err)
	}
}
