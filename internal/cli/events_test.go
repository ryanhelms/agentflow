package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
)

func TestEventsCommandPrintsPageAndCursor(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if out, err := exec.Command("git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	const repo bus.RepoID = "0123456789abcdef"
	st := storetest.New(repo)
	for _, typ := range []bus.Type{bus.TypeGitHeadChanged, bus.TypeConfigLayerChanged} {
		e := bus.Event{Type: typ, Source: "watch.git", Repo: repo, Payload: json.RawMessage(`{}`)}
		if typ == bus.TypeGitHeadChanged {
			e.Worktree = "main"
		}
		if _, err := st.Append(context.Background(), e.Normalize(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	old, oldGlobal := app.OpenEventStore, app.OpenGlobalEventStore
	app.OpenEventStore = func(context.Context, string, store.Role) (store.Store, error) { return st, nil }
	app.OpenGlobalEventStore = func(context.Context, store.Role) (store.Store, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { app.OpenEventStore, app.OpenGlobalEventStore = old, oldGlobal })

	out, err := captureCLI(t, "events", "--type", "git.**")
	if err != nil || !strings.Contains(out, "git.head.changed") || strings.Contains(out, "config.layer.changed") || !strings.Contains(out, "cursor: v1:"+string(repo)+"=2") {
		t.Fatalf("text = %q, %v", out, err)
	}
	out, err = captureCLI(t, "events", "--json", "--limit", "1")
	var page app.EventsPage
	if err != nil || json.Unmarshal([]byte(out), &page) != nil || len(page.Events) != 1 || !page.More || page.Cursor != "v1:"+string(repo)+"=1" {
		t.Fatalf("json = %q, %v", out, err)
	}
	if _, err := captureCLI(t, "events", "--topic", "fedcba9876543210/*/**"); err == nil {
		t.Fatal("a topic of another repository was accepted")
	}
	if _, err := captureCLI(t, "events", "extra"); err == nil {
		t.Fatal("a positional argument was accepted")
	}
}

func TestEventsCommandAllReposAndGlobal(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if out, err := exec.Command("git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	const here, there bus.RepoID = "0123456789abcdef", "fedcba9876543210"
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	add := func(st *storetest.Store, repo bus.RepoID, typ bus.Type, at time.Duration, payload string) {
		t.Helper()
		e := bus.Event{Type: typ, Source: "watch.git", Repo: repo, Time: base.Add(at), Payload: json.RawMessage(payload)}
		if typ == bus.TypeGitHeadChanged {
			e.Worktree = "main"
		}
		if _, err := st.Append(context.Background(), e.Normalize(base)); err != nil {
			t.Fatal(err)
		}
	}
	local, other, global := storetest.New(here), storetest.New(there), storetest.New(bus.GlobalRepo)
	add(local, here, bus.TypeGitHeadChanged, 0, `{}`)
	add(other, there, bus.TypeGitHeadChanged, time.Second, `{}`)
	add(global, bus.GlobalRepo, bus.TypeConfigLayerChanged, 2*time.Second, `{"layer":"personal"}`)
	otherDir := filepath.Join(t.TempDir(), "other.git")
	oldOpen, oldGlobal, oldServed := app.OpenEventStore, app.OpenGlobalEventStore, servedRepos
	app.OpenEventStore = func(_ context.Context, common string, _ store.Role) (store.Store, error) {
		if common == otherDir {
			return other, nil
		}
		return local, nil
	}
	app.OpenGlobalEventStore = func(context.Context, store.Role) (store.Store, error) { return global, nil }
	servedRepos = func(context.Context) ([]string, error) { return []string{otherDir}, nil }
	t.Cleanup(func() { app.OpenEventStore, app.OpenGlobalEventStore, servedRepos = oldOpen, oldGlobal, oldServed })

	// By default: this repository and global; each line names its repository.
	out, err := captureCLI(t, "events")
	if err != nil || strings.Contains(out, string(there)) || !strings.Contains(out, string(here)) || !strings.Contains(out, "global") || strings.Count(out, "\n") != 3 {
		t.Fatalf("default = %q, %v", out, err)
	}
	if !strings.Contains(out, "cursor: v1:"+string(here)+"=1,global=1") {
		t.Fatalf("default cursor in %q", out)
	}
	out, err = captureCLI(t, "events", "--all-repos")
	if err != nil || !strings.Contains(out, string(there)) || strings.Count(out, "\n") != 4 {
		t.Fatalf("--all-repos = %q, %v", out, err)
	}
	out, err = captureCLI(t, "events", "--all-repos", "--json")
	var page app.EventsPage
	if err != nil || json.Unmarshal([]byte(out), &page) != nil || len(page.Events) != 3 || page.Events[1].Repo != there || page.Repo != here {
		t.Fatalf("--all-repos --json = %q, %v", out, err)
	}
	out, err = captureCLI(t, "events", "--all-repos", "--topic", string(there)+"/*/**")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err != nil || len(lines) != 2 || !strings.Contains(lines[0], string(there)) || !strings.HasPrefix(lines[1], "cursor:") {
		t.Fatalf("--topic of a served repository = %q, %v", out, err)
	}
	// Another repository's topic needs --all-repos.
	if _, err := captureCLI(t, "events", "--topic", string(there)+"/*/**"); err == nil || !strings.Contains(err.Error(), "--all-repos") {
		t.Fatalf("foreign topic without --all-repos: %v", err)
	}
}
