package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
)

// openStore keeps the fake store open across the hooks' separate opens.
type openStore struct{ *storetest.Store }

func (openStore) Close() error { return nil }

// storedEvents returns what the hooks published to st.
func storedEvents(t *testing.T, st *storetest.Store) []bus.Event {
	t.Helper()
	var out []bus.Event
	for e, err := range st.ReadFrom(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func hookRuns(events []bus.Event, hook string) (n int) {
	for _, e := range events {
		if e.Type == bus.TypeHookRan && e.Source == "hook:"+hook {
			n++
		}
	}
	return n
}

func TestHookCommandsPublishAndLintMsgDoesNot(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "dev@example.com"},
		{"config", "user.name", "Dev"},
		{"commit", "-q", "--allow-empty", "-m", "feat: add quota api"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	// Hooks publish only to a repository that already has an event store
	// (a daemon creates it), so mark it as the daemon would.
	storeDir := store.Path(filepath.Join(dir, ".git"))
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, store.RepoIDFile), []byte("0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := storetest.New("0123456789abcdef")
	old := app.OpenEventStore
	app.OpenEventStore = func(_ context.Context, _ string, role store.Role) (store.Store, error) {
		if role != store.RoleAppender {
			t.Errorf("a hook opened the store as %v", role)
		}
		return openStore{st}, nil
	}
	t.Cleanup(func() { app.OpenEventStore = old })
	bad := filepath.Join(dir, "MSG")
	if err := os.WriteFile(bad, []byte("updated stuff\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// lint-msg runs for every commit of a push or a PR: it validates and publishes nothing.
	if _, err := captureCLI(t, "lint-msg", bad); err == nil {
		t.Fatal("lint-msg accepted a bad message")
	}
	if got := storedEvents(t, st); len(got) != 0 {
		t.Fatalf("lint-msg published %d events", len(got))
	}

	// The git hook rejects the same message and says so on the bus, however
	// the process was started (the test binary's arguments do not matter).
	if _, err := captureCLI(t, "hook", "commit-msg", bad); err == nil {
		t.Fatal("hook commit-msg accepted a bad message")
	}
	if got := storedEvents(t, st); hookRuns(got, "commit-msg") != 1 {
		t.Fatalf("hook commit-msg published %+v", got)
	}

	// post-commit publishes commit.created and hook.ran, prints nothing and succeeds.
	out, err := captureCLI(t, "hook", "post-commit")
	if err != nil || out != "" {
		t.Fatalf("hook post-commit = %q, %v", out, err)
	}
	got := storedEvents(t, st)
	var created int
	for _, e := range got {
		if e.Type == bus.TypeCommitCreated && e.Source == "hook:post-commit" {
			created++
		}
	}
	if created != 1 || hookRuns(got, "post-commit") != 1 {
		t.Fatalf("hook post-commit published %+v", got)
	}
}

func TestPostCommitNeverFailsTheCommit(t *testing.T) {
	// Outside a repository, with no daemon and a store that cannot be opened.
	t.Chdir(t.TempDir())
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	old := app.OpenEventStore
	app.OpenEventStore = func(context.Context, string, store.Role) (store.Store, error) { return nil, os.ErrPermission }
	t.Cleanup(func() { app.OpenEventStore = old })
	out, err := captureCLI(t, "hook", "post-commit")
	if err != nil || out != "" {
		t.Fatalf("hook post-commit = %q, %v", out, err)
	}
	if !strings.Contains(usage, "post-commit") {
		t.Fatal("the usage text does not list hook post-commit")
	}
}
