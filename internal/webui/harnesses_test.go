package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/harness/harnesstest"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/watch"
)

// scriptedDetector is a fake harness.Detector whose report a test changes.
type scriptedDetector struct {
	mu    sync.Mutex
	rep   harness.Report
	calls []harness.DetectOptions
}

func (d *scriptedDetector) Detect(_ context.Context, o harness.DetectOptions) (harness.Report, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, o)
	r := d.rep
	r.CheckedAt = time.Now().UTC()
	return r, nil
}

func (d *scriptedDetector) set(hs ...harness.Harness) {
	d.mu.Lock()
	d.rep.Harnesses = hs
	d.mu.Unlock()
}

func testHarness(id string, state harness.State, path string) harness.Harness {
	return harness.Harness{ID: id, Name: id, Kind: "native", State: state, Sources: []string{"registry", "agentflow"},
		Command: id, Args: []string{"--acp"}, Path: path, Probe: harness.Probe{Status: "none", Code: "not_probed"},
		Auth: harness.SignInStatus{Status: "unknown", Code: "no_check"}}
}

// runSource starts a harness source over det, makes it the runtime's and
// waits for its first pass.
func runSource(t *testing.T, det harness.Detector) *watch.HarnessSource {
	t.Helper()
	src := &watch.HarnessSource{Detector: det, Bus: &nullPublisher{}, Join: 10 * time.Millisecond, PollInterval: func() time.Duration { return time.Hour }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = src.Run(ctx) }()
	setHarnesses(harnessesRuntime{source: src, ctx: ctx})
	t.Cleanup(func() { cancel(); <-done; setHarnesses(harnessesRuntime{}) })
	waitUntil(t, func() bool { return src.Passes() == 1 })
	return src
}

type nullPublisher struct{}

func (nullPublisher) Publish(_ context.Context, e bus.Event) (bus.Event, error) { return e, nil }

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func harnessRequest(t *testing.T, s *server, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", s.origin+path, bytes.NewReader([]byte(body)))
	req.Header.Set("Origin", s.origin)
	req.Header.Set("Authorization", "Bearer "+grantToken(s.session, "tab/v1", time.Now().Add(time.Hour)))
	req.Header.Set("X-Agentflow-Request", "console")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	return w
}

func setHarnessClock(t *testing.T, at *time.Time) {
	t.Helper()
	old := harnessNow
	harnessNow = func() time.Time { return *at }
	t.Cleanup(func() { harnessNow = old })
}

func TestHarnessEndpointsKeepTheConsoleChecks(t *testing.T) {
	harnesstest.PATH(t)
	s := testServer(t)
	for _, path := range []string{"/api/harnesses/check", "/api/harnesses/probe"} {
		if w := harnessRequest(t, s, path, "{}", map[string]string{"Origin": ""}); w.Code != 403 {
			t.Errorf("%s without Origin: %d", path, w.Code)
		}
		if w := harnessRequest(t, s, path, "{}", map[string]string{"Origin": "http://evil.invalid"}); w.Code != 403 {
			t.Errorf("%s from another origin: %d", path, w.Code)
		}
		if w := harnessRequest(t, s, path, "{}", map[string]string{"X-Agentflow-Request": ""}); w.Code != 403 {
			t.Errorf("%s without the request header: %d", path, w.Code)
		}
		if w := harnessRequest(t, s, path, "{}", map[string]string{"Authorization": ""}); w.Code != 401 {
			t.Errorf("%s without a session: %d", path, w.Code)
		}
	}
}

