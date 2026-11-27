package plugin_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/bustest"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
	"github.com/ryanhelms/agentflow/internal/plugin/plugintest"
	"github.com/ryanhelms/agentflow/internal/policy"
)

const (
	repo  bus.RepoID     = "0123456789abcdef"
	other bus.RepoID     = "fedcba9876543210"
	tree  bus.WorktreeID = "main"
)

type fixture struct {
	t       *testing.T
	h       *plugin.Host
	bus     *bustest.Bus
	src     *plugintest.Source
	layers  *plugintest.Layers
	launch  *plugintest.Launcher
	home    string
	gateDir string
	ran     chan fakeIn

	mu        sync.Mutex
	commands  map[string]command.Command // more commands than notify
	lifecycle []bus.PluginLifecycleChanged
	failed    []bus.PluginDeliveryFailed
	ops       []bus.Event
	delays    []time.Duration
}

type fakeIn struct {
	Signal       string `json:"signal"`
	OperationID  string `json:"operation_id,omitempty"`
	Title        string `json:"title,omitempty"`
	ConfirmToken string `json:"confirm_token,omitempty"`
}

// fakeNotify gates like a write through the Invoker.
func fakeNotify(ran chan fakeIn) command.Spec[fakeIn, map[string]string] {
	return command.Spec[fakeIn, map[string]string]{
		Name: "notify", Policy: command.Policy{Class: policy.Remote, Action: "notify", Gate: command.GateByInvoker, AuditByInvoker: true},
		Token:    func(in *fakeIn) *string { return &in.ConfirmToken },
		Describe: func(in fakeIn) command.Detail { return command.Detail{Subject: "notify " + in.Signal} },
		Plan: func(_ context.Context, _ *app.App, in fakeIn) (command.Plan, error) {
			return command.Plan{Summary: map[string]string{"signal": in.Signal}}, nil
		},
		Run: func(_ context.Context, _ *app.App, in fakeIn) (any, error) {
			ran <- in
			return map[string]string{"sent": in.Signal}, nil
		},
	}
}

func newFixture(t *testing.T, child func() *plugintest.Child) *fixture {
	t.Helper()
	f := &fixture{t: t, bus: bustest.New(bus.Options{}), src: &plugintest.Source{}, layers: &plugintest.Layers{},
		home: t.TempDir(), gateDir: t.TempDir(), ran: make(chan fakeIn, 16)}
	if child == nil {
		child = func() *plugintest.Child { return &plugintest.Child{} }
	}
	f.launch = &plugintest.Launcher{New: child}
	sub, err := f.bus.Subscribe(bus.SubscribeOptions{Name: "test", Buffer: 4096},
		bus.Topic{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: "plugin.**"}, bus.Topic{Repo: repo, Worktree: bus.Any, Types: "op.**"})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for e := range sub.Events() {
			f.mu.Lock()
			switch e.Type {
			case bus.TypePluginLifecycleChanged:
				var p bus.PluginLifecycleChanged
				_ = json.Unmarshal(e.Payload, &p)
				f.lifecycle = append(f.lifecycle, p)
			case bus.TypePluginDeliveryFailed:
				var p bus.PluginDeliveryFailed
				_ = json.Unmarshal(e.Payload, &p)
				f.failed = append(f.failed, p)
			default:
				f.ops = append(f.ops, e)
			}
			f.mu.Unlock()
		}
	}()
	gate, err := policy.NewGate(f.gateDir, config.Autonomy{Local: config.Auto, Remote: config.Auto}, "test")
	if err != nil {
		t.Fatal(err)
	}
	notify := fakeNotify(f.ran)
	f.h = plugin.NewHost(plugin.Options{
		Source: f.src, Layers: f.layers, Bus: f.bus, Home: f.home,
		Served: func() []plugin.Worktree {
			return []plugin.Worktree{{Repo: repo, ID: tree, Root: "/r", Main: true}, {Repo: other, ID: tree, Root: "/o", Main: true}}
		},
		Commands: func(name string) (command.Command, bool) {
			if name == "notify" {
				return notify, true
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			c, ok := f.commands[name]
			return c, ok
		},
		OpenApp: func(_ context.Context, _ string, id string) (*app.App, error) {
			client := plugin.OriginPrefix + id
			g := *gate
			g.Client = client
			return &app.App{Gate: &g, Client: client}, nil
		},
		Launch:  f.launch.Launch,
		Environ: func(string, map[string]string) ([]string, error) { return []string{"PATH=/usr/bin"}, nil },
		After: func(d time.Duration) <-chan time.Time {
			if d >= time.Second { // restart backoff: record it and go on at once
				f.mu.Lock()
				f.delays = append(f.delays, d)
				f.mu.Unlock()
				return time.After(time.Millisecond)
			}
			return time.After(d)
		},
		PingInterval: 20 * time.Millisecond, PingTimeout: 20 * time.Millisecond, DeliverTimeout: 100 * time.Millisecond, InitializeTimeout: 200 * time.Millisecond,
	})
	t.Cleanup(f.h.Stop)
	return f
}

