package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Each hub belongs to one local clone. Streams and acknowledgements are
// authenticated against the worktree the tab is currently displaying.
type tabHub struct {
	mu       sync.Mutex
	tabs     []*consoleTab
	attached chan struct{} // closed when the first authenticated tab connects
	// holds counts, per tab ID, the event streams whose tab= parameters
	// list it. A live tab stays attached while it is held, plus TabGrace.
	holds map[string]int
	// notify publishes ui.tabs.changed; set by repoEvents.bindTabs.
	notify func()
	// dirty records a change made under mu; unlock publishes it.
	dirty bool
}

type consoleTab struct {
	scope   string // verified from the worktree-specific Commander capability on this stream
	id      string
	origin  string
	path    string
	events  chan navigation
	done    chan struct{}
	pending map[string]chan tabOutcome // guarded by tabHub.mu
	stream  chan struct{}              // closed when another connection takes over
	retire  *time.Timer
	// live tabs attached with POST /api/tabs/attach and receive navigation
	// in the tabs projection over the event stream; the others use the
	// old GET /api/tabs/events stream.
	live       bool
	attachedAt time.Time
	retireGen  int            // invalidates an armed grace timer
	nav        *TabNavigation // pending navigation of a live tab, guarded by tabHub.mu
}

// tabGrace is TabGrace; tests shorten it.
var tabGrace = TabGrace

// onChange sets the function that publishes ui.tabs.changed.
func (h *tabHub) onChange(notify func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notify = notify
}

// unlock releases mu and, when the tabs changed, publishes ui.tabs.changed
// outside the lock: the tabs projection reads the hub again.
func (h *tabHub) unlock() {
	notify := h.notify
	changed := h.dirty
	h.dirty = false
	h.mu.Unlock()
	if changed && notify != nil {
		notify()
	}
}

type navigation struct {
	Destination
	ID     string `json:"id"`
	Target string `json:"target,omitempty"`
}

// A live tab can block a switch to protect local edits or a publication.
// Treat that as a terminal result, so callers can explain what needs attention.
type tabOutcome string

const (
	tabMissing     tabOutcome = ""
	tabAccepted    tabOutcome = "accepted"
	tabStorageFull tabOutcome = "storage"
	tabPublishing  tabOutcome = "publishing"
	tabAutonomy    tabOutcome = "autonomy"
)

func (o tabOutcome) err() error {
	switch o {
	case tabStorageFull:
		return errors.New("the open console could not save its worktree state. Keep that tab open, save your edits or free browser storage, then retry")
	case tabAutonomy:
		return errors.New("save or discard the open console’s autonomy edits before switching worktrees")
	case tabPublishing:
		return errors.New("the open console is saving a change. Wait for the save to finish, then retry")
	}
	return nil
}

// navigate leaves existing pages alone on automatic launches. A manual launch
// counts as handled only when the selected page has accepted its destination.
func (h *tabHub) navigateTo(ctx context.Context, d Destination, automatic bool, target string) tabOutcome {
	return h.navigateScoped(ctx, d, automatic, target, "", "")
}

