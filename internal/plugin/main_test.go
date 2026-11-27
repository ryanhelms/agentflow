package plugin_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

func TestMain(m *testing.M) {
	jsonrpctest.RegisterChild("plugin", childPlugin)
	jsonrpctest.RegisterChild("plugin-env", childEnvPlugin)
	jsonrpctest.RegisterChild("eager", childEager)
	jsonrpctest.RunChild()
	os.Exit(m.Run())
}

// childPlugin is the test binary as a process plugin speaking protocol 1.
func childPlugin([]string) int {
	conn := jsonrpc.NewConn(os.Stdin, os.Stdout, jsonrpc.HandlerFunc(func(_ context.Context, r *jsonrpc.Request) (any, error) {
		if r.Method == plugin.MethodInitialize {
			return plugin.InitializeResult{ProtocolVersion: manifest.ProtocolVersion}, nil
		}
		return struct{}{}, nil
	}), jsonrpc.Limits{})
	<-conn.Done()
	return 0
}

// childEnvPlugin is childPlugin that first writes its environment, one
// variable a line, to env.txt in its data directory.
func childEnvPlugin(args []string) int {
	data := os.Getenv(jsonrpc.EnvPluginData)
	if err := os.MkdirAll(data, 0o700); err != nil {
		return 3
	}
	if err := os.WriteFile(filepath.Join(data, "env.txt"), []byte(strings.Join(os.Environ(), "\n")), 0o600); err != nil {
		return 3
	}
	return childPlugin(args)
}

// realHost is a host over the real launcher, running the test binary in
// child mode for every process plugin.
func realHost(t *testing.T, mode string) (*fixture, func() []jsonrpc.Process) {
	f := newFixture(t, nil)
	var (
		pmu   sync.Mutex
		procs []jsonrpc.Process
	)
	launch := func(ctx context.Context, s jsonrpc.LaunchSpec) (jsonrpc.Process, error) {
		child := jsonrpctest.ChildSpec(t, mode)
		s.Path, s.Root, s.Args, s.ShutdownGrace = child.Path, child.Root, child.Args, child.ShutdownGrace
		p, err := jsonrpc.Launch(ctx, s)
		if err == nil {
			pmu.Lock()
			procs = append(procs, p)
			pmu.Unlock()
		}
		return p, err
	}
	f.h.Stop()
	f.h = plugin.NewHost(plugin.Options{Source: f.src, Layers: f.layers, Bus: f.bus, Home: f.home, Launch: launch,
		Served:  func() []plugin.Worktree { return []plugin.Worktree{{Repo: repo, ID: tree, Root: "/r", Main: true}} },
		Environ: plugin.DefaultOptions(plugin.Options{}).Environ, InitializeTimeout: 10 * time.Second,
		After: func(time.Duration) <-chan time.Time { return time.After(10 * time.Millisecond) }})
	t.Cleanup(f.h.Stop)
	return f, func() []jsonrpc.Process {
		pmu.Lock()
		defer pmu.Unlock()
		return append([]jsonrpc.Process(nil), procs...)
	}
}

func (f *fixture) sawBackoff(codes ...string) bool {
	for _, p := range f.lifecycle {
		if p.State == bus.PluginBackoff && slices.Contains(codes, p.Code) {
			return true
		}
	}
	return false
}

// TestRealProcessPluginRestartsAfterAKill runs the host over the real
// launcher with the re-exec child: killing it shows backoff, then running.
func TestRealProcessPluginRestartsAfterAKill(t *testing.T) {
	f, procs := realHost(t, "plugin")
	f.install(processManifest("real"), true)
	f.start()
	f.waitState("real", bus.PluginRunning)
	_ = procs()[0].Kill()
	f.eventually("backoff", func() bool { return f.sawBackoff(bus.PluginCodeCrashed) })
	f.eventually("restarted", func() bool { return len(procs()) >= 2 })
	f.waitState("real", bus.PluginRunning)
	f.checkTransitions()
}

// TestFloodingOrMalformedPluginBlocksOnlyItself: a child that floods stdout
// without a newline, or writes a line that is not JSON, breaks the
// protocol. The host restarts it and keeps serving another plugin.
func TestFloodingOrMalformedPluginBlocksOnlyItself(t *testing.T) {
	for _, mode := range []string{"flood", "garbage"} {
		t.Run(mode, func(t *testing.T) {
			f, procs := realHost(t, mode)
			f.install(processManifest("bad"), true)
			f.install(ciNotify(), true)
			f.start()
			f.waitState("ci-notify", bus.PluginRunning)
			f.eventually("protocol backoff", func() bool {
				return f.sawBackoff(bus.PluginCodeProtocol)
			})
			start := time.Now()
			for i := 0; i < 50; i++ {
				f.publish(ciUpdated("failure", "failure")) // filtered by when: no invoke, no rate limit
			}
			if time.Since(start) > time.Second {
				t.Fatal("a broken plugin held up Publish")
			}
			f.eventually("the other plugin still works", func() bool { return f.status("ci-notify").Delivered >= 50 })
			if len(procs()) == 0 {
				t.Fatal("never launched")
			}
			f.checkTransitions()
		})
	}
}

// TestRealChildSeesOnlyTheScrubbedEnvironment reads the environment a real
// child process got: the plugin variables, and no token or session.
func TestRealChildSeesOnlyTheScrubbedEnvironment(t *testing.T) {
	t.Setenv("GH_TOKEN", "secret-token")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "session")
	f, _ := realHost(t, "plugin-env")
	f.install(processManifest("envreal"), true)
	f.start()
	f.waitState("envreal", bus.PluginRunning)
	raw, err := os.ReadFile(filepath.Join(f.home, manifest.PluginsDir, "envreal", manifest.DataDir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	vars := strings.Split(string(raw), "\n")
	for _, v := range vars {
		if strings.Contains(v, "secret-token") || strings.HasPrefix(v, "GH_TOKEN=") || strings.HasPrefix(v, "CLAUDE_CODE_SESSION_ID=") {
			t.Errorf("the child got %s", v)
		}
	}
	if !slices.Contains(vars, jsonrpc.EnvPluginID+"=envreal") {
		t.Errorf("no %s in %v", jsonrpc.EnvPluginID, vars)
	}
}

func TestDisabledProcessPluginIsNeverLaunched(t *testing.T) {
	f := newFixture(t, nil)
	f.install(processManifest("off"), false)
	f.start()
	f.waitState("off", bus.PluginInstalled)
	if f.launch.Count() != 0 {
		t.Fatal("a disabled plugin was launched")
	}
}
