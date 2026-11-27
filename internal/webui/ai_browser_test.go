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

// Drive the AI sessions page from a scripted stream and fake endpoints: the
// session list, the selected session with its stream, plan, tool calls,
// diffs and terminal output, the permission cards and their answers,
// Cancel, hostile agent text and every page state.
//
// The ai projection and the two endpoints (lane 7.2) are replaced here: the
// checks file writes AIState frames with the shapes of the ADR ("Phase 7 AI
// projection"), and aiEndpoints answers the requests. No test starts a
// harness or a session.
func TestAIBrowser(t *testing.T) {
	runBrowserFixture(t, "/w/first/", func(mux *http.ServeMux, origin string) {
		aiConsoleFixture(t, mux, origin, "testdata/ai/ai-test.js")
	})
}

// AGENTFLOW_AI_DEMO is an address such as 127.0.0.1:41736. With it set,
// this test serves the console with a scripted session
// (testdata/ai/demo.js) until the test is stopped, for a design review in
// a browser:
//
//	AGENTFLOW_AI_DEMO=127.0.0.1:41736 go test ./internal/webui -run TestAIDemo -timeout 0
func TestAIDemo(t *testing.T) {
	addr := os.Getenv("AGENTFLOW_AI_DEMO")
	if addr == "" {
		t.Skip("set AGENTFLOW_AI_DEMO to an address to serve the AI sessions page fixture")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	aiConsoleFixture(t, mux, "http://"+listener.Addr().String(), "testdata/ai/demo.js")
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-t.Context().Done(); _ = srv.Close() }()
	t.Logf("serving the AI sessions page fixture at http://%s/w/first/", listener.Addr())
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

// aiEndpoints stands in for lane 7.2's POST /api/ai/respond and
// /api/ai/cancel. It records each request and answers 202 {}, or the status
// a test asked for. Neither answer carries session data.
type aiEndpoints struct {
	mu       sync.Mutex
	requests []map[string]string
	status   map[string]int
}

func (f *aiEndpoints) serve(route string, authorized func(*http.Request) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
		f.mu.Lock()
		f.requests = append(f.requests, map[string]string{
			"route": route, "body": string(body), "content_type": r.Header.Get("Content-Type"),
			"console": r.Header.Get("X-Agentflow-Request"), "origin": r.Header.Get("Origin"),
		})
		status := f.status[route]
		f.mu.Unlock()
		switch {
		case !authorized(r):
			problem(w, http.StatusUnauthorized, "fixture session ended")
		case status == http.StatusTooManyRequests:
			problem(w, status, "This console answered too often. Wait a minute.")
		case status != 0:
			problem(w, status, "fixture refused the request")
		default:
			reply(w, http.StatusAccepted, map[string]string{})
		}
	}
}

// install adds the routes the test page reads the fake endpoints with:
//
//	GET  /fixture/ai/requests the respond and cancel requests seen so far
//	POST /fixture/ai/status   answer ?route= (respond or cancel) with ?code= (0 accepts)
func (f *aiEndpoints) install(mux *http.ServeMux) {
	mux.HandleFunc("GET /fixture/ai/requests", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.requests)
	})
	mux.HandleFunc("POST /fixture/ai/status", func(w http.ResponseWriter, r *http.Request) {
		code := 0
		switch r.URL.Query().Get("code") {
		case "429":
			code = http.StatusTooManyRequests
		case "503":
			code = http.StatusServiceUnavailable
		}
		f.mu.Lock()
		f.status[r.URL.Query().Get("route")] = code
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
}

// aiConsoleFixture serves the embedded console at /w/first/ with a scripted
// event stream and the fake session endpoints, and runs the checks file in
// it.
func aiConsoleFixture(t *testing.T, mux *http.ServeMux, origin, checksFile string) {
	t.Helper()
	s := testServer(t)
	s.origin = origin
	s.worktreePath = "/w/first/"
	worktree := http.NewServeMux()
	mux.Handle("/w/first/", http.StripPrefix("/w/first", worktree))
	worktree.Handle("/", s.handler())
	stream := newScriptedStream(s.authorized)
	stream.install(mux)
	worktree.HandleFunc("GET /api/events", stream.events)
	endpoints := &aiEndpoints{requests: []map[string]string{}, status: map[string]int{}}
	endpoints.install(mux)
	worktree.HandleFunc("POST /api/ai/respond", endpoints.serve("respond", s.authorized))
	worktree.HandleFunc("POST /api/ai/cancel", endpoints.serve("cancel", s.authorized))

	page, err := assets.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	checks, err := os.ReadFile(checksFile)
	if err != nil {
		t.Fatal(err)
	}
	worktree.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The console's own policy, so the page is checked under it: no
		// inline style or script, and nothing loaded from another origin.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
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
