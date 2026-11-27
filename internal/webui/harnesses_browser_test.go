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

// harnessesWiring is what app.js needs to feed the page: the screen, the
// "harnesses" state from the stream, the stream's status and the page
// object. Each pair is text in app.js and the text that replaces it.
//
// app.js is not one of this lane's files, so until it has these lines the
// fixture serves a copy with them applied. wireHarnesses leaves an app.js
// that already creates the page as it is.
var harnessesWiring = [][2]string{
	{`plugins: "plugins page", `, `plugins: "plugins page", harnesses: "harnesses page", `},
	{`plugins: "plugins", configuration: "configuration"`, `plugins: "plugins", harnesses: "harnesses", configuration: "configuration"`},
	{"  pluginsPage.tick();\n", "  pluginsPage.tick();\n  harnessesPage.tick();\n"},
	{`    showRepository("plugins",states.find(repositoryState("plugins"))?.state ?? null);` + "\n",
		`    showRepository("plugins",states.find(repositoryState("plugins"))?.state ?? null);` + "\n" +
			`    showRepository("harnesses",states.find(repositoryState("harnesses"))?.state ?? null);` + "\n"},
	{`["activity","approvals","timeline","plugins"]`, `["activity","approvals","timeline","plugins","harnesses"]`},
	{`timeline: null, plugins: null};`, `timeline: null, plugins: null, harnesses: null};`},
	{"const panels = AgentflowPanels.createPanels({", `// The Harnesses page (docs/design/ui/harnesses.md). Its values are the
// repository's "harnesses" state; its requests are probe and check.
const harnessesPage = AgentflowHarnesses.createHarnesses({
  root: document, request, announce, settingsRoute: groupRoute,
  visible: () => !$("console-shell").hidden && pages[destination().screen] === "harnesses"
});
const panels = AgentflowPanels.createPanels({`},
	{`  else if (name === "plugins") pluginsPage.show(liveRepository.plugins,flash);` + "\n",
		`  else if (name === "plugins") pluginsPage.show(liveRepository.plugins,flash);` + "\n" +
			`  else if (name === "harnesses") harnessesPage.show(liveRepository.harnesses,flash);` + "\n"},
	{`present: Boolean(liveRepository.plugins)});` + "\n",
		`present: Boolean(liveRepository.plugins)});` + "\n" +
			`  harnessesPage.status({state: name, offline: name === "offline" ? offlineNotes[liveStatus.reason] || "" : "", ready: liveReady, present: Boolean(liveRepository.harnesses)});` + "\n"},
	{`    renderRepository("plugins");` + "\n", `    renderRepository("plugins");` + "\n" + `    renderRepository("harnesses");` + "\n"},
	{"  liveRepository.plugins = null;\n", "  liveRepository.plugins = null;\n  liveRepository.harnesses = null;\n"},
}

// wireHarnesses returns app.js with harnessesWiring applied. It fails when
// a line it replaces is missing or not unique, so a change to app.js that
// breaks the wiring shows here.
func wireHarnesses(t *testing.T, app string) string {
	t.Helper()
	if strings.Contains(app, "AgentflowHarnesses") {
		return app
	}
	for _, pair := range harnessesWiring {
		if n := strings.Count(app, pair[0]); n != 1 {
			t.Fatalf("app.js has %d places for the Harnesses wiring %q, want 1", n, pair[0])
		}
		app = strings.Replace(app, pair[0], pair[1], 1)
	}
	return app
}

// The wiring applies to the app.js of this tree, or app.js already has it.
func TestHarnessesWiringApplies(t *testing.T) {
	app, err := assets.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	wired := wireHarnesses(t, string(app))
	for _, want := range []string{"AgentflowHarnesses.createHarnesses(", `harnesses: "harnesses"`, "harnessesPage.show(", "harnessesPage.status(", "harnessesPage.tick()"} {
		if !strings.Contains(wired, want) {
			t.Errorf("the wired app.js lacks %q", want)
		}
	}
	page, err := assets.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<script src="harnesses.js" defer></script>`, `id="page-harnesses"`, `data-page="harnesses"`} {
		if !strings.Contains(string(page), want) {
			t.Errorf("index.html lacks %q", want)
		}
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

	read := func(name string) []byte {
		data, err := assets.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	page := string(read("static/index.html"))
	app := wireHarnesses(t, string(read("static/app.js")))
	checks, err := os.ReadFile(checksFile)
	if err != nil {
		t.Fatal(err)
	}
	worktree.HandleFunc("GET /app.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, app)
	})
	worktree.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The console's own policy, so the page is checked under it: no
		// inline style or script, and nothing loaded from another origin.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		_, _ = io.WriteString(w, strings.Replace(page, "</head>", `<script src="/fixture/checks.js" defer></script></head>`, 1))
	})
	mux.HandleFunc("GET /fixture/checks.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write(checks)
	})
	mux.HandleFunc("POST /fixture/ticket", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, mintLaunch(s.instanceKey, time.Now()))
	})
}
