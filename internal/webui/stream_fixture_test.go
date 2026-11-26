package webui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// scriptedStream is a scripted /api/events endpoint for browser tests. The
// test page writes the SSE text itself, so it controls every frame and
// chunk boundary of the wire format in the event-driven-platform ADR.
type scriptedStream struct {
	authorized func(*http.Request) bool

	mu       sync.Mutex
	streams  map[chan string]struct{}
	requests []map[string]string
	status   int
}

func newScriptedStream(authorized func(*http.Request) bool) *scriptedStream {
	return &scriptedStream{authorized: authorized, streams: map[chan string]struct{}{}}
}

// events serves GET <worktree>/api/events.
func (f *scriptedStream) events(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, map[string]string{
		"authorization": r.Header.Get("Authorization"), "last_event_id": r.Header.Get("Last-Event-ID"),
		"accept": r.Header.Get("Accept"), "query": r.URL.RawQuery, "path": r.URL.Path,
	})
	status := f.status
	f.mu.Unlock()
	if !f.authorized(r) {
		problem(w, http.StatusUnauthorized, "fixture session ended")
		return
	}
	if status != 0 {
		problem(w, status, "fixture stream unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	_, _ = io.WriteString(w, "retry: 250\n\n")
	flusher.Flush()
	stream := make(chan string, 64)
	f.mu.Lock()
	f.streams[stream] = struct{}{}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.streams, stream)
		f.mu.Unlock()
	}()
	for {
		select {
		case <-r.Context().Done():
			return
		case text, open := <-stream:
			if !open {
				return
			}
			_, _ = io.WriteString(w, text)
			flusher.Flush()
		}
	}
}

func (f *scriptedStream) open() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.streams)
}

// install adds the routes the test page drives the stream with:
//
//	POST /fixture/stream/emit     write the body to the open stream, waiting for one
//	POST /fixture/stream/drop     end every open stream
//	POST /fixture/stream/status   answer later requests with ?code= (0 serves the stream)
//	GET  /fixture/stream/requests the requests seen so far
func (f *scriptedStream) install(mux *http.ServeMux) {
	mux.HandleFunc("POST /fixture/stream/emit", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "invalid fixture frame", http.StatusBadRequest)
			return
		}
		deadline := time.Now().Add(5 * time.Second)
		for f.open() == 0 {
			if time.Now().After(deadline) {
				http.Error(w, "no open stream", http.StatusConflict)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		f.mu.Lock()
		for stream := range f.streams {
			stream <- string(body)
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /fixture/stream/drop", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		for stream := range f.streams {
			close(stream)
			delete(f.streams, stream)
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /fixture/stream/status", func(w http.ResponseWriter, r *http.Request) {
		code := 0
		switch r.URL.Query().Get("code") {
		case "404":
			code = http.StatusNotFound
		case "429":
			code = http.StatusTooManyRequests
		case "503":
			code = http.StatusServiceUnavailable
		}
		f.mu.Lock()
		f.status = code
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /fixture/stream/requests", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.requests)
	})
}

// tabsStream is an /api/events endpoint for a console without an event
// runtime: it serves only the tabs projection of one tab hub, as the daemon
// does, so browser tests can drive tab navigation over the stream.
type tabsStream struct {
	hub       *tabHub
	worktrees []string

	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// newTabsStream follows hub for the worktrees with these route IDs.
func newTabsStream(hub *tabHub, worktrees ...string) *tabsStream {
	f := &tabsStream{hub: hub, worktrees: worktrees, subs: map[chan struct{}]struct{}{}}
	hub.onChange(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for sub := range f.subs {
			select {
			case sub <- struct{}{}:
			default:
			}
		}
	})
	return f
}

func (f *tabsStream) frame(name string, seq int) string {
	const repo = "3f2a9c1b7d4e5f60"
	states := make([]map[string]any, 0, len(f.worktrees))
	for _, id := range f.worktrees {
		states = append(states, map[string]any{"projection": ProjectionTabs, "repo": repo, "worktree": id, "state": f.hub.tabsState("/w/" + id + "/")})
	}
	cursor := fmt.Sprintf("v1:%s=%d", repo, seq)
	body := map[string]any{"cursor": cursor, "states": states}
	if name == "delta" {
		body = map[string]any{"event": map[string]any{"id": fmt.Sprintf("e%d", seq), "type": "ui.tabs.changed", "repo": repo, "seq": seq}, "states": states}
	}
	data, _ := json.Marshal(body)
	return fmt.Sprintf("event: %s\nid: %s\ndata: %s\n\n", name, cursor, data)
}

// events serves GET <worktree>/api/events for sessions authorized accepts.
func (f *tabsStream) events(authorized func(*http.Request) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			problem(w, http.StatusUnauthorized, "fixture session ended")
			return
		}
		tabs, err := streamTabs(r.URL.Query()["tab"])
		if err != nil {
			problem(w, http.StatusBadRequest, err.Error())
			return
		}
		defer f.hub.hold(tabs)()
		sub := make(chan struct{}, 1)
		f.mu.Lock()
		f.subs[sub] = struct{}{}
		f.mu.Unlock()
		defer func() {
			f.mu.Lock()
			delete(f.subs, sub)
			f.mu.Unlock()
		}()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		seq := 1
		_, _ = io.WriteString(w, "retry: 250\n\n"+f.frame("snapshot", seq))
		flusher.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-sub:
				seq++
				_, _ = io.WriteString(w, f.frame("delta", seq))
				flusher.Flush()
			}
		}
	}
}
