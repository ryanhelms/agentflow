package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
)

// streamHeartbeat is StreamHeartbeat; tests shorten it.
var streamHeartbeat = StreamHeartbeat

// streamLimit caps concurrent event streams per console origin. Every
// worktree of a clone shares one origin, so the daemon keeps one per clone.
type streamLimit struct {
	mu sync.Mutex
	n  int
}

func (l *streamLimit) acquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n >= MaxStreamsPerOrigin {
		return false
	}
	l.n++
	return true
}

func (l *streamLimit) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.n--
}

func (s *server) streamLimiter() *streamLimit {
	s.streamsOnce.Do(func() {
		if s.streams == nil {
			s.streams = &streamLimit{}
		}
	})
	return s.streams
}

// maxStreamTabs caps the tab= parameters of one stream (ADR, "Tabs on the
// stream").
const maxStreamTabs = 16

// globalStreamFamilies are the only event families a stream may follow in
// bus.GlobalRepo (ADR, "Phase 2 scoping", rule 2).
var globalStreamFamilies = []string{"config", "client"}

// globalPatternAllowed reports whether p selects only events of
// globalStreamFamilies: its first segment names one of them literally.
func globalPatternAllowed(p bus.Pattern) bool {
	first, _, _ := strings.Cut(string(p), ".")
	return slices.Contains(globalStreamFamilies, first)
}

// streamTopics parses the topic= parameters of a stream for repository
// repo. A malformed topic is a 400. A topic naming any other repository is
// a 403, and so is a "global" topic outside config.** and client.**. No
// topic means every event of repo.
func streamTopics(raw []string, repo bus.RepoID) ([]bus.Topic, int, error) {
	if len(raw) > MaxStreamTopics {
		return nil, http.StatusBadRequest, fmt.Errorf("at most %d topics", MaxStreamTopics)
	}
	if len(raw) == 0 {
		return []bus.Topic{{Repo: repo, Worktree: bus.Any, Types: "**"}}, 0, nil
	}
	topics := make([]bus.Topic, 0, len(raw))
	for _, r := range raw {
		t, err := bus.ParseTopic(r)
		if err != nil {
			return nil, http.StatusBadRequest, errors.New("invalid topic")
		}
		if t.Repo == bus.Any {
			t.Repo = repo
		}
		if t.Repo == bus.GlobalRepo {
			if !globalPatternAllowed(t.Types) {
				return nil, http.StatusForbidden, errors.New("global topics may follow only config.** and client.**")
			}
			if t.Worktree != bus.Any {
				return nil, http.StatusBadRequest, errors.New("global topics have no worktree")
			}
			topics = append(topics, t)
			continue
		}
		if t.Repo != repo {
			return nil, http.StatusForbidden, errors.New("topic outside this console's repository")
		}
		topics = append(topics, t)
	}
	return topics, 0, nil
}

// streamCursor parses Last-Event-ID. A cursor with an entry for any other
// repository than repo and "global" is a 403: it would otherwise reveal or
// widen another repository's position.
func streamCursor(raw string, repo bus.RepoID) (bus.Cursor, int, error) {
	c, err := bus.ParseCursor(strings.TrimSpace(raw))
	if err != nil {
		return nil, http.StatusBadRequest, errors.New("invalid Last-Event-ID")
	}
	for r := range c {
		if r != repo && r != bus.GlobalRepo {
			return nil, http.StatusForbidden, errors.New("Last-Event-ID outside this console's repository")
		}
	}
	return c, 0, nil
}

// streamTabs parses the tab= parameters: the tabs of the browser that this
// stream keeps attached.
func streamTabs(raw []string) ([]string, error) {
	if len(raw) > maxStreamTabs {
		return nil, fmt.Errorf("at most %d tabs", maxStreamTabs)
	}
	tabs := make([]string, 0, len(raw))
	for _, id := range raw {
		if !validTabID(id) {
			return nil, errors.New("invalid tab id")
		}
		if !slices.Contains(tabs, id) {
			tabs = append(tabs, id)
		}
	}
	return tabs, nil
}

