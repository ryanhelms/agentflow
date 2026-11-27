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

// Drive the Harnesses page from a scripted stream and fake endpoints: the
// groups and cards, every detection and sign-in state, the fix and sign-in
// commands, the probe and check requests, hostile state text and every
// page state.
//
// The harnesses projection and the two endpoints (lane 6.2) are replaced
// here: the checks file writes HarnessesState frames with the shapes of the
// ADR ("Phase 6 harnesses projection"), and harnessesEndpoints answers the
// requests. No test starts a harness.
func TestHarnessesBrowser(t *testing.T) {
	runBrowserFixture(t, "/w/first/", func(mux *http.ServeMux, origin string) {
		harnessesConsoleFixture(t, mux, origin, "testdata/harnesses/harnesses-test.js")
	})
}

// AGENTFLOW_HARNESSES_DEMO is an address such as 127.0.0.1:41735. With it
// set, this test serves the console with scripted harness states
// (testdata/harnesses/demo.js) until the test is stopped, for a design
// review in a browser:
//
//	AGENTFLOW_HARNESSES_DEMO=127.0.0.1:41735 go test ./internal/webui -run TestHarnessesDemo -timeout 0
func TestHarnessesDemo(t *testing.T) {
	addr := os.Getenv("AGENTFLOW_HARNESSES_DEMO")
	if addr == "" {
		t.Skip("set AGENTFLOW_HARNESSES_DEMO to an address to serve the Harnesses page fixture")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	harnessesConsoleFixture(t, mux, "http://"+listener.Addr().String(), "testdata/harnesses/demo.js")
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-t.Context().Done(); _ = srv.Close() }()
	t.Logf("serving the Harnesses page fixture at http://%s/w/first/", listener.Addr())
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

// The Harnesses page is a console screen: agentflow ui --screen harnesses
// and the reconnect screen's command reach it.
func TestHarnessesDestination(t *testing.T) {
	dest, err := (Destination{Screen: "harnesses"}).Normalize("overview")
	if err != nil {
		t.Fatal(err)
	}
	if got := dest.command(); got != "agentflow ui --screen harnesses" {
		t.Fatalf("command = %q", got)
	}
	if got := dest.Fragment(); got != "screen=harnesses" {
		t.Fatalf("fragment = %q", got)
	}
	if _, err := (Destination{Screen: "harnesses", Role: "secops"}).Normalize("overview"); err == nil {
		t.Fatal("a role was accepted on the harnesses screen")
	}
}

// harnessesEndpoints stands in for lane 6.2's POST /api/harnesses/probe and
// /api/harnesses/check. It records each request and answers 202 {}, or the
// status a test asked for. Neither answer carries harness data.
type harnessesEndpoints struct {
	mu       sync.Mutex
	requests []map[string]string
	status   map[string]int
}

func (f *harnessesEndpoints) serve(route string, authorized func(*http.Request) bool) http.HandlerFunc {
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
			problem(w, status, "This console probed less than a minute ago.")
		case status != 0:
			problem(w, status, "fixture refused the request")
		default:
			reply(w, http.StatusAccepted, map[string]string{})
		}
	}
}

// install adds the routes the test page reads the fake endpoints with:
//
//	GET  /fixture/harnesses/requests the probe and check requests seen so far
//	POST /fixture/harnesses/status   answer ?route= (probe or check) with ?code= (0 accepts)
func (f *harnessesEndpoints) install(mux *http.ServeMux) {
	mux.HandleFunc("GET /fixture/harnesses/requests", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.requests)
	})
	mux.HandleFunc("POST /fixture/harnesses/status", func(w http.ResponseWriter, r *http.Request) {
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

// harnessesConsoleFixture serves the embedded console at /w/first/ with a
// scripted event stream and the fake harness endpoints, and runs the checks
// file in it.
func harnessesConsoleFixture(t *testing.T, mux *http.ServeMux, origin, checksFile string) {
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
	endpoints := &harnessesEndpoints{requests: []map[string]string{}, status: map[string]int{}}
	endpoints.install(mux)
	worktree.HandleFunc("POST /api/harnesses/probe", endpoints.serve("probe", s.authorized))
	worktree.HandleFunc("POST /api/harnesses/check", endpoints.serve("check", s.authorized))

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
