package webui

import (
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Drive the Plugins page and the timeline's plugin annotations from a
// scripted stream: cards, states and countdowns, live changes, the Settings
// link, the commands to copy, hostile plugin text and every page state.
//
// The plugins projection (lane 5.2) is replaced here by PluginsState frames
// the checks file writes, with the shapes of projection_plugins_state.go.
func TestPluginsBrowser(t *testing.T) {
	runBrowserFixture(t, "/w/first/", func(mux *http.ServeMux, origin string) {
		pluginsConsoleFixture(t, mux, origin, "testdata/plugins/plugins-test.js")
	})
}

// AGENTFLOW_PLUGINS_DEMO is an address such as 127.0.0.1:41734. With it
// set, this test serves the console with scripted plugin states
// (testdata/plugins/demo.js) until the test is stopped, for a design review
// in a browser:
//
//	AGENTFLOW_PLUGINS_DEMO=127.0.0.1:41734 go test ./internal/webui -run TestPluginsDemo -timeout 0
func TestPluginsDemo(t *testing.T) {
	addr := os.Getenv("AGENTFLOW_PLUGINS_DEMO")
	if addr == "" {
		t.Skip("set AGENTFLOW_PLUGINS_DEMO to an address to serve the Plugins page fixture")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	pluginsConsoleFixture(t, mux, "http://"+listener.Addr().String(), "testdata/plugins/demo.js")
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-t.Context().Done(); _ = srv.Close() }()
	t.Logf("serving the Plugins page fixture at http://%s/w/first/", listener.Addr())
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

// pluginsConsoleFixture serves the embedded console at /w/first/ with a
// scripted event stream and runs the checks file in it.
func pluginsConsoleFixture(t *testing.T, mux *http.ServeMux, origin, checksFile string) {
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
