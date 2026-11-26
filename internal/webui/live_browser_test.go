package webui

import (
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the stream client on its own: frame parsing, the snapshot store,
// and the SharedWorker and per-tab streams against a scripted endpoint.
func TestEventStreamBrowser(t *testing.T) {
	streamClientFixture(t, "testdata/event-worker-test.js")
}

// Exercise a tab's life on the stream: attach, the tab= list of the shared
// stream, navigation from the tabs state, acknowledgement and leave.
func TestTabFlowBrowser(t *testing.T) {
	streamClientFixture(t, "testdata/tab-flow-test.js", "tab-navigation.js")
}

// streamClientFixture serves a bare page at /w/first/ with event-worker.js,
// the extra static scripts and the checks file, against a scripted stream.
func streamClientFixture(t *testing.T, checksFile string, scripts ...string) {
	runBrowserFixture(t, "/w/first/", func(mux *http.ServeMux, _ string) {
		checks, err := os.ReadFile(checksFile)
		if err != nil {
			t.Fatal(err)
		}
		routes := map[string][]byte{"/checks.js": checks}
		tags := ""
		for _, name := range append([]string{"event-worker.js"}, scripts...) {
			script, err := assets.ReadFile("static/" + name)
			if err != nil {
				t.Fatal(err)
			}
			routes["/w/first/"+name] = script
			tags += `<script src="` + name + `"></script>`
		}
		var token atomic.Value
		token.Store("good")
		stream := newScriptedStream(func(r *http.Request) bool {
			return r.Header.Get("Authorization") == "Bearer "+token.Load().(string)
		})
		stream.install(mux)
		mux.HandleFunc("GET /w/first/api/events", stream.events)
		mux.HandleFunc("POST /fixture/stream/token", func(w http.ResponseWriter, r *http.Request) {
			token.Store(r.URL.Query().Get("value"))
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("GET /w/first/{$}", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><body>`+tags+`<script src="/checks.js"></script></body></html>`)
		})
		for route, script := range routes {
			mux.HandleFunc("GET "+route, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/javascript")
				_, _ = w.Write(script)
			})
		}
	})
}

// Drive the embedded console's overview from a scripted event stream.
func TestLiveOverviewBrowser(t *testing.T) {
	liveConsoleFixture(t, "testdata/live-overview-test.js")
}

// Drive the activity, approvals and handoff panels from a scripted stream.
func TestLivePanelsBrowser(t *testing.T) {
	liveConsoleFixture(t, "testdata/live-panels-test.js")
}

// liveConsoleFixture serves the embedded console at /w/first/ with a
// scripted event stream and runs the checks file in it.
func liveConsoleFixture(t *testing.T, checksFile string) {
	runBrowserFixture(t, "/w/first/", func(mux *http.ServeMux, origin string) {
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
	})
}