func (f *fixture) install(m manifest.Manifest, enabled bool) {
	f.t.Helper()
	p := manifest.Installed{ID: m.ID, Version: m.Version, Tier: m.Tier, Hash: "sha256:" + strings.Repeat("a", 64), Manifest: m}
	if m.Tier == manifest.TierProcess {
		p.Dir = filepath.Join(f.home, "plugins", m.ID, "aaaaaaaaaaaaaaaa")
	}
	f.src.Set(append(f.installed(), p)...)
	f.layers.SetPersonal(m.ID, plugin.PersonalPlugin{Enabled: enabled})
}

func (f *fixture) installed() []manifest.Installed {
	ps, _ := f.src.Installed(context.Background())
	return ps
}

func (f *fixture) start() {
	f.t.Helper()
	if err := f.h.Start(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) status(id string) plugin.Status {
	for _, st := range f.h.Status() {
		if st.ID == id {
			return st
		}
	}
	return plugin.Status{}
}

func (f *fixture) waitState(id string, state bus.PluginState) plugin.Status {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := f.status(id); st.State == state {
			return st
		}
		time.Sleep(2 * time.Millisecond)
	}
	f.t.Fatalf("%s never reached %s: %+v", id, state, f.status(id))
	return plugin.Status{}
}

func (f *fixture) eventually(what string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		ok := cond()
		f.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	f.t.Fatalf("never: %s", what)
}