func TestHarnessCheck(t *testing.T) {
	harnesstest.PATH(t)
	s := testServer(t)
	// Without a source both bodies are accepted and nothing happens.
	for _, body := range []string{"", "{}"} {
		if w := harnessRequest(t, s, "/api/harnesses/check", body, nil); w.Code != 202 || w.Body.String() != "{}\n" {
			t.Errorf("check %q: %d %q", body, w.Code, w.Body.String())
		}
	}
	if w := harnessRequest(t, s, "/api/harnesses/check", `{"ids":["x"]}`, nil); w.Code != 400 {
		t.Errorf("check with fields: %d", w.Code)
	}
	if w := harnessRequest(t, s, "/api/harnesses/check", `{} {}`, nil); w.Code != 400 {
		t.Errorf("check with two bodies: %d", w.Code)
	}

	det := &scriptedDetector{}
	src := runSource(t, det)
	now := time.Now()
	setHarnessClock(t, &now)
	if w := harnessRequest(t, s, "/api/harnesses/check", "{}", nil); w.Code != 202 || w.Body.String() != "{}\n" {
		t.Fatalf("check: %d %s", w.Code, w.Body)
	}
	waitUntil(t, func() bool { return src.Passes() == 2 })
	now = now.Add(5 * time.Second)
	if w := harnessRequest(t, s, "/api/harnesses/check", "{}", nil); w.Code != 202 {
		t.Fatalf("second check: %d", w.Code)
	}
	time.Sleep(200 * time.Millisecond)
	if n := src.Passes(); n != 2 {
		t.Errorf("a check within 10 s ran a pass (%d)", n)
	}
	now = now.Add(6 * time.Second)
	harnessRequest(t, s, "/api/harnesses/check", "{}", nil)
	waitUntil(t, func() bool { return src.Passes() == 3 })
	// Another console has its own limit.
	other := testServer(t)
	harnessRequest(t, other, "/api/harnesses/check", "{}", nil)
	waitUntil(t, func() bool { return src.Passes() == 4 })
	for _, o := range det.calls {
		if o.Requester != harness.RequesterDaemon {
			t.Errorf("a check ran as %q", o.Requester)
		}
	}
}

type fakeHarnessesIn struct {
	Probe bool     `json:"probe,omitempty"`
	All   bool     `json:"all,omitempty"`
	IDs   []string `json:"ids,omitempty"`
}