// eventStream serves GET <prefix>/api/events: the SSE stream of the ADR
// event-driven-platform. The handler wrapper has already checked the host,
// the origin and the Bearer session.
func (s *server) eventStream(w http.ResponseWriter, r *http.Request) {
	ev := s.events
	if ev == nil {
		problem(w, http.StatusServiceUnavailable, "live events are not available for this console")
		return
	}
	topics, status, err := streamTopics(r.URL.Query()["topic"], ev.id)
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	last, status, err := streamCursor(r.Header.Get("Last-Event-ID"), ev.id)
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	tabs, err := streamTabs(r.URL.Query()["tab"])
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	until, _ := grantDeadline(token, s.session, "tab/v1", time.Now(), tabSessionFor)
	limit := s.streamLimiter()
	if !limit.acquire() {
		problem(w, http.StatusTooManyRequests, "too many event streams for this console")
		return
	}
	defer limit.release()
	if len(tabs) > 0 {
		// The listed tabs stay attached while this stream is open, and for
		// TabGrace after it closes.
		defer s.tabRoutes().hold(tabs)()
	}

	// Subscribe before reading the store head, so nothing falls between the
	// snapshot and the first delta.
	l, stop := ev.projector.listen()
	defer stop()

	rc := http.NewResponseController(w)
	// The daemon's ReadTimeout would otherwise end a long-lived stream.
	_ = rc.SetReadDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	st := &sseStream{w: w, rc: rc, ev: ev, topics: topics, listener: l}
	if st.raw(fmt.Sprintf("retry: %d\n\n", StreamRetry)) != nil {
		return
	}
	ctx := r.Context()
	if err := st.start(ctx, last); err != nil {
		return
	}
	ping := time.NewTicker(streamHeartbeat)
	defer ping.Stop()
	expiry := time.NewTimer(time.Until(until))
	defer expiry.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry.C:
			return // the session ended; the client reauthenticates
		case <-ping.C:
			if st.raw(": ping\n\n") != nil {
				return
			}
		case <-l.lost:
			if st.resync(ctx, ResyncOverflow) != nil {
				return
			}
		case u := <-l.ch:
			if err := st.apply(ctx, u); err != nil {
				return
			}
		}
	}
}

type sseStream struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	ev       *repoEvents
	topics   []bus.Topic
	listener *listener
	cursor   bus.Cursor
	sent     map[ProjectionKey]map[string]string // last state JSON sent, by key and projection
}

func (st *sseStream) raw(s string) error {
	if _, err := fmt.Fprint(st.w, s); err != nil {
		return err
	}
	return st.rc.Flush()
}

func (st *sseStream) frame(name string, id *bus.Cursor, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var f strings.Builder
	f.WriteString("event: " + name + "\n")
	if id != nil {
		f.WriteString("id: " + id.String() + "\n")
	}
	f.WriteString("data: ")
	f.Write(b)
	f.WriteString("\n\n")
	return st.raw(f.String())
}

// followsGlobal reports whether the stream names bus.GlobalRepo in a
// topic. Only then does its cursor carry a global entry.
func (st *sseStream) followsGlobal() bool {
	for _, t := range st.topics {
		if t.Repo == bus.GlobalRepo {
			return true
		}
	}
	return false
}

// globalStore is the store whose events the stream may replay besides its
// repository's: the global one, when the stream follows it.
func (st *sseStream) globalStore() store.Store {
	if st.ev.global == nil || !st.followsGlobal() {
		return nil
	}
	return st.ev.global
}

// head returns the store heads of this repository and, when the stream
// follows it, of "global"; nothing of any other repository.
func (st *sseStream) head(ctx context.Context) (bus.Cursor, error) {
	c := bus.Cursor{}
	for repo, s := range map[bus.RepoID]store.Store{st.ev.id: st.ev.store, bus.GlobalRepo: st.globalStore()} {
		if s == nil {
			continue
		}
		h, err := s.Head(ctx)
		if err != nil {
			return nil, err
		}
		if seq := h[repo]; seq > 0 {
			c[repo] = seq
		}
	}
	return c, nil
}

// start sends the replay after last (if any) and the first snapshot.
func (st *sseStream) start(ctx context.Context, last bus.Cursor) error {
	head, err := st.head(ctx)
	if err != nil {
		return err
	}
	cursor := last.Clone()
	for _, part := range []struct {
		repo bus.RepoID
		s    store.Store
	}{{st.ev.id, st.ev.store}, {bus.GlobalRepo, st.globalStore()}} {
		if part.s == nil || last[part.repo] == 0 {
			continue
		}
		reason, err := st.replay(ctx, part.s, part.repo, last, head, cursor)
		if err != nil {
			return err
		}
		if reason != "" {
			return st.resync(ctx, reason)
		}
	}
	return st.snapshot(ctx, head)
}

