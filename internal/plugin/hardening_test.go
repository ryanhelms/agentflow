package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// spy is a builtin test plugin. It keeps its Env, panics on an op.failed
// event whose code is "panic", and ends itself when told to.
type spy struct {
	once sync.Once
	done chan struct{}
}

var spies = struct {
	sync.Mutex
	envs []plugin.Env
	last *spy
}{}

func spyManifest() manifest.Manifest {
	return manifest.Manifest{Schema: 1, ID: "spy", Name: "Spy", Version: "0.1.0", Tier: manifest.TierBuiltin,
		Permissions: manifest.Permissions{Subscribe: []string{"op.failed"}, Publish: []string{string(bus.TypePluginAnnotationAdded)}},
		Extensions:  manifest.Extensions{Handler: []manifest.Handler{{Name: "h", On: []string{"op.failed"}}}}}
}

func init() {
	plugin.RegisterBuiltin(plugin.Builtin{Manifest: spyManifest(), New: func() plugin.Instance {
		s := &spy{done: make(chan struct{})}
		spies.Lock()
		spies.last = s
		spies.Unlock()
		return s
	}})
}

func (s *spy) Start(ctx context.Context, env plugin.Env) error {
	if _, err := env.Subscribe(ctx, []string{"op.failed"}); err != nil {
		return err
	}
	spies.Lock()
	spies.envs = append(spies.envs, env)
	spies.Unlock()
	return nil
}

func (s *spy) Deliver(_ context.Context, e bus.Event) error {
	if strings.Contains(string(e.Payload), `"panic"`) {
		panic("spy panics")
	}
	return nil
}

func (s *spy) Settings(context.Context, map[string]any) error { return nil }
func (s *spy) Stop(context.Context) error                     { s.end(); return nil }
func (s *spy) Done() <-chan struct{}                          { return s.done }
func (s *spy) Err() error                                     { return errors.New("spy ended") }
func (s *spy) end()                                           { s.once.Do(func() { close(s.done) }) }

func spyEnvs() []plugin.Env {
	spies.Lock()
	defer spies.Unlock()
	return append([]plugin.Env(nil), spies.envs...)
}

func resetSpies() {
	spies.Lock()
	spies.envs, spies.last = nil, nil
	spies.Unlock()
}

func spyFixture(t *testing.T) *fixture {
	resetSpies()
	f := newFixture(t, nil)
	f.install(spyManifest(), true)
	f.start()
	f.waitState("spy", bus.PluginRunning)
	f.eventually("spy env", func() bool { return len(spyEnvs()) == 1 })
	return f
}

func annotation(text string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"operation_id": "op_" + strings.Repeat("7", 32), "text": text})
	return raw
}

func TestAPanickingPluginDoesNotKillTheDaemon(t *testing.T) {
	f := spyFixture(t)
	ev := f.publish(bus.Event{Type: bus.TypeOpFailed, Source: "cli", Repo: repo, Worktree: tree, CorrelationID: "op_" + strings.Repeat("1", 32),
		Payload: json.RawMessage(`{"operation":"commit","code":"panic"}`)})
	f.eventually("crash dead letter", func() bool {
		return len(f.failed) == 1 && f.failed[0].EventID == ev.ID && f.failed[0].Code == bus.PluginCodeCrashed
	})
}

func TestDeclarativeActionPanicIsThePluginsCrash(t *testing.T) {
	f := newFixture(t, nil)
	f.commands = map[string]command.Command{"comment": command.Spec[fakeIn, any]{
		Name: "comment", Policy: command.Policy{Class: policy.Remote, Action: "comment", Gate: command.GateByInvoker, AuditByInvoker: true},
		Token: func(in *fakeIn) *string { return &in.ConfirmToken },
		Plan:  func(context.Context, *app.App, fakeIn) (command.Plan, error) { panic("command panics") },
		Run:   func(context.Context, *app.App, fakeIn) (any, error) { return nil, nil },
	}}
	m := ciNotify()
	m.ID = "boom"
	m.Permissions.Invoke = []string{"comment"}
	m.Extensions.Handler[0].Actions[0].Invoke.Command = "comment"
	f.install(m, true)
	f.start()
	f.waitState("boom", bus.PluginRunning)
	ev := f.publish(ciUpdated("failure", "success"))
	f.eventually("crash dead letter", func() bool {
		return len(f.failed) == 1 && f.failed[0].EventID == ev.ID && f.failed[0].Code == bus.PluginCodeCrashed
	})
	// The host carries on: the next event is delivered too.
	f.publish(ciUpdated("success", "failure"))
	f.eventually("next delivery", func() bool { return f.status("boom").Delivered >= 1 })
}