// fakeHarnessesSpec registers a stand-in for lane 6.3's ai.harnesses in a
// test registry. It records its input and client and waits for release.
func fakeHarnessesSpec(t *testing.T) (calls chan fakeHarnessesCall, release chan struct{}) {
	t.Helper()
	reg := command.NewRegistry()
	calls, release = make(chan fakeHarnessesCall, 4), make(chan struct{})
	err := reg.Add(command.Spec[fakeHarnessesIn, any]{Name: HarnessesCommand, Policy: command.Policy{Class: policy.Read},
		Run: func(_ context.Context, a *app.App, in fakeHarnessesIn) (any, error) {
			calls <- fakeHarnessesCall{in: in, client: a.Client}
			<-release
			return harness.Report{}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	old := harnessCommands
	harnessCommands = func() *command.Registry { return reg }
	t.Cleanup(func() { harnessCommands = old })
	return calls, release
}

type fakeHarnessesCall struct {
	in     fakeHarnessesIn
	client string
}

func TestHarnessProbe(t *testing.T) {
	harnesstest.PATH(t)
	s := testServer(t)
	if w := harnessRequest(t, s, "/api/harnesses/probe", "{}", nil); w.Code != 503 || !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("without a source: %d %s", w.Code, w.Body)
	}
	det := &scriptedDetector{}
	det.set(testHarness("gemini", harness.StateNative, "/x/gemini"), testHarness("goose", harness.StateNative, "/x/goose"),
		harness.Harness{ID: "kimi", State: harness.StateNotInstalled})
	src := runSource(t, det)
	if w := harnessRequest(t, s, "/api/harnesses/probe", "{}", nil); w.Code != 503 {
		t.Errorf("without the ai.harnesses command: %d %s", w.Code, w.Body)
	}
	calls, release := fakeHarnessesSpec(t)
	now := time.Now()
	setHarnessClock(t, &now)

	for _, body := range []string{`{"ids":["Bad"]}`, `{"ids":["a","a"]}`, `{"ids":["../x"]}`, `{"probe":false}`, `[]`,
		`{"ids":[` + strings.Repeat(`"a",`, 64) + `"b"]}`} {
		if w := harnessRequest(t, s, "/api/harnesses/probe", body, nil); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}

	w := harnessRequest(t, s, "/api/harnesses/probe", "{}", nil)
	if w.Code != 202 || w.Body.String() != "{}\n" {
		t.Fatalf("probe: %d %q", w.Code, w.Body.String())
	}
	var call fakeHarnessesCall
	select {
	case call = <-calls:
	case <-time.After(10 * time.Second):
		t.Fatal("the command did not run")
	}
	if !call.in.Probe || call.in.All || len(call.in.IDs) != 0 || call.client != harnessesClient {
		t.Errorf("call %+v", call)
	}
	if got := strings.Join(probingIDs(), ","); got != "gemini,goose" {
		t.Errorf("probing %q", got)
	}
	st := harnessesStateOf(currentHarnesses(), probingIDs(), "")
	if strings.Join(st.Probing, ",") != "gemini,goose" {
		t.Errorf("state probing %v", st.Probing)
	}
	passes := src.Passes()
	close(release)
	waitUntil(t, func() bool { return len(probingIDs()) == 0 && src.Passes() > passes })

	now = now.Add(30 * time.Second)
	if w := harnessRequest(t, s, "/api/harnesses/probe", `{"ids":["gemini"]}`, nil); w.Code != 429 || !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("a second probe within a minute: %d %s", w.Code, w.Body)
	}
	now = now.Add(31 * time.Second)
	if w := harnessRequest(t, s, "/api/harnesses/probe", `{"ids":["gemini"]}`, nil); w.Code != 202 {
		t.Fatalf("probe after a minute: %d %s", w.Code, w.Body)
	}
	call = <-calls
	if !call.in.Probe || strings.Join(call.in.IDs, ",") != "gemini" {
		t.Errorf("call %+v", call)
	}
}

func TestHarnessesStateWithoutDetector(t *testing.T) {
	st := harnessesStateOf(harnessesRuntime{}, []string{"gemini"}, "/home/me")
	b, _ := json.Marshal(st)
	want := `{"detector":false,"registry":{"commit":"","committed_at":"0001-01-01T00:00:00Z","agents":0},"auto_probe":false,"harnesses":[],"checked_at":"0001-01-01T00:00:00Z"}`
	if string(b) != want {
		t.Errorf("state\n got %s\nwant %s", b, want)
	}
}

func TestHarnessesStateShortensPathsAndDropsNotInstalled(t *testing.T) {
	det := &scriptedDetector{}
	gem := testHarness("gemini", harness.StateNative, "/home/me/.local/bin/gemini")
	ad := testHarness("claude-acp", harness.StateAdapterAvailable, "")
	ad.Base = &harness.Base{Name: "Claude Code", Command: "claude", Path: "/home/me/bin/claude"}
	det.set(gem, ad, harness.Harness{ID: "kimi", State: harness.StateNotInstalled}, testHarness("other", harness.StateNative, "/opt/homebrew/bin/other"))
	det.rep.SkippedPath = []harness.SkippedDir{{Dir: "/home/me/project/bin", Reason: harness.SkipInsideRepository}}
	runSource(t, det)
	st := harnessesStateOf(currentHarnesses(), []string{"gemini", "kimi", "missing"}, "/home/me")
	if !st.Detector || len(st.Harnesses) != 3 {
		t.Fatalf("state %+v", st)
	}
	if st.Harnesses[0].Path != "~/.local/bin/gemini" || st.Harnesses[1].Base.Path != "~/bin/claude" || st.Harnesses[2].Path != "/opt/homebrew/bin/other" {
		t.Errorf("paths %+v", st.Harnesses)
	}
	if st.SkippedPath[0].Dir != "~/project/bin" {
		t.Errorf("skipped %+v", st.SkippedPath)
	}
	if strings.Join(st.Probing, ",") != "gemini" {
		t.Errorf("probing %v", st.Probing)
	}
	// The source's own report keeps its paths.
	if rep, _ := currentHarnesses().source.Last(); rep.Harnesses[0].Path != "/home/me/.local/bin/gemini" {
		t.Error("the state changed the source's report")
	}
}

// TestDaemonHarnessSource runs the daemon runtime with a fake Detector:
// the start pass publishes harness.detected in global, the harnesses
// projection shows it in the repository's frames, and a personal
// configuration change runs a pass that publishes what changed.
func TestDaemonHarnessSource(t *testing.T) {
	harnesstest.PATH(t)
	det := &scriptedDetector{}
	old := newHarnessOptions
	t.Cleanup(func() { newHarnessOptions = old })
	var u *userRuntime
	newHarnessOptions = func(func() []string) HarnessOptions {
		return HarnessOptions{Detector: det, AutoProbe: func() bool { return false }}
	}
	u = startUserRuntime(t, storetest.New(bus.GlobalRepo))
	src := currentHarnesses().source
	if src == nil {
		t.Fatal("no harness source started")
	}
	// The start pass may run before the test subscribes; it found nothing.
	waitUntil(t, func() bool { return src.Passes() >= 1 })
	l, stop := u.repo.projector.listen()
	defer stop()

	// The report's path is under the HOME the user runtime set.
	gem := testHarness("gemini", harness.StateNative, u.userHome+"/.local/bin/gemini")
	det.set(gem)
	writePersonalConfig(t, u.home, "# one\n")
	ev := nextGlobal(t, u, bus.TypeHarnessDetected)
	var p bus.HarnessChanged
	_ = json.Unmarshal(ev.Payload, &p)
	if p.Harness != "gemini" || p.Trigger != watch.HarnessTriggerConfig || ev.Source != watch.HarnessEventSource {
		t.Errorf("detected %+v", p)
	}

	gem.State, gem.Code = harness.StateNeedsAuth, harness.CodeSignedOut
	det.set(gem)
	writePersonalConfig(t, u.home, "# two\n")
	ev = nextGlobal(t, u, bus.TypeHarnessChanged)
	_ = json.Unmarshal(ev.Payload, &p)
	if p.State != "needs_auth" || p.Previous != "native" || p.Trigger != watch.HarnessTriggerConfig {
		t.Errorf("changed %+v", p)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case up := <-l.ch:
			for _, s := range up.states {
				var st HarnessesState
				if s.Projection != ProjectionHarnesses || json.Unmarshal(s.State, &st) != nil {
					continue
				}
				if st.Detector && len(st.Harnesses) == 1 && st.Harnesses[0].State == harness.StateNeedsAuth {
					if st.Harnesses[0].Path != "~/.local/bin/gemini" {
						t.Errorf("path %q", st.Harnesses[0].Path)
					}
					if s.Repo != u.repo.id || s.Worktree != "" {
						t.Errorf("key %s/%s", s.Repo, s.Worktree)
					}
					for _, c := range det.calls {
						if c.Probe || c.Requester != harness.RequesterDaemon {
							t.Errorf("a pass with auto_probe off: %+v", c)
						}
					}
					return
				}
			}
		case <-deadline:
			t.Fatal("the harnesses projection never showed the change")
		}
	}
}

