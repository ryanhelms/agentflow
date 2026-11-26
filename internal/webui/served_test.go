package webui

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/gitx"
)

func TestServedReposListsEachClonesCommonDir(t *testing.T) {
	ctx := context.Background()
	registry := filepath.Join(t.TempDir(), ".agentflow-ui")
	prefix, err := daemonPrefix(registry)
	if err != nil {
		t.Fatal(err)
	}
	// The read path must name the file the daemon writes.
	if index, err := servedIndex(registry); err != nil || index != prefix+".repos.json" {
		t.Fatalf("index = %q, %v; the daemon uses %q", index, err, prefix+".repos.json")
	}
	if got, err := servedRepos(ctx, registry); err != nil || len(got) != 0 {
		t.Fatalf("no index: %v, %v", got, err)
	}

	a, b := filepath.Join(t.TempDir(), "a", ".git"), filepath.Join(t.TempDir(), "b", ".git")
	entries := []registeredWorktree{
		{CloneID: "1", ID: "main", Root: filepath.Dir(b), Common: b, GitDir: b},
		{CloneID: "2", ID: "main", Root: filepath.Dir(a), Common: a, GitDir: a},
		{CloneID: "2", ID: "task-12345678", Root: "/elsewhere/task", Common: a, GitDir: filepath.Join(a, "worktrees", "task")},
	}
	if err := writePrivateJSON(prefix+".repos.json", entries); err != nil {
		t.Fatal(err)
	}
	got, err := servedRepos(ctx, registry)
	if want := []string{a, b}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("served = %v, %v, want %v (sorted, one per clone)", got, err, want)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := servedRepos(cancelled, registry); err == nil {
		t.Fatal("a cancelled context was ignored")
	}

	// An entry that is not an absolute path is a damaged registry, not a repository.
	if err := writePrivateJSON(prefix+".repos.json", []registeredWorktree{{CloneID: "3", ID: "main", Common: "relative/.git"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := servedRepos(ctx, registry); err == nil {
		t.Fatalf("a relative path was accepted: %v", got)
	}
	if err := os.WriteFile(prefix+".repos.json", []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := servedRepos(ctx, registry); err == nil {
		t.Fatal("a damaged index was accepted")
	}
}

func TestServedReposNeverCreatesTheRegistry(t *testing.T) {
	registry := filepath.Join(t.TempDir(), ".agentflow-ui")
	got, err := servedRepos(context.Background(), registry)
	if err != nil || len(got) != 0 {
		t.Fatalf("no registry: %v, %v", got, err)
	}
	if _, err := os.Lstat(registry); !os.IsNotExist(err) {
		t.Fatalf("a read created the registry: %v", err)
	}
}

func TestServedReposRefusesAnIndexOthersCanWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not describe access on Windows")
	}
	registry := filepath.Join(t.TempDir(), ".agentflow-ui")
	prefix, err := daemonPrefix(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateJSON(prefix+".repos.json", []registeredWorktree{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(prefix+".repos.json", 0o666); err != nil {
		t.Fatal(err)
	}
	if got, err := servedRepos(context.Background(), registry); err == nil {
		t.Fatalf("a group-writable index was read: %v", got)
	}
}

// useRegistry points the global store lookup at a temporary registry.
func useRegistry(t *testing.T, registry string) {
	t.Helper()
	old := globalRegistryDir
	globalRegistryDir = func() (string, error) { return registry, nil }
	t.Cleanup(func() { globalRegistryDir = old })
}

func TestGlobalStoreDirOnlyReports(t *testing.T) {
	registry := filepath.Join(t.TempDir(), ".agentflow-ui")
	useRegistry(t, registry)
	// The console never ran: no registry, no store, and nothing is created.
	if dir, ok, err := GlobalStoreDir(); err != nil || ok || dir != "" {
		t.Fatalf("no registry: %q, %t, %v", dir, ok, err)
	}
	if _, err := OpenGlobalEventStore(context.Background(), store.RoleReader); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("open without a registry: %v", err)
	}
	if _, err := os.Lstat(registry); !os.IsNotExist(err) {
		t.Fatalf("a read created the registry: %v", err)
	}
	// A registry without the store directory: still nothing to read, nothing created.
	prefix, err := daemonPrefix(registry)
	if err != nil {
		t.Fatal(err)
	}
	if dir, ok, err := GlobalStoreDir(); err != nil || ok || dir != "" {
		t.Fatalf("no store: %q, %t, %v", dir, ok, err)
	}
	if _, err := os.Lstat(prefix + globalStoreSuffix); !os.IsNotExist(err) {
		t.Fatalf("a read created the store directory: %v", err)
	}
	// The daemon's own lookup names the same directory.
	created, err := globalStoreDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir, ok, err := GlobalStoreDir(); err != nil || !ok || dir != created {
		t.Fatalf("with the store: %q, %t, %v, want %q", dir, ok, err, created)
	}
}

func TestAppReadsTheGlobalStoreThroughTheRegistry(t *testing.T) {
	ctx := context.Background()
	registry := filepath.Join(t.TempDir(), ".agentflow-ui")
	useRegistry(t, registry)
	dir, err := globalStoreDir()
	if err != nil {
		t.Fatal(err)
	}
	// The daemon owns the store and records a personal-layer change.
	owner, err := store.OpenGlobal(ctx, dir, store.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	e := bus.Event{Type: bus.TypeConfigLayerChanged, Source: "watch.config", Repo: bus.GlobalRepo, Payload: json.RawMessage(`{"layer":"personal","paths":["config.yml"]}`)}
	if _, err := owner.Append(ctx, e.Normalize(time.Now())); err != nil {
		t.Fatal(err)
	}
	defer owner.Close()

	// A reader coexists with the owner, and package app reaches the store
	// through the opener this package wires in.
	gitdir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", gitdir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	oldOpen := app.OpenEventStore
	app.OpenEventStore = func(context.Context, string, store.Role) (store.Store, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { app.OpenEventStore = oldOpen })
	a := &app.App{Repo: &gitx.Repo{Dir: gitdir}}
	page, err := a.Events(ctx, app.EventsQuery{})
	if err != nil || len(page.Events) != 1 || page.Events[0].Repo != bus.GlobalRepo || page.Cursor != "v1:global=1" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	// A reader never writes: Append is refused.
	reader, err := OpenGlobalEventStore(ctx, store.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.Append(ctx, e.Normalize(time.Now())); !errors.Is(err, store.ErrReadOnly) {
		t.Fatalf("a reader appended: %v", err)
	}
}
