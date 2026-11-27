package plugin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/plugin/plugintest"
)

func TestCrashLoopBacksOffThenFails(t *testing.T) {
	f := newFixture(t, func() *plugintest.Child {
		return &plugintest.Child{Handle: func(c *plugintest.Child, r *jsonrpc.Request) (any, error) {
			if r.Method == plugin.MethodInitialize {
				go c.Proc.Exit(1) // dies right after starting
			}
			return plugintest.Serve(c, r)
		}}
	})
	f.install(processManifest("crashy"), true)
	f.start()
	if st := f.waitState("crashy", bus.PluginFailed); st.Code != bus.PluginCodeCrashLoop {
		t.Fatalf("crash loop: %+v", st)
	}
	f.mu.Lock()
	delays := append([]time.Duration(nil), f.delays...)
	var backoffs int
	for _, p := range f.lifecycle {
		if p.State == bus.PluginBackoff {
			backoffs++
			if p.RetryAt == nil || p.Code != bus.PluginCodeExited {
				t.Errorf("backoff payload: %+v", p)
			}
		}
	}
	f.mu.Unlock()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if !slices.Equal(delays, want) || backoffs != 4 || f.launch.Count() != 5 {
		t.Fatalf("delays %v backoffs %d launches %d", delays, backoffs, f.launch.Count())
	}
	f.checkTransitions()
}

func TestProtocolErrorsRestartOrFail(t *testing.T) {
	f := newFixture(t, func() *plugintest.Child {
		return &plugintest.Child{Subscribe: []string{"op.failed"}, Handle: func(c *plugintest.Child, r *jsonrpc.Request) (any, error) {
			if r.Method == plugin.MethodEventDeliver {
				go c.Proc.Break() // a malformed frame
				time.Sleep(time.Second)
			}
			return plugintest.Serve(c, r)
		}}
	})
	f.install(processManifest("broken"), true)
	f.start()
	f.waitState("broken", bus.PluginRunning)
	f.eventually("subscribed", func() bool { return len(f.status("broken").Subscriptions) == 1 })
	e := f.publish(bus.Event{Type: bus.TypeOpFailed, Source: "cli", Repo: repo, Worktree: tree, CorrelationID: "op_" + strings.Repeat("1", 32),
		Payload: json.RawMessage(`{"operation":"commit"}`)})
	f.eventually("protocol backoff", func() bool {
		for _, p := range f.lifecycle {
			if p.State == bus.PluginBackoff && p.Code == bus.PluginCodeProtocol {
				return true
			}
		}
		return false
	})
	f.eventually("dead letter", func() bool { return len(f.failed) == 1 && f.failed[0].EventID == e.ID })
	if l := f.h.Letters(); len(l) != 1 || l[0].Reason != bus.PluginCodeProtocol || l[0].Consumer != "plugin:broken" {
		t.Fatalf("letters: %+v", l)
	}
	raw, err := os.ReadFile(filepath.Join(f.home, "plugins", "deadletter.jsonl"))
	if err != nil || !strings.Contains(string(raw), e.ID) {
		t.Fatalf("persistent letter: %v %s", err, raw)
	}
	if st, _ := os.Stat(filepath.Join(f.home, "plugins", "deadletter.jsonl")); st.Mode().Perm() != 0o600 {
		t.Fatalf("dead letter mode %v", st.Mode())
	}

	g := newFixture(t, func() *plugintest.Child {
		return &plugintest.Child{Handle: func(c *plugintest.Child, r *jsonrpc.Request) (any, error) {
			if r.Method == plugin.MethodInitialize {
				return plugin.InitializeResult{ProtocolVersion: 99}, nil
			}
			return plugintest.Serve(c, r)
		}}
	})
	g.install(processManifest("future"), true)
	g.start()
	if st := g.waitState("future", bus.PluginFailed); st.Code != bus.PluginCodeProtocol {
		t.Fatalf("wrong version: %+v", st)
	}
}

