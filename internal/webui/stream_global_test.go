package webui

import (
	"context"
	"errors"
	"fmt"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/watch"
)

// TestMain keeps every test of this package away from the OS user's real
// ~/.agentflow-ui: a runtime started by a test opens its global store in a
// private temporary registry instead.
func TestMain(m *testing.M) {
	jsonrpctest.RegisterChild("hints", childHints)
	jsonrpctest.RunChild()
	registry, err := os.MkdirTemp("", "agentflow-ui-registry-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	globalRegistryDir = func() (string, error) { return registry, nil }
	code := m.Run()
	_ = os.RemoveAll(registry)
	os.Exit(code)
}

// privateRegistry points globalRegistryDir at a fresh private directory for
// one test and returns it.
func privateRegistry(t *testing.T) string {
	t.Helper()
	registry := filepath.Join(t.TempDir(), "registry")
	old := globalRegistryDir
	globalRegistryDir = func() (string, error) { return registry, nil }
	t.Cleanup(func() { globalRegistryDir = old })
	return registry
}

// The daemon owns the global store: an event published on its bus in
// GlobalRepo is stored in <registry>/<host-hash>.events, and a reader sees
// it next to the repository's events through store.Union.
func TestEventRuntimeStoresGlobalEvents(t *testing.T) {
	registry := privateRegistry(t)
	dir := eventRepo(t)
	ctx := context.Background()
	r, _, err := inspectWorktree(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	oldWatcher, oldExts := newWatcher, runtimeExtensions
	newWatcher = func() (watch.Watcher, error) { return quietWatcher{}, nil }
	runtimeExtensions = nil
	t.Cleanup(func() { newWatcher, runtimeExtensions = oldWatcher, oldExts })

	rt, stop, err := startEventRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()
	if rt.global == nil || rt.global.Repo() != bus.GlobalRepo {
		t.Fatalf("runtime global store = %v", rt.global)
	}
	ev, err := rt.attach(r)
	if err != nil {
		t.Fatal(err)
	}
	if ev.global != rt.global {
		t.Fatal("repository does not see the global store")
	}
	published, err := rt.bus.Publish(ctx, bus.Event{Type: bus.TypeConfigLayerChanged, Source: "test", Repo: bus.GlobalRepo})
	if err != nil {
		t.Fatal(err)
	}
	if published.Seq != 1 {
		t.Fatalf("global event seq = %d, want 1 (stored)", published.Seq)
	}
	if _, err := rt.bus.Publish(ctx, bus.Event{Type: bus.TypeGitRefsChanged, Source: "test", Repo: ev.id}); err != nil {
		t.Fatal(err)
	}

	prefix, err := daemonPrefix(registry)
	if err != nil {
		t.Fatal(err)
	}
	gdir := prefix + globalStoreSuffix
	if !strings.HasSuffix(gdir, ".events") || filepath.Dir(gdir) != registry {
		t.Fatalf("global store dir %s", gdir)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(gdir); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("global store mode = %v, %v", info, err)
		}
	}
	if _, err := store.OpenGlobal(ctx, gdir, store.RoleOwner); !errors.Is(err, store.ErrOwned) {
		t.Fatalf("second owner err = %v", err)
	}
	greader, err := store.OpenGlobal(ctx, gdir, store.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = greader.Close() }()
	rreader, err := store.Open(ctx, r.Common, store.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rreader.Close() }()
	var got []bus.RepoID
	for e, err := range store.Union(rreader, greader).ReadFrom(ctx, bus.Cursor{}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, e.Repo)
	}
	if len(got) != 2 || got[0] != bus.GlobalRepo || got[1] != ev.id {
		t.Fatalf("union read %v", got)
	}

	// Stopping the daemon releases the store; the next daemon resumes it.
	stop()
	stopped = true
	again, err := openDaemonGlobalStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	if head, err := again.Head(ctx); err != nil || head[bus.GlobalRepo] != 1 {
		t.Fatalf("Head after restart = %v, %v", head, err)
	}
}

// A global store directory that other users can read is refused, and the
// runtime starts without a global store rather than use it.
func TestGlobalStoreRefusesSharedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits do not apply on Windows")
	}
	registry := privateRegistry(t)
	prefix, err := daemonPrefix(registry)
	if err != nil {
		t.Fatal(err)
	}
	gdir := prefix + globalStoreSuffix
	if err := os.Mkdir(gdir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(gdir, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := openDaemonGlobalStore(context.Background()); !errors.Is(err, store.ErrNotPrivate) {
		t.Fatalf("shared global store dir err = %v", err)
	}
	oldWatcher, oldExts := newWatcher, runtimeExtensions
	newWatcher = func() (watch.Watcher, error) { return quietWatcher{}, nil }
	runtimeExtensions = nil
	t.Cleanup(func() { newWatcher, runtimeExtensions = oldWatcher, oldExts })
	rt, stop, err := startEventRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if rt.global != nil {
		t.Fatal("runtime opened a shared global store")
	}
	if _, err := os.Stat(filepath.Join(gdir, store.RepoIDFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repo-id written into a shared directory: %v", err)
	}
}