func (h *tabHub) navigateScoped(ctx context.Context, d Destination, automatic bool, target, scope, scoped string) tabOutcome {
	h.mu.Lock()
	tabs := slices.Clone(h.tabs)
	h.mu.Unlock()
	if len(tabs) == 0 {
		return tabMissing
	}
	if automatic {
		return tabAccepted
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for _, tab := range slices.Backward(tabs) {
		targetForTab := target
		if scope != "" && tab.scope == scope {
			targetForTab = scoped
		}
		if outcome := h.navigateTab(ctx, tab, d, targetForTab); outcome != tabMissing {
			return outcome
		}
	}
	return tabMissing
}

func (h *tabHub) navigateTab(ctx context.Context, tab *consoleTab, d Destination, target string) tabOutcome {
	id := randomKey()
	event := navigation{Destination: d, ID: id}
	if target != "" {
		u, err := url.Parse(target)
		if err != nil || u.Scheme != "http" || u.Scheme+"://"+u.Host != tab.origin || u.User != nil || u.RawQuery != "" || !validWorktreePath(u.EscapedPath()) {
			return tabMissing
		}
		if u.Path != tab.path {
			event.Target = target
		}
	}
	ack := make(chan tabOutcome, 1)
	h.mu.Lock()
	tab.pending[id] = ack
	if tab.live {
		tab.nav = &TabNavigation{ID: id, Tab: tab.id, Destination: d, Target: event.Target}
		h.dirty = true
	}
	h.unlock()
	defer func() {
		h.mu.Lock()
		delete(tab.pending, id)
		if tab.nav != nil && tab.nav.ID == id {
			tab.nav = nil
			h.dirty = true
		}
		h.unlock()
	}()
	if tab.live {
		// The navigation is in the tab's worktree's tabs state now.
		select {
		case handled := <-ack:
			return handled
		case <-tab.done:
			return tabMissing
		case <-ctx.Done():
			return tabMissing
		}
	}
	select {
	case <-tab.done:
		return tabMissing
	case <-ctx.Done():
		return tabMissing
	case tab.events <- event:
	}
	select {
	case handled := <-ack:
		return handled
	case <-tab.done:
		return tabMissing
	case <-ctx.Done():
		return tabMissing
	}
}

func (h *tabHub) removeLocked(tab *consoleTab) {
	if i := slices.Index(h.tabs, tab); i >= 0 {
		h.tabs = slices.Delete(h.tabs, i, i+1)
		if tab.retire != nil {
			tab.retire.Stop()
		}
		close(tab.done)
		h.dirty = true
	}
}

// waitForAttachment bridges browser startup: registering a console launch
// precedes the browser's authenticated event stream by a separate round trip.
func (h *tabHub) waitForAttachment(ctx context.Context, until time.Time) {
	left := time.Until(until)
	if left <= 0 {
		return
	}
	h.mu.Lock()
	if len(h.tabs) > 0 {
		h.mu.Unlock()
		return
	}
	if h.attached == nil {
		h.attached = make(chan struct{})
	}
	attached := h.attached
	h.mu.Unlock()
	timer := time.NewTimer(left)
	defer timer.Stop()
	select {
	case <-attached:
	case <-ctx.Done():
	case <-timer.C:
	}
}

// Attach successive HTTP streams to the same tab. A graceful rotation keeps
// the tab ready for navigation while the browser frees a connection slot.
func (h *tabHub) attach(id, origin, path string, scopeID ...string) (*consoleTab, chan struct{}) {
	scope := ""
	if len(scopeID) > 0 {
		scope = scopeID[0]
	}
	h.mu.Lock()
	defer h.unlock()
	for _, tab := range h.tabs {
		if tab.id == id && tab.origin == origin && tab.path == path && !tab.live {
			if tab.scope != scope {
				h.removeLocked(tab)
				break
			}
			if tab.stream != nil {
				close(tab.stream)
			}
			if tab.retire != nil {
				tab.retire.Stop()
				tab.retire = nil
			}
			tab.stream = make(chan struct{})
			return tab, tab.stream
		}
	}
	if len(h.tabs) >= 64 {
		return nil, nil
	}
	tab := h.addLocked(id, origin, path, scope)
	tab.stream = make(chan struct{})
	return tab, tab.stream
}

func (h *tabHub) addLocked(id, origin, path, scope string) *consoleTab {
	tab := &consoleTab{id: id, scope: scope, origin: origin, path: path, events: make(chan navigation), done: make(chan struct{}), pending: make(map[string]chan tabOutcome), attachedAt: time.Now()}
	h.tabs = append(h.tabs, tab)
	h.dirty = true
	if h.attached != nil {
		close(h.attached)
		h.attached = nil
	}
	return tab
}

// attachLive attaches a tab that follows the event stream (POST
// /api/tabs/attach). The same tab moving to another worktree replaces its
// old entry. It reports false when the hub is full.
func (h *tabHub) attachLive(id, origin, path, scope string) bool {
	h.mu.Lock()
	defer h.unlock()
	for _, tab := range slices.Clone(h.tabs) {
		if tab.id != id || tab.origin != origin {
			continue
		}
		if tab.live && tab.path == path && tab.scope == scope {
			h.armLocked(tab)
			return true
		}
		if tab.live || tab.path == path {
			h.removeLocked(tab)
		}
	}
	if len(h.tabs) >= 64 {
		return false
	}
	tab := h.addLocked(id, origin, path, scope)
	tab.live = true
	h.armLocked(tab)
	return true
}

// armLocked starts a live tab's TabGrace timer unless a stream holds it.
func (h *tabHub) armLocked(tab *consoleTab) {
	tab.retireGen++
	if tab.retire != nil {
		tab.retire.Stop()
		tab.retire = nil
	}
	if h.holds[tab.id] > 0 {
		return
	}
	gen := tab.retireGen
	tab.retire = time.AfterFunc(tabGrace, func() {
		h.mu.Lock()
		defer h.unlock()
		if tab.retireGen == gen && h.holds[tab.id] == 0 {
			h.removeLocked(tab)
		}
	})
}

// hold keeps the live tabs ids attached until the returned release runs,
// and for TabGrace after it. An event stream holds the tabs of its tab=
// parameters for as long as it is open.
func (h *tabHub) hold(ids []string) (release func()) {
	h.mu.Lock()
	if h.holds == nil {
		h.holds = map[string]int{}
	}
	for _, id := range ids {
		h.holds[id]++
	}
	for _, tab := range h.tabs {
		if tab.live && slices.Contains(ids, tab.id) {
			h.armLocked(tab)
		}
	}
	h.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			for _, id := range ids {
				if h.holds[id]--; h.holds[id] <= 0 {
					delete(h.holds, id)
				}
			}
			for _, tab := range h.tabs {
				if tab.live && slices.Contains(ids, tab.id) {
					h.armLocked(tab)
				}
			}
			h.mu.Unlock()
		})
	}
}

