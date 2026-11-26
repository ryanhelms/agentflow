package webui

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Fixture", "-c", "user.email=test@example.org"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func eventRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "chore: init")
	return dir
}

func TestIdentifyWorktreeMatchesConsoleIDs(t *testing.T) {
	dir := eventRepo(t)
	ctx := context.Background()
	main, err := IdentifyWorktree(ctx, dir)
	if err != nil || main.Worktree != "main" || main.Root != dir {
		t.Fatalf("primary = %+v, %v", main, err)
	}
	linked := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-Feature_X")
	gitIn(t, dir, "worktree", "add", "-q", "-b", "feature", linked)
	id, err := IdentifyWorktree(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := inspectWorktree(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	if string(id.Worktree) != r.ID || !id.Worktree.Valid() || !strings.HasPrefix(string(id.Worktree), strings.ToLower(dnsLabel(filepath.Base(linked)))+"-") || id.CommonDir != main.CommonDir {
		t.Fatalf("linked = %+v, console ID %s", id, r.ID)
	}
}

func TestOverviewProjectionState(t *testing.T) {
	dir := eventRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _, err := inspectWorktree(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	ev := &repoEvents{id: repoA, worktrees: map[bus.WorktreeID]registeredWorktree{}}
	ev.addWorktree(r)
	o := &overviewProjection{repo: ev}
	raw, err := o.State(ctx, ProjectionKey{Repo: repoA, Worktree: "main"})
	if err != nil {
		t.Fatal(err)
	}
	var st OverviewState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	head := gitIn(t, dir, "rev-parse", "HEAD")
	if st.Branch != "main" || st.OID != head || !strings.HasPrefix(head, st.Head) || st.Head == "" || st.Detached ||
		st.Changed != 2 || st.Staged != 1 || st.Untracked != 1 || st.Operation != "" || !st.Config.Valid {
		t.Fatalf("state = %s", raw)
	}

	// The same data /api/state reports for the overview.
	s := testServer(t)
	s.repo.Dir = dir
	rec := request(t, s, "GET", "/api/state", "", "session", nil)
	var api struct {
		Branch     string `json:"branch"`
		Head       string `json:"head"`
		DirtyCount int    `json:"dirty_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &api); err != nil || api.Branch != st.Branch || api.Head != st.Head || api.DirtyCount != st.Changed {
		t.Fatalf("/api/state = %s, overview = %s", rec.Body.String(), raw)
	}

	if err := os.WriteFile(filepath.Join(r.GitDir, "MERGE_HEAD"), []byte(head+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ = o.State(ctx, ProjectionKey{Repo: repoA, Worktree: "main"})
	if !strings.Contains(string(raw), `"operation":"merge"`) {
		t.Fatalf("merge state = %s", raw)
	}

	if st.Upstream != nil {
		t.Fatalf("upstream without a remote = %+v", st.Upstream)
	}
	remote := t.TempDir()
	gitIn(t, remote, "init", "-q", "--bare")
	gitIn(t, dir, "remote", "add", "origin", remote)
	gitIn(t, dir, "push", "-q", "-u", "origin", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "chore: ahead")
	raw, _ = o.State(ctx, ProjectionKey{Repo: repoA, Worktree: "main"})
	if !strings.Contains(string(raw), `"upstream":{"name":"origin/main","ahead":1,"behind":0}`) {
		t.Fatalf("upstream state = %s", raw)
	}

	if raw, _ := o.State(ctx, ProjectionKey{Repo: repoA, Worktree: "gone-00000000"}); string(raw) != "null" {
		t.Fatalf("unknown worktree = %s", raw)
	}
	if raw, _ := o.State(ctx, ProjectionKey{Repo: repoB, Worktree: "main"}); string(raw) != "null" {
		t.Fatalf("other repository = %s", raw)
	}
	keys := o.Keys(bus.Event{Type: bus.TypeGitRefsChanged, Repo: repoA})
	if len(keys) != 1 || keys[0].Worktree != "main" {
		t.Fatalf("repository-wide keys = %v", keys)
	}
	if keys := o.Keys(bus.Event{Type: bus.TypeGitHeadChanged, Repo: repoB, Worktree: "main"}); len(keys) != 0 {
		t.Fatalf("other repository keys = %v", keys)
	}
}

func TestEventRuntimeOwnsStoreAndRoutesEvents(t *testing.T) {
	dir := eventRepo(t)
	ctx := context.Background()
	r, _, err := inspectWorktree(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	var roles []store.Role
	old := openEventStore
	openEventStore = func(ctx context.Context, common string, role store.Role) (store.Store, error) {
		roles = append(roles, role)
		return app.OpenEventStore(ctx, common, role)
	}
	t.Cleanup(func() { openEventStore = old })
	rt, stop, err := startEventRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ev, err := rt.attach(r)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := rt.attach(r); err != nil || again != ev {
		t.Fatalf("second attach = %p, %v", again, err)
	}
	if len(roles) != 1 || roles[0] != store.RoleOwner {
		t.Fatalf("store roles = %v, want one RoleOwner", roles)
	}
	reader, err := store.Open(ctx, r.Common, store.RoleReader)
	if err != nil || reader.Repo() != ev.id {
		t.Fatalf("store on disk = %v; runtime %s", err, ev.id)
	}
	_ = reader.Close()
	l, unlisten := ev.projector.listen()
	defer unlisten()
	e, err := rt.bus.Publish(ctx, bus.Event{Type: bus.TypeGitHeadChanged, Source: "test", Repo: ev.id, Worktree: "main"})
	if err != nil || e.Seq != 1 {
		t.Fatalf("publish = %+v, %v", e, err)
	}
	if head, _ := ev.store.Head(ctx); head[ev.id] != 1 {
		t.Fatalf("store head = %v", head)
	}
	deadline := time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case u := <-l.ch:
			// The runtime's GitHub poller may report its health first; skip that
			// transient event and wait for the one published above.
			if u.event.Type != bus.TypeGitHeadChanged {
				continue
			}
			// git.head.changed also recomputes the repository's activity.
			if u.event.Seq != 1 || len(u.states) != 2 || u.states[0].Projection != "overview" || !strings.Contains(string(u.states[0].State), `"branch":"main"`) || u.states[1].Projection != ProjectionActivity {
				t.Fatalf("update = %+v", u)
			}
			got = true
		case <-deadline:
			t.Fatal("no projector update")
		}
	}
	if _, err := rt.bus.Publish(ctx, bus.Event{Type: bus.TypeGitHeadChanged, Source: "test", Repo: repoB, Worktree: "main"}); err == nil {
		t.Fatal("an event for a repository without a store was accepted")
	}
}

func TestEventWorkerHasOneOriginWidePath(t *testing.T) {
	rec := httptest.NewRecorder()
	(&daemon{}).pickerResource(rec, httptest.NewRequest("GET", "http://repo.localhost/_agentflow/event-worker.js", nil), registeredWorktree{})
	_, err := assets.ReadFile("static/event-worker.js")
	switch {
	case err == nil && (rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript")):
		t.Fatalf("worker = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	case err != nil && rec.Code != 404:
		t.Fatalf("missing worker = %d", rec.Code)
	}
}

// The watcher reports index writes as git.index.changed, so a projection
// that refreshed the index itself would wake itself up again.
func TestOverviewDoesNotRewriteTheIndex(t *testing.T) {
	dir := eventRepo(t)
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "a.txt")
	gitIn(t, dir, "commit", "-q", "-m", "chore: add a")
	// Same content, new stat data: a plain git status would refresh the index.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(dir, ".git", "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overviewOf(context.Background(), dir, filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("overview rewrote the index: %v -> %v", before.ModTime(), after.ModTime())
	}
}