// replay sends the stored events of repo after last, up to head, as deltas
// without states. cursor starts as last and advances with each event, so
// each delta's id covers every store replayed so far. It returns a resync reason
// when it cannot.
func (st *sseStream) replay(ctx context.Context, s store.Reader, repo bus.RepoID, last, head, cursor bus.Cursor) (ResyncReason, error) {
	seen, newest := last[repo], head[repo]
	switch {
	case seen > newest:
		return ResyncCursorExpired, nil // the store was reset since
	case newest-seen > store.MaxReplay:
		return ResyncReplayLimit, nil
	}
	for e, err := range s.ReadFrom(ctx, bus.Cursor{repo: seen}) {
		if errors.Is(err, store.ErrCursorExpired) {
			return ResyncCursorExpired, nil
		}
		if err != nil {
			return "", err
		}
		if e.Repo != repo || e.Seq > newest {
			break
		}
		cursor[repo] = e.Seq
		if !bus.MatchAny(st.topics, e) {
			continue
		}
		id := cursor.Clone()
		if err := st.frame(FrameDelta, &id, DeltaFrame{Event: e, States: []ProjectionState{}}); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (st *sseStream) snapshot(ctx context.Context, head bus.Cursor) error {
	states := st.ev.projector.snapshot(ctx, func(p Projection, k ProjectionKey) bool { return selects(st.topics, p, k) })
	st.cursor = head
	st.sent = map[ProjectionKey]map[string]string{}
	kept := states[:0]
	for _, s := range states {
		if string(s.State) == "null" {
			continue
		}
		st.remember(s)
		kept = append(kept, s)
	}
	return st.frame(FrameSnapshot, &head, SnapshotFrame{Cursor: head.String(), States: kept})
}

func (st *sseStream) remember(s ProjectionState) {
	k := ProjectionKey{Repo: s.Repo, Worktree: s.Worktree}
	if st.sent[k] == nil {
		st.sent[k] = map[string]string{}
	}
	if string(s.State) == "null" {
		delete(st.sent[k], s.Projection)
		return
	}
	st.sent[k][s.Projection] = string(s.State)
}

// resync discards everything queued for this stream, then sends resync and
// a fresh snapshot. Queued events are older than the new head, so the
// snapshot covers them, and back-to-back overflow markers collapse into
// one resync.
func (st *sseStream) resync(ctx context.Context, reason ResyncReason) error {
	for drained := false; !drained; {
		select {
		case <-st.listener.ch:
		case <-st.listener.lost:
		default:
			drained = true
		}
	}
	if err := st.frame(FrameResync, nil, ResyncFrame{Reason: reason}); err != nil {
		return err
	}
	head, err := st.head(ctx)
	if err != nil {
		return err
	}
	return st.snapshot(ctx, head)
}

func (st *sseStream) apply(ctx context.Context, u streamUpdate) error {
	if u.resync {
		return st.resync(ctx, ResyncOverflow)
	}
	e := u.event
	global := e.Repo == bus.GlobalRepo
	if e.Repo != "" && e.Repo != st.ev.id && !global {
		return nil // never another repository's event
	}
	if e.Seq > 0 && (!global || st.followsGlobal()) {
		if st.cursor.Seen(e) {
			return nil // the snapshot already reflects it
		}
		st.cursor = st.cursor.Advance(e)
	}
	matched := bus.MatchAny(st.topics, e)
	if !matched && !global {
		return nil
	}
	states := []ProjectionState{}
	for _, s := range u.states {
		if s.Repo != st.ev.id || !st.selected(s) {
			continue
		}
		k := ProjectionKey{Repo: s.Repo, Worktree: s.Worktree}
		prev, had := st.sent[k][s.Projection]
		if string(s.State) == "null" && !had || had && prev == string(s.State) {
			continue
		}
		st.remember(s)
		states = append(states, s)
	}
	if !matched && len(states) == 0 {
		// A global event the stream does not follow is sent only for the
		// repository states it changed, such as overview.config after a
		// personal-layer edit.
		return nil
	}
	id := st.cursor
	return st.frame(FrameDelta, &id, DeltaFrame{Event: e, States: states})
}

func (st *sseStream) selected(s ProjectionState) bool {
	for _, p := range st.ev.projector.projections {
		if p.Name() == s.Projection {
			return selects(st.topics, p, ProjectionKey{Repo: s.Repo, Worktree: s.Worktree})
		}
	}
	return false
}
