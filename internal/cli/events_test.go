package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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
	old := app.OpenEventStore
	app.OpenEventStore = func(context.Context, string, store.Role) (store.Store, error) { return st, nil }
	t.Cleanup(func() { app.OpenEventStore = old })

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