func (f *fixture) publish(e bus.Event) bus.Event {
	f.t.Helper()
	out, err := f.bus.Publish(context.Background(), e)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// checkTransitions fails when a published lifecycle change is not in
// plugin.Transitions.
func (f *fixture) checkTransitions() {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.lifecycle {
		if !slices.Contains(plugin.Transitions[p.Previous], p.State) {
			f.t.Errorf("illegal transition %q -> %q", p.Previous, p.State)
		}
	}
}

func ciUpdated(state, previous string) bus.Event {
	return bus.Event{Type: bus.TypeGHCIUpdated, Source: "watch.github", Repo: repo, Worktree: tree,
		Payload: json.RawMessage(`{"state":"` + state + `","previous":"` + previous + `","head_sha":"abc","head_ref":"feat"}`)}
}

func ciNotify() manifest.Manifest {
	return manifest.Manifest{Schema: 1, ID: "ci-notify", Name: "CI", Version: "0.1.0", Tier: manifest.TierDeclarative,
		Permissions: manifest.Permissions{Subscribe: []string{"gh.ci.updated"}, Invoke: []string{"notify"}},
		Extensions: manifest.Extensions{Handler: []manifest.Handler{{Name: "ci-failed", On: []string{"gh.ci.updated"},
			When: []manifest.Condition{{Field: "payload.state", Equals: "failure"}, {Field: "payload.previous", NotEquals: "failure"}},
			Actions: []manifest.Action{{Invoke: &manifest.InvokeAction{Command: "notify", Input: map[string]any{
				"signal": "{{settings.signal}}", "operation_id": "ci-notify-{{event.payload.head_sha}}", "title": "CI failed on {{event.payload.head_ref}}"}}}},
		}}},
		Settings: []manifest.Setting{{Key: "signal", Type: manifest.SettingString, Default: "ci.failed", Label: "Signal", Description: "d"}},
	}
}

func TestDeclarativePluginWritesWaitForAPerson(t *testing.T) {
	f := newFixture(t, nil)
	f.install(ciNotify(), true)
	f.start()
	f.waitState("ci-notify", bus.PluginRunning)

	f.publish(ciUpdated("failure", "failure")) // filtered by when
	cause := f.publish(ciUpdated("failure", "success"))
	f.eventually("op events", func() bool { return len(f.ops) >= 2 })
	select {
	case in := <-f.ran:
		t.Fatalf("auto applied a plugin write without a person: %+v", in)
	case <-time.After(50 * time.Millisecond):
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ops) != 2 || f.ops[0].Type != bus.TypeOpRequested || f.ops[1].Type != bus.TypeOpAwaitingApproval {
		t.Fatalf("op events: %+v", f.ops)
	}
	req := f.ops[0]
	var p bus.OpPayload
	_ = json.Unmarshal(req.Payload, &p)
	if req.Source != "plugin:ci-notify" || p.Client != "plugin:ci-notify" || req.CausationID != cause.ID || req.Repo != repo {
		t.Fatalf("op.requested: %+v %+v", req, p)
	}
	for _, e := range f.ops {
		if strings.Contains(string(e.Payload), "cf_") {
			t.Fatalf("a confirm token reached an event: %s", e.Payload)
		}
	}
	if st := f.status("ci-notify"); st.Invocations != 1 || st.Delivered != 2 {
		t.Fatalf("counters: %+v", st)
	}
}

func TestDisabledOrUnverifiedPluginNeverStarts(t *testing.T) {
	f := newFixture(t, nil)
	m := ciNotify()
	f.install(m, false)
	pm := processManifest("hints")
	f.install(pm, true)
	f.src.SetVerifyErr("hints", manifest.ErrHashMismatch)
	f.start()
	f.waitState("ci-notify", bus.PluginInstalled)
	if st := f.waitState("hints", bus.PluginFailed); st.Code != bus.PluginCodeHashMismatch {
		t.Fatalf("hash mismatch: %+v", st)
	}
	if f.launch.Count() != 0 {
		t.Fatal("a plugin whose hash differs was launched")
	}
	// failed is sticky: a reload with nothing changed leaves it there.
	if err := f.h.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.status("hints").State != bus.PluginFailed || f.launch.Count() != 0 {
		t.Fatal("failed was not sticky")
	}
	// Disabling it, then removing it.
	f.layers.SetPersonal("hints", plugin.PersonalPlugin{})
	_ = f.h.Reload(context.Background())
	f.waitState("hints", bus.PluginStopped)
	f.src.Set()
	_ = f.h.Reload(context.Background())
	f.eventually("removed", func() bool {
		return len(f.lifecycle) > 0 && f.lifecycle[len(f.lifecycle)-1].State == bus.PluginRemoved
	})
	f.checkTransitions()
}

func processManifest(id string) manifest.Manifest {
	return manifest.Manifest{Schema: 1, ID: id, Name: id, Version: "0.1.0", Tier: manifest.TierProcess,
		Exec:        &manifest.Exec{Command: "bin"},
		Permissions: manifest.Permissions{Subscribe: []string{"op.failed", "git.**"}, Publish: []string{string(bus.TypePluginAnnotationAdded)}, Invoke: []string{"notify"}},
		Extensions:  manifest.Extensions{Handler: []manifest.Handler{{Name: "h", On: []string{"op.failed", "git.**"}}}},
	}
}

func TestBackoffDoublesToFiveMinutes(t *testing.T) {
	got := []time.Duration{}
	for n := 1; n <= 12; n++ {
		got = append(got, plugin.BackoffForTest(n))
	}
	if got[0] != time.Second || got[1] != 2*time.Second || got[8] != 256*time.Second || got[9] != 5*time.Minute || got[11] != 5*time.Minute {
		t.Fatalf("backoff: %v", got)
	}
}

func TestDeclarativeAnnotateAndFailedAction(t *testing.T) {
	f := newFixture(t, nil)
	m := manifest.Manifest{Schema: 1, ID: "notes", Name: "Notes", Version: "0.1.0", Tier: manifest.TierDeclarative,
		Permissions: manifest.Permissions{Subscribe: []string{"op.failed"}, Publish: []string{string(bus.TypePluginAnnotationAdded)}},
		Extensions: manifest.Extensions{Handler: []manifest.Handler{
			{Name: "hint", On: []string{"op.failed"}, When: []manifest.Condition{{Field: "payload.code", In: []any{"operation_changed", "blocked"}}},
				Actions: []manifest.Action{{Annotate: &manifest.AnnotateAction{Text: "{{event.payload.code}}: prepare it again", URL: "https://e.com/{{event.payload.operation}}"}}}},
			// Not granted at runtime: its action fails and the event is a dead letter.
			{Name: "bad", On: []string{"op.failed"}, When: []manifest.Condition{{Field: "payload.code", Equals: "invalid_token"}},
				Actions: []manifest.Action{{Invoke: &manifest.InvokeAction{Command: "notify"}}}},
		}},
	}
	f.install(m, true)
	notes, err := f.bus.Subscribe(bus.SubscribeOptions{Name: "notes"}, bus.Topic{Repo: repo, Worktree: bus.Any, Types: bus.Pattern(bus.TypePluginAnnotationAdded)})
	if err != nil {
		t.Fatal(err)
	}
	f.start()
	f.waitState("notes", bus.PluginRunning)
	opID := "op_" + strings.Repeat("5", 32)
	failed := func(code string) bus.Event {
		return f.publish(bus.Event{Type: bus.TypeOpFailed, Source: "cli", Repo: repo, Worktree: tree, CorrelationID: opID,
			Payload: json.RawMessage(`{"operation":"commit","code":"` + code + `"}`)})
	}
	cause := failed("operation_changed")
	e := <-notes.Events()
	var p bus.PluginAnnotationAdded
	_ = json.Unmarshal(e.Payload, &p)
	if p.OperationID != opID || p.Text != "operation_changed: prepare it again" || p.URL != "https://e.com/commit" || e.CausationID != cause.ID || p.Plugin != "notes" {
		t.Fatalf("annotation: %+v %+v", e, p)
	}
	bad := failed("invalid_token")
	f.eventually("action dead letter", func() bool {
		return len(f.failed) == 1 && f.failed[0].EventID == bad.ID && f.failed[0].Code == plugin.ReasonAction
	})
}