func nextGlobal(t *testing.T, u *userRuntime, typ bus.Type) bus.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-u.global.Events():
			if e.Type == typ {
				if e.Repo != bus.GlobalRepo {
					t.Errorf("%s in %s", e.Type, e.Repo)
				}
				return e
			}
		case <-deadline:
			t.Fatalf("no %s", typ)
		}
	}
}

func TestDaemonWithoutHarnessDetector(t *testing.T) {
	// The default in a test binary: no Detector, so no source runs and the
	// state says so.
	u := startUserRuntime(t, storetest.New(bus.GlobalRepo))
	if currentHarnesses().source != nil {
		t.Fatal("a harness source started in a test binary")
	}
	p := &harnessesProjection{repo: u.repo, runtime: currentHarnesses, probing: probingIDs}
	raw, err := p.State(context.Background(), ProjectionKey{Repo: u.repo.id})
	if err != nil || !strings.HasPrefix(string(raw), `{"detector":false,`) {
		t.Errorf("state %s %v", raw, err)
	}
	if keys := p.Keys(bus.Event{Type: bus.TypeHarnessChanged, Repo: bus.GlobalRepo}); len(keys) != 1 {
		t.Errorf("keys %v", keys)
	}
	if keys := p.Keys(bus.Event{Type: bus.TypeHarnessChanged, Repo: u.repo.id}); len(keys) != 0 {
		t.Errorf("keys for a repository event %v", keys)
	}
}

func writePersonalConfig(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
