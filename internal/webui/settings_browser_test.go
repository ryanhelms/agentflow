package webui

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Drive the Settings page from a scripted stream and fake settings
// endpoints: controls for every widget kind, the Save bar, live checks, the
// review, conflicts, undo, search and the draft that survives a reload.
//
// The endpoints of lane 4.2 (internal/webui/settings.go) are faked here by
// settingsFixture, with the routes and bodies of the ADR, section "Phase 4
// console endpoints".
func TestSettingsBrowser(t *testing.T) {
	runBrowserFixture(t, "/w/first/", func(mux *http.ServeMux, origin string) {
		settingsConsoleFixture(t, mux, origin, "testdata/settings/settings-test.js")
	})
}

// AGENTFLOW_SETTINGS_DEMO is an address such as 127.0.0.1:41733. With it
// set, this test serves the Settings page with the fixture catalog and a
// scripted backend that runs in the page (testdata/settings/demo.js) until
// the test is stopped, for a design review in a browser:
//
//	AGENTFLOW_SETTINGS_DEMO=127.0.0.1:41733 go test ./internal/webui -run TestSettingsDemo -timeout 0
func TestSettingsDemo(t *testing.T) {
	addr := os.Getenv("AGENTFLOW_SETTINGS_DEMO")
	if addr == "" {
		t.Skip("set AGENTFLOW_SETTINGS_DEMO to an address to serve the Settings page fixture")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	settingsConsoleFixture(t, mux, "http://"+listener.Addr().String(), "testdata/settings/demo.js")
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-t.Context().Done(); _ = srv.Close() }()
	t.Logf("serving the Settings page fixture at http://%s/w/first/", listener.Addr())
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

// settingsFixture fakes the settings endpoints. The test page queues the
// answers; it sees its own requests by wrapping fetch.
//
//	POST /fixture/settings/answer?route=preview|save|undo  queue the body as the next answer
//
// The last queued answer of a route repeats until another is queued. With
// &reset the queue is emptied first.
type settingsFixture struct {
	mu      sync.Mutex
	answers map[string][]json.RawMessage
}

func (f *settingsFixture) answer(route string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	queue := f.answers[route]
	if len(queue) == 0 {
		return nil
	}
	if len(queue) > 1 {
		f.answers[route] = queue[1:]
	}
	return queue[0]
}

// settingsConsoleFixture serves the embedded console at /w/first/ with a
// scripted event stream, the fixture catalog and fake settings endpoints,
// and runs the checks file in it.
func settingsConsoleFixture(t *testing.T, mux *http.ServeMux, origin, checksFile string) {
	t.Helper()
	s := testServer(t)
	s.origin = origin
	s.worktreePath = "/w/first/"
	handler := s.handler()
	worktree := http.NewServeMux()
	mux.Handle("/w/first/", http.StripPrefix("/w/first", worktree))
	worktree.Handle("/", handler)
	stream := newScriptedStream(s.authorized)
	stream.install(mux)
	worktree.HandleFunc("GET /api/events", stream.events)

	read := func(name string) []byte {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	catalog := read("testdata/settings/catalog.json")
	checks := read(checksFile)
	page, err := assets.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &settingsFixture{answers: map[string][]json.RawMessage{}}
	// The console's own session check, as the real endpoints run it.
	refused := func(w http.ResponseWriter, r *http.Request) bool {
		if s.authorized(r) {
			return false
		}
		problem(w, http.StatusUnauthorized, "fixture session ended")
		return true
	}
	worktree.HandleFunc("GET /api/settings/catalog", func(w http.ResponseWriter, r *http.Request) {
		if refused(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(catalog)
	})
	for _, route := range []string{"preview", "save", "undo"} {
		worktree.HandleFunc("POST /api/settings/"+route, func(w http.ResponseWriter, r *http.Request) {
			if refused(w, r) {
				return
			}
			body := fixture.answer(route)
			if body == nil {
				problem(w, http.StatusServiceUnavailable, "no fixture answer for "+route)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}
	mux.HandleFunc("POST /fixture/settings/answer", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil || !json.Valid(body) {
			http.Error(w, "invalid fixture answer", http.StatusBadRequest)
			return
		}
		route := r.URL.Query().Get("route")
		fixture.mu.Lock()
		if r.URL.Query().Has("reset") {
			fixture.answers[route] = nil
		}
		fixture.answers[route] = append(fixture.answers[route], body)
		fixture.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	worktree.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, strings.Replace(string(page), "</head>", `<script src="/fixture/checks.js" defer></script></head>`, 1))
	})
	mux.HandleFunc("GET /fixture/checks.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write(checks)
	})
	mux.HandleFunc("POST /fixture/ticket", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, mintLaunch(s.instanceKey, time.Now()))
	})
}