func (h *tabHub) detach(tab *consoleTab, stream chan struct{}, rotating bool) {
	h.mu.Lock()
	defer h.unlock()
	if tab.stream != stream {
		return
	}
	if !rotating {
		h.removeLocked(tab)
		return
	}
	tab.retire = time.AfterFunc(3*time.Second, func() {
		h.mu.Lock()
		defer h.unlock()
		if tab.stream == stream {
			h.removeLocked(tab)
		}
	})
}

func validWorktreePath(path string) bool {
	id, ok := strings.CutPrefix(path, "/w/")
	return ok && strings.HasSuffix(id, "/") && routeLabel.MatchString(strings.TrimSuffix(id, "/"))
}

// A standalone server has its own hub. The daemon injects a clone-wide hub
// before serving, and supplies the authenticated worktree path for each stream.
func (s *server) tabRoutes() *tabHub {
	s.tabsOnce.Do(func() {
		if s.tabs == nil {
			s.tabs = &tabHub{}
		}
	})
	return s.tabs
}

// validTabID reports whether id is a tab ID: 32 lowercase hex characters.
func validTabID(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
}

// tabAttach serves POST /api/tabs/attach {tab}: the tab follows the event
// stream from now on. It is checked like the old stream: under this
// worktree's session and with the Commander scope of this request.
func (s *server) tabAttach(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tab string `json:"tab"`
	}
	if !read(w, r, &in) {
		return
	}
	if !validTabID(in.Tab) {
		problem(w, http.StatusBadRequest, "invalid tab id")
		return
	}
	_, scope := s.commanderFor(r)
	if !s.tabRoutes().attachLive(in.Tab, s.origin, s.worktreePath, scope) {
		problem(w, http.StatusTooManyRequests, "too many connected tabs")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// tabEvents serves the old GET /api/tabs/events navigation stream. Lane
// 2.5 moves the browser to POST /api/tabs/attach and the tabs projection;
// a follow-up on issue #36 then removes this endpoint.
func (s *server) tabEvents(w http.ResponseWriter, r *http.Request) {
	h := s.tabRoutes()
	id := r.URL.Query().Get("tab")
	if !validTabID(id) {
		problem(w, http.StatusBadRequest, "invalid tab id")
		return
	}
	_, scope := s.commanderFor(r)
	tab, stream := h.attach(id, s.origin, s.worktreePath, scope)
	if tab == nil {
		problem(w, http.StatusTooManyRequests, "too many connected tabs")
		return
	}
	rotating := false
	defer func() { h.detach(tab, stream, rotating) }()
	w.Header().Set("Content-Type", "text/event-stream")
	rc := http.NewResponseController(w)
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil || rc.Flush() != nil {
		return
	}
	// Periodically return the HTTP/1.1 slot to the browser's pool. Other
	// worktrees share this host, and may have requests queued behind streams.
	rotation := time.NewTimer(5 * time.Second)
	defer rotation.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tab.done:
			return
		case <-stream:
			return
		case <-rotation.C:
			rotating = true
			return
		case event := <-tab.events:
			data, err := json.Marshal(event)
			if err != nil {
				return
			}
			_, err = fmt.Fprintf(w, "event: navigate\ndata: %s\n\n", data)
			rotating = err == nil && rc.Flush() == nil
			return // Release the connection slot so even a busy browser can acknowledge.
		}
	}
}

func (s *server) tabAck(w http.ResponseWriter, r *http.Request) {
	h := s.tabRoutes()
	var input struct {
		Tab     string     `json:"tab"`
		ID      string     `json:"id"`
		Handled bool       `json:"handled"`
		Blocked tabOutcome `json:"blocked,omitempty"`
	}
	if !read(w, r, &input) {
		return
	}
	// Only accept known reasons. Browser-controlled text never enters chat logs.
	outcome := input.Blocked
	if input.Blocked != tabMissing && (input.Handled || (input.Blocked != tabStorageFull && input.Blocked != tabPublishing && input.Blocked != tabAutonomy)) {
		problem(w, http.StatusBadRequest, "unknown or conflicting navigation result")
		return
	}
	if input.Handled {
		outcome = tabAccepted
	}
	h.mu.Lock()
	for _, tab := range h.tabs {
		if ack := tab.pending[input.ID]; tab.id == input.Tab && tab.path == s.worktreePath && tab.origin == s.origin && ack != nil {
			select {
			case ack <- outcome:
			default:
			}
			h.mu.Unlock()
			reply(w, http.StatusOK, struct{}{})
			return
		}
	}
	h.mu.Unlock()
	problem(w, http.StatusNotFound, "navigation request has expired")
}

// A browser can close while its HTTP stream is rotating. Explicit departure
// clears that brief reconnection grace period as soon as the page leaves.
func (s *server) tabLeave(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tab string `json:"tab"`
	}
	if !read(w, r, &in) {
		return
	}
	h := s.tabRoutes()
	h.mu.Lock()
	for _, tab := range h.tabs {
		if tab.id == in.Tab && tab.path == s.worktreePath && tab.origin == s.origin {
			h.removeLocked(tab)
			break
		}
	}
	h.unlock()
	w.WriteHeader(http.StatusNoContent)
}