func TestHangingPluginBlocksOnlyItself(t *testing.T) {
	var mu sync.Mutex
	got := map[string][]bus.Type{}
	f := newFixture(t, func() *plugintest.Child {
		return &plugintest.Child{Subscribe: []string{"git.**"}, Handle: func(c *plugintest.Child, r *jsonrpc.Request) (any, error) {
			if r.Method == plugin.MethodEventDeliver {
				id := c.Proc.Spec.Args[0]
				var p plugin.EventDeliverParams
				_ = json.Unmarshal(r.Params, &p)
				mu.Lock()
				got[id] = append(got[id], p.Event.Type)
				mu.Unlock()
				if id == "hang" {
					time.Sleep(time.Hour)
				}
			}
			return plugintest.Serve(c, r)
		}}
	})
	hang, ok := processManifest("hang"), processManifest("fine")
	hang.Exec.Args, ok.Exec.Args = []string{"hang"}, []string{"fine"}
	f.install(hang, true)
	f.install(ok, true)
	f.start()
	f.waitState("hang", bus.PluginRunning)
	f.waitState("fine", bus.PluginRunning)
	f.eventually("subscribed", func() bool {
		return len(f.status("hang").Subscriptions) == 1 && len(f.status("fine").Subscriptions) == 1
	})
	start := time.Now()
	for i := 0; i < 400; i++ { // more than the subscription buffer
		f.publish(bus.Event{Type: bus.TypeGitHeadChanged, Source: "watch.git", Repo: repo, Worktree: tree, Payload: json.RawMessage(`{}`)})
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("a hanging plugin held up Publish")
	}
	f.eventually("fine got every event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return uint64(len(got["fine"]))+f.status("fine").Dropped >= 400
	})
	f.eventually("timeout letters", func() bool { return len(f.failed) > 0 && f.failed[0].Code == bus.PluginCodeTimeout })
}

func TestMissedPingsDegradeThenRestart(t *testing.T) {
	var mu sync.Mutex
	deaf := true
	f := newFixture(t, func() *plugintest.Child {
		return &plugintest.Child{Handle: func(c *plugintest.Child, r *jsonrpc.Request) (any, error) {
			mu.Lock()
			d := deaf
			mu.Unlock()
			if r.Method == plugin.MethodPing && d {
				time.Sleep(time.Hour)
			}
			return plugintest.Serve(c, r)
		}}
	})
	f.install(processManifest("deaf"), true)
	f.start()
	f.waitState("deaf", bus.PluginDegraded)
	f.eventually("restart on missed pings", func() bool {
		for _, p := range f.lifecycle {
			if p.State == bus.PluginBackoff && p.Code == bus.PluginCodePing {
				return true
			}
		}
		return false
	})
	mu.Lock()
	deaf = false
	mu.Unlock()
	f.waitState("deaf", bus.PluginRunning)
	f.checkTransitions()
}

func TestProcessPluginGetsTheScrubbedEnvironment(t *testing.T) {
	t.Setenv("GH_TOKEN", "secret-token")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "session")
	f := newFixture(t, nil)
	prod := plugin.DefaultOptions(plugin.Options{})
	if prod.Launch == nil || prod.Environ == nil {
		t.Fatal("the production launcher is not wired")
	}
	data := filepath.Join(f.home, "plugins", "x", "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	vars, err := prod.Environ(data, map[string]string{jsonrpc.EnvPluginID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(vars, "\n")
	if strings.Contains(joined, "secret-token") || strings.Contains(joined, "CLAUDE_CODE_SESSION_ID") || !strings.Contains(joined, "HOME="+data) {
		t.Fatalf("environment: %v", vars)
	}
	// The host launches only the executable inside the installed copy.
	f.install(processManifest("env"), true)
	f.start()
	f.waitState("env", bus.PluginRunning)
	spec := f.launch.Last().Proc.Spec
	if spec.Path != filepath.Join(spec.Root, "bin") || spec.Dir != filepath.Join(f.home, "plugins", "env", "data") {
		t.Fatalf("launch spec: %+v", spec)
	}
}
