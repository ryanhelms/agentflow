package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// streamFixture is a scripted /api/events endpoint for browser tests. The
// test page writes the SSE text itself, so it controls every frame and
// chunk boundary of the wire format in the event-driven-platform ADR.
type streamFixture struct {
	authorized func(*http.Request) bool

	mu       sync.Mutex
	streams  map[chan string]struct{}
	requests []map[string]string
	status   int
}

func newStreamFixture(authorized func(*http.Request) bool) *streamFixture {
	return &streamFixture{authorized: authorized, streams: map[chan string]struct{}{}}
}

// events serves GET <worktree>/api/events.
func (f *streamFixture) events(w http.ResponseWriter, r *http.Request) {
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

func (f *streamFixture) open() int {
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
func (f *streamFixture) install(mux *http.ServeMux) {
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