func TestAnnotationCauseMustBeInTheTargetRepository(t *testing.T) {
	f := spyFixture(t)
	env := spyEnvs()[0]
	ev := f.publish(bus.Event{Type: bus.TypeOpFailed, Source: "cli", Repo: repo, Worktree: tree, CorrelationID: "op_" + strings.Repeat("2", 32),
		Payload: json.RawMessage(`{"operation":"commit","code":"x"}`)})
	f.eventually("delivered", func() bool { return f.status("spy").Delivered >= 1 })
	ctx := context.Background()
	_, err := env.Publish(ctx, plugin.PublishRequest{Type: bus.TypePluginAnnotationAdded, Payload: annotation("t"), Cause: ev.ID, Repo: other})
	var r *plugin.Refusal
	if !errors.As(err, &r) || r.Code != plugin.RefuseInvalid {
		t.Fatalf("a cause from another repository was accepted: %v", err)
	}
	if _, err := env.Publish(ctx, plugin.PublishRequest{Type: bus.TypePluginAnnotationAdded, Payload: annotation("t"), Cause: ev.ID, Repo: repo}); err != nil {
		t.Fatalf("same repository: %v", err)
	}
}

func TestAnnotationTextLosesControlAndBidiCharacters(t *testing.T) {
	f := spyFixture(t)
	notes, err := f.bus.Subscribe(bus.SubscribeOptions{Name: "notes"}, bus.Topic{Repo: repo, Worktree: bus.Any, Types: bus.Pattern(bus.TypePluginAnnotationAdded)})
	if err != nil {
		t.Fatal(err)
	}
	env := spyEnvs()[0]
	if _, err := env.Publish(context.Background(), plugin.PublishRequest{Type: bus.TypePluginAnnotationAdded,
		Payload: annotation("safe\u202Egnp.exe\u0007 \u2066x\u2069\x1b[31m"), Repo: repo}); err != nil {
		t.Fatal(err)
	}
	e := <-notes.Events()
	var p bus.PluginAnnotationAdded
	_ = json.Unmarshal(e.Payload, &p)
	if p.Text != "safegnp.exe x[31m" {
		t.Fatalf("text %q", p.Text)
	}
	if got := plugin.SafeText("a\u200Fb\u061Cc\x7f\u0085d\nsecond", 100); got != "abcd" {
		t.Fatalf("SafeText %q", got)
	}
}

func TestRateLimitsSurviveARestart(t *testing.T) {
	f := spyFixture(t)
	env := spyEnvs()[0]
	ctx := context.Background()
	for i := 0; i < plugin.PublishBurst; i++ {
		if _, err := env.Publish(ctx, plugin.PublishRequest{Type: bus.TypePluginAnnotationAdded, Payload: annotation("n"), Repo: repo}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	spies.Lock()
	first := spies.last
	spies.Unlock()
	first.end() // the plugin dies; the host restarts it
	f.eventually("restarted", func() bool { return len(spyEnvs()) == 2 })
	f.waitState("spy", bus.PluginRunning)
	_, err := spyEnvs()[1].Publish(ctx, plugin.PublishRequest{Type: bus.TypePluginAnnotationAdded, Payload: annotation("n"), Repo: repo})
	var r *plugin.Refusal
	if !errors.As(err, &r) || r.Code != plugin.RefuseRateLimited {
		t.Fatalf("a restart reset the publish budget: %v", err)
	}
}

func TestDeadLetterFileStaysSmall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deadletter.jsonl")
	d := plugin.OpenDeadLetterForTest(path)
	big := json.RawMessage(`{"blob":"` + strings.Repeat("x", 8000) + `"}`)
	mid := json.RawMessage(`{"blob":"` + strings.Repeat("y", 2000) + `"}`)
	var inode uint64
	rewrites := 0
	for i := 0; i < 2000; i++ {
		p := mid
		if i == 1999 {
			p = big
		}
		d.Dead(context.Background(), bus.Letter{Event: bus.Event{ID: "e", Type: bus.TypeOpFailed, Payload: p}, Consumer: "plugin:spy",
			Reason: strings.Repeat("r", 5000), Time: time.Unix(0, 0)})
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() > 1<<20 {
			t.Fatalf("file reached %d bytes", st.Size())
		}
		if ino := plugin.InodeForTest(st); ino != inode {
			if inode != 0 {
				rewrites++
			}
			inode = ino
		}
	}
	if rewrites > 10 {
		t.Fatalf("%d rewrites for 2000 letters", rewrites)
	}
	ls := d.Letters()
	last := ls[len(ls)-1]
	if string(last.Event.Payload) != `{"dropped_bytes":8011}` || len(last.Reason) > 1024 {
		t.Fatalf("stored letter not capped: %s, reason %d bytes", last.Event.Payload, len(last.Reason))
	}
	// Reopened, it holds the same letters.
	if again := plugin.OpenDeadLetterForTest(path).Letters(); len(again) != len(ls) {
		t.Fatalf("reopened %d letters, kept %d", len(again), len(ls))
	}
}
