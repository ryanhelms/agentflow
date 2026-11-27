package webui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

// The "serve-child" child mode is `agentflow ui --serve-child` for the
// tests: it serves from the registry named in its first argument, which
// must be a private folder under the temporary directory, never the
// account's real ~/.agentflow-ui.
func init() { jsonrpctest.RegisterChild("serve-child", serveChildForTest) }

func serveChildForTest(args []string) int {
	if len(args) != 5 {
		fmt.Fprintln(os.Stderr, "serve-child: want REGISTRY SCREEN ROLE MODE PORT")
		return 2
	}
	registry := args[0]
	tmp, _ := filepath.EvalSymlinks(os.TempDir())
	real, _ := filepath.EvalSymlinks(filepath.Dir(registry))
	if !filepath.IsAbs(registry) || tmp == "" || real == "" || !strings.HasPrefix(real+string(filepath.Separator), tmp+string(filepath.Separator)) {
		fmt.Fprintln(os.Stderr, "serve-child: the registry must be under the temporary directory")
		return 2
	}
	globalRegistryDir = func() (string, error) { return registry, nil }
	port, err := strconv.Atoi(args[4])
	if err != nil {
		return 2
	}
	if err := ServeChild(context.Background(), Destination{Screen: args[1], Role: args[2]}, args[3], port, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// childConsole points consoleChild at the serve-child mode with registry
// and stops the daemon it starts when the test ends.
func childConsole(t *testing.T, registry string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var started []*exec.Cmd
	old := consoleChild
	consoleChild = func(_ string, args []string) (*exec.Cmd, error) {
		if len(args) != 6 || args[0] != "ui" || args[1] != "--serve-child" {
			return nil, fmt.Errorf("unexpected console child %q", args)
		}
		cmd := exec.Command(exe, append([]string{jsonrpctest.ChildArg, "serve-child", registry}, args[2:]...)...)
		mu.Lock()
		started = append(started, cmd)
		mu.Unlock()
		return cmd, nil
	}
	t.Cleanup(func() {
		consoleChild = old
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, _ = daemonCommand(ctx, "stop")
		mu.Lock()
		defer mu.Unlock()
		for _, cmd := range started {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	})
}

// With no console running, the console's own start (a serve-child daemon,
// then the starter's control request) gives the starter its link at once,
// in every mode: auto (the Claude Code SessionStart auto-open) and open
// used to get no link or wait for a tab that was never coming.
func TestConsoleStartGivesTheStarterItsLink(t *testing.T) {
	for _, mode := range []string{"link", "open", "auto"} {
		t.Run(mode, func(t *testing.T) {
			registry := privateRegistry(t)
			childConsole(t, registry)
			dir := testServer(t).repo.Dir
			start := time.Now()
			ready, err := prepareConsole(t.Context(), dir, Destination{Screen: "overview"}, mode, 0)
			took := time.Since(start)
			if err != nil {
				t.Fatalf("prepareConsole(%s) = %v", mode, err)
			}
			if ready.URL == "" || !strings.Contains(ready.URL, "#launch=") {
				t.Errorf("mode %s: the starter got no link (%+v)", mode, ready)
			}
			if ready.Reused {
				t.Errorf("mode %s: a fresh console reported reused", mode)
			}
			if took > 4*time.Second {
				t.Errorf("mode %s: the link took %s (waited for a tab)", mode, took)
			}
		})
	}
}

// The same, in process: a fresh daemon started as the serve-child, then
// the starter's control request with the same operation.
func TestServeChildLeavesTheLinkToTheStarter(t *testing.T) {
	for _, mode := range []string{"link", "open", "auto"} {
		t.Run(mode, func(t *testing.T) {
			registry := filepath.Join(t.TempDir(), "registry")
			root := testServer(t).repo.Dir
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			got := make(chan string, 1)
			done := make(chan error, 1)
			op := registration(root, mode, "overview")
			go func() {
				done <- serveRegisteredAs(ctx, registry, -1, op, func(a string, _ bool) { got <- a }, true)
			}()
			select {
			case a := <-got:
				if a != "" {
					t.Errorf("the serve-child got a link")
				}
			case err := <-done:
				t.Fatal(err)
			case <-time.After(10 * time.Second):
				t.Fatal("the serve-child did not report ready")
			}
			start := time.Now()
			address, found, err := findRegistered(t.Context(), registry, op, 0)
			if err != nil || !found || address == "" {
				t.Errorf("mode %s: the starter got no link (found=%v err=%v)", mode, found, err)
			}
			if took := time.Since(start); took > 4*time.Second {
				t.Errorf("mode %s: the link took %s", mode, took)
			}
			cancel()
			<-done
		})
	}
}
