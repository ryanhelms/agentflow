package webui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
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
