package repository

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/provider"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{
		"-c", "user.name=test", "-c", "user.email=test@example.org", "-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=" + os.DevNull,
	}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", args[0], out, err)
	}
	return strings.TrimSpace(string(out))
}

func put(t *testing.T, file, body string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	origin, engineer string
	cfg              Config
}

func setup(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "remote.git")
	engineer := filepath.Join(root, "engineer")
	git(t, root, "init", "--bare", "--initial-branch=trunk", origin)
	git(t, root, "init", "--initial-branch=trunk", engineer)
	put(t, filepath.Join(engineer, "notes.txt"), "unrelated\n")
	put(t, filepath.Join(engineer, ".gitattributes"), "*.md text eol=crlf\n")
	git(t, engineer, "add", "--", "notes.txt", ".gitattributes")
	git(t, engineer, "commit", "-m", "seed")
	git(t, engineer, "push", origin, "HEAD:refs/heads/trunk")
	put(t, filepath.Join(engineer, "notes.txt"), "engineer's staged work")
	git(t, engineer, "add", "--", "notes.txt")
	put(t, filepath.Join(engineer, "notes.txt"), "engineer's unstaged work")
	return fixture{origin, engineer, Config{Instance: "team", Origin: origin, Directory: ".agentflow/records", Publication: "push"}}
}

func tasks(t *testing.T, cfg Config) *Tasks {
	t.Helper()
	a, err := NewTasks(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func knowledge(t *testing.T, cfg Config) *Knowledge {
	t.Helper()
	a, err := NewKnowledge(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func key(id string) provider.WriteOptions { return provider.WriteOptions{OperationID: id} }

func TestTwoClientsRefetchAndReplay(t *testing.T) {
	t.Parallel()
	f := setup(t)
	ctx := t.Context()
	left, right := tasks(t, f.cfg), tasks(t, f.cfg)
	before := git(t, f.engineer, "status", "--porcelain", "--branch")
	head := git(t, f.engineer, "rev-parse", "HEAD")
	draft := provider.TaskDraft{Scope: "work", Title: "one"}
	var attempts int
	left.a.push = func(c checkout, ctx context.Context, commit, branch string) error {
		attempts++
		if attempts == 1 {
			_, err := right.Create(ctx, provider.TaskDraft{Scope: "work", Title: "two"}, key("other"))
			if err != nil {
				t.Fatal(err)
			}
		}
		return c.push(ctx, commit, branch)
	}
	r, err := left.Create(ctx, draft, key("stable"))
	if err != nil || r.Status != provider.Applied || attempts != 2 {
		t.Fatalf("create: %+v, %v (%d attempts)", r, err, attempts)
	}
	items, err := right.Find(ctx, provider.TaskQuery{Scope: "work"})
	if err != nil || len(items) != 2 {
		t.Fatalf("find after competing updates: %v, %v", items, err)
	}
	state := provider.TaskState{ID: "done", Name: "Done", Terminal: true}
	done, err := right.Transition(ctx, r.Ref, state, provider.WriteOptions{OperationID: "finish", ExpectedRevision: r.Revision})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := left.Create(ctx, draft, key("stable"))
	if err != nil || replay.Status != provider.AlreadyApplied || replay.Revision != done.Revision || replay.Ref != r.Ref {
		t.Fatalf("replay: %+v, %v", replay, err)
	}
	draft.Title = "different"
	if _, err := left.Create(ctx, draft, key("stable")); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("reused key: %v", err)
	}
	task, err := left.Get(ctx, r.Ref)
	if err != nil || task.State != state {
		t.Fatalf("get: %+v, %v", task, err)
	}
	if count := git(t, f.origin, "rev-list", "--count", "trunk"); count != "4" {
		t.Fatalf("unexpected history length %s", count)
	}
	if git(t, f.engineer, "status", "--porcelain", "--branch") != before || git(t, f.engineer, "rev-parse", "HEAD") != head {
		t.Fatal("engineer checkout changed")
	}
}

func TestConflictingRevisions(t *testing.T) {
	t.Parallel()
	f := setup(t)
	left, right := knowledge(t, f.cfg), knowledge(t, f.cfg)
	ctx := t.Context()
	ref := provider.Ref{Instance: f.cfg.Instance, Scope: "docs", ID: "guide"}
	first, err := left.Publish(ctx, ref, provider.DocumentDraft{Title: "first"}, key("create"))
	if err != nil {
		t.Fatal(err)
	}
	left.a.push = func(c checkout, ctx context.Context, commit, branch string) error {
		_, err := right.Publish(ctx, ref, provider.DocumentDraft{Title: "winner"}, provider.WriteOptions{ExpectedRevision: first.Revision, OperationID: "winner"})
		if err != nil {
			t.Fatal(err)
		}
		return c.push(ctx, commit, branch)
	}
	_, err = left.Publish(ctx, ref, provider.DocumentDraft{Title: "loser"}, provider.WriteOptions{ExpectedRevision: first.Revision, OperationID: "loser"})
	if !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("stale publish: %v", err)
	}
	got, err := left.Find(ctx, provider.DocumentQuery{Text: "winner"})
	if err != nil || len(got) != 1 || got[0].Ref != ref {
		t.Fatalf("winner: %+v, %v", got, err)
	}
}

func TestAmbiguousPush(t *testing.T) {
	t.Parallel()
	f := setup(t)
	a := knowledge(t, f.cfg)
	var pushes int
	a.a.push = func(c checkout, ctx context.Context, commit, branch string) error {
		pushes++
		if err := c.push(ctx, commit, branch); err != nil {
			t.Fatal(err)
		}
		return errors.New("lost acknowledgement")
	}
	ref := provider.Ref{Instance: "team", Scope: "docs", ID: "one"}
	r, err := a.Publish(t.Context(), ref, provider.DocumentDraft{Title: "title", Body: "body\r\n"}, provider.WriteOptions{})
	if err != nil || r.Status != provider.AlreadyApplied || pushes != 1 {
		t.Fatalf("reconcile: %+v, %v (%d pushes)", r, err, pushes)
	}
	got, err := a.Get(t.Context(), ref)
	if err != nil || got.Revision != r.Revision || got.Body != "body\r\n" {
		t.Fatalf("fetched revision: %+v, %v", got, err)
	}
	if git(t, f.origin, "rev-list", "--count", "trunk") != "2" {
		t.Fatal("unexpected duplicate commit")
	}
}

type reviewPublisher func(Publication) (string, error)

func (p reviewPublisher) EnsurePullRequest(_ context.Context, req Publication) (string, error) {
	return p(req)
}

func TestReviewPendingAndReplay(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.cfg.Publication = ""
	ctx := t.Context()
	a := tasks(t, f.cfg)
	draft := provider.TaskDraft{Scope: "work", Title: "proposed"}
	old := git(t, f.origin, "rev-parse", "trunk")
	a.a.push = func(c checkout, ctx context.Context, commit, branch string) error {
		if err := c.push(ctx, commit, branch); err != nil {
			t.Fatal(err)
		}
		return errors.New("review branch acknowledgement lost")
	}
	r, err := a.Create(ctx, draft, key("request"))
	if err != nil || r.Status != provider.PendingReview || r.URL != "" {
		t.Fatalf("pending without publisher: %+v, %v", r, err)
	}
	if _, err := a.Get(ctx, r.Ref); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("read unmerged proposal: %v", err)
	}
	var head string
	lost := errors.New("publisher unavailable")
	f.cfg.Publisher = reviewPublisher(func(p Publication) (string, error) {
		if p.Base != "trunk" || p.OperationID != "request" || p.Origin != f.origin || p.Ref != r.Ref {
			t.Fatalf("unexpected publication: %+v", p)
		}
		if head == "" {
			head = p.Head
			return "", lost
		}
		if head != p.Head {
			t.Fatal("publication branch changed")
		}
		return "https://example.invalid/pulls/1", nil
	})
	b := tasks(t, f.cfg)
	pending, err := b.Create(ctx, draft, key("request"))
	if !errors.Is(err, lost) || pending.Status != provider.PendingReview {
		t.Fatalf("publisher failure: %+v, %v", pending, err)
	}
	pending, err = b.Create(ctx, draft, key("request"))
	if err != nil || pending.Status != provider.PendingReview || pending.URL == "" || pending.Revision != r.Revision {
		t.Fatalf("reused proposal: %+v, %v", pending, err)
	}
	if git(t, f.origin, "rev-parse", "trunk") != old || git(t, f.origin, "rev-list", "--count", head) != "2" {
		t.Fatal("review replay changed history")
	}
	different := draft
	different.Title = "wrong"
	if _, err := b.Create(ctx, different, key("request")); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("reused pending key: %v", err)
	}
	// Simulate a reviewer accepting the change in the bare fixture.
	git(t, f.origin, "update-ref", "refs/heads/trunk", git(t, f.origin, "rev-parse", head), old)
	r, err = b.Create(ctx, draft, key("request"))
	if err != nil || r.Status != provider.AlreadyApplied {
		t.Fatalf("merged proposal: %+v, %v", r, err)
	}
}

func TestRenamedRecordAndCheckoutIsolation(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	cfg := f.cfg
	cfg.Branch = "data"
	git(t, f.origin, "branch", cfg.Branch, "trunk")
	a := tasks(t, cfg)
	r, err := a.Create(ctx, provider.TaskDraft{Scope: "work", Title: "one"}, key("new"))
	if err != nil {
		t.Fatal(err)
	}
	// Rename a record and add unrelated changes on the configured branch.
	admin := t.TempDir()
	git(t, admin, "clone", "--branch", cfg.Branch, f.origin, ".")
	old := cfg.Directory + "/task-" + r.Ref.StorageKey() + ".md"
	renamed := cfg.Directory + "/-use [this], please.MD"
	git(t, admin, "mv", "--", old, renamed)
	put(t, filepath.Join(admin, cfg.Directory, "unrelated.txt"), "leave alone")
	git(t, admin, "add", "--", cfg.Directory+"/unrelated.txt")
	git(t, admin, "update-index", "--chmod=+x", "--", renamed)
	git(t, admin, "commit", "-m", "rename record")
	git(t, admin, "push", f.origin, "HEAD:refs/heads/data")
	start := git(t, f.engineer, "rev-parse", "HEAD")
	t.Setenv("GIT_DIR", filepath.Join(f.engineer, ".git"))
	t.Setenv("GIT_WORK_TREE", f.engineer)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(f.engineer, ".git", "index"))
	got, err := a.Get(ctx, r.Ref)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Transition(ctx, r.Ref, provider.TaskState{ID: "done"}, provider.WriteOptions{ExpectedRevision: got.Revision, OperationID: "changed"})
	if err != nil {
		t.Fatal(err)
	}
	// Inspect from the shell without the simulated hook environment.
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	if changed := git(t, f.origin, "diff", "--name-only", "data^", "data"); changed != renamed {
		t.Fatalf("committed paths: %q", changed)
	}
	if mode := git(t, f.origin, "ls-tree", "data", "--", renamed); !strings.HasPrefix(mode, "100755 ") {
		t.Fatalf("file mode changed: %s", mode)
	}
	if git(t, f.origin, "rev-parse", "trunk") != start || git(t, f.engineer, "rev-parse", "HEAD") != start ||
		git(t, f.engineer, "show", ":notes.txt") != "engineer's staged work" {
		t.Fatal("operation changed the default branch or the engineer's checkout")
	}
}

func TestBoundedPushes(t *testing.T) {
	t.Parallel()
	f := setup(t)
	a := tasks(t, f.cfg)
	calls := 0
	a.a.push = func(c checkout, ctx context.Context, commit, branch string) error {
		calls++
		if calls > maxPushes {
			t.Fatal("exceeded push budget")
		}
		parent := git(t, f.origin, "rev-parse", branch)
		tree := git(t, f.origin, "rev-parse", parent+"^{tree}")
		next := git(t, f.origin, "commit-tree", tree, "-p", parent, "-m", "concurrent write")
		git(t, f.origin, "update-ref", "refs/heads/"+branch, next, parent)
		return c.push(ctx, commit, branch)
	}
	_, err := a.Create(t.Context(), provider.TaskDraft{Scope: "tasks", Title: "starved"}, key("limited"))
	if !errors.Is(err, provider.ErrConflict) || calls != maxPushes {
		t.Fatalf("retry limit: %v after %d attempts", err, calls)
	}
	calls = 0
	denied := errors.New("push refused")
	a.a.push = func(checkout, context.Context, string, string) error {
		calls++
		return denied
	}
	_, err = a.Create(t.Context(), provider.TaskDraft{Scope: "tasks", Title: "denied"}, key("denied"))
	if !errors.Is(err, denied) || calls != 1 {
		t.Fatalf("permanent failure: %v after %d attempts", err, calls)
	}
}

func TestPathBoundaries(t *testing.T) {
	t.Parallel()
	f := setup(t)
	for _, directory := range []string{".git", "records/.GiT/hooks", "a/../b", "/etc", "../outside", ".git.", "git~1/refs", "a\\..\\escape", "C:/records"} {
		cfg := f.cfg
		cfg.Directory = directory
		if _, err := NewTasks(cfg); err == nil {
			t.Errorf("accepted directory %q", directory)
		}
	}
	for _, origin := range []string{"--upload-pack=anything", "ext::command", "helper::https://example.invalid/foo"} {
		cfg := f.cfg
		cfg.Origin = origin
		if _, err := NewTasks(cfg); err == nil {
			t.Errorf("accepted origin %q", origin)
		}
	}
	for _, branch := range []string{"--upload-pack=anything", "trunk:refs/heads/other", "trunk*"} {
		cfg := f.cfg
		cfg.Branch = branch
		if _, err := tasks(t, cfg).Find(t.Context(), provider.TaskQuery{}); err == nil {
			t.Errorf("accepted branch %q", branch)
		}
	}
	// Install a Git symlink without requiring OS symlink privileges.
	target := filepath.Join(f.engineer, "notes.txt")
	blob := git(t, f.engineer, "hash-object", "-w", target)
	git(t, f.engineer, "update-index", "--add", "--cacheinfo", "120000", blob, "link")
	git(t, f.engineer, "update-index", "--add", "--cacheinfo", "120000", blob, "records/link.md")
	git(t, f.engineer, "commit", "-m", "add links")
	git(t, f.engineer, "push", f.origin, "HEAD:refs/heads/trunk")
	for _, directory := range []string{"link/subdir", "records"} {
		cfg := f.cfg
		cfg.Directory = directory
		if _, err := tasks(t, cfg).Create(t.Context(), provider.TaskDraft{Scope: "test", Title: "escape"}, key("try")); err == nil {
			t.Errorf("accepted symlink under %q", directory)
		}
	}
}

func TestConcurrentTransitionPinsOriginalRevision(t *testing.T) {
	t.Parallel()
	f := setup(t)
	a, b := tasks(t, f.cfg), tasks(t, f.cfg)
	ctx := t.Context()
	r, err := a.Create(ctx, provider.TaskDraft{Scope: "ops", Title: "task"}, key("create"))
	if err != nil {
		t.Fatal(err)
	}
	a.a.push = func(c checkout, ctx context.Context, commit, branch string) error {
		_, err := b.Transition(ctx, r.Ref, provider.TaskState{ID: "closed"}, key("close"))
		if err != nil {
			t.Fatal(err)
		}
		return c.push(ctx, commit, branch)
	}
	if _, err := a.Transition(ctx, r.Ref, provider.TaskState{ID: "started"}, key("start")); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("expected racing transition to conflict: %v", err)
	}
	got, err := a.Get(ctx, r.Ref)
	if err != nil || got.State.ID != "closed" {
		t.Fatalf("winning transition: %+v, %v", got, err)
	}
}

func TestAmbiguousPushFollowedByRemoval(t *testing.T) {
	t.Parallel()
	f := setup(t)
	a := knowledge(t, f.cfg)
	oldTree := git(t, f.origin, "rev-parse", "trunk^{tree}")
	a.a.push = func(c checkout, ctx context.Context, commit, branch string) error {
		if err := c.push(ctx, commit, branch); err != nil {
			t.Fatal(err)
		}
		removed := git(t, f.origin, "commit-tree", oldTree, "-p", commit, "-m", "remove record")
		git(t, f.origin, "update-ref", "refs/heads/"+branch, removed, commit)
		return errors.New("lost acknowledgement")
	}
	ref := provider.Ref{Instance: "team", Scope: "docs", ID: "removed"}
	_, err := a.Publish(t.Context(), ref, provider.DocumentDraft{Title: "removed"}, key("lost"))
	if !errors.Is(err, provider.ErrConflict) || git(t, f.origin, "rev-list", "--count", "trunk") != "3" {
		t.Fatalf("expected to preserve the removal: %v", err)
	}
	fresh := knowledge(t, f.cfg)
	_, err = fresh.Publish(t.Context(), ref, provider.DocumentDraft{Title: "removed"}, key("lost"))
	if !errors.Is(err, provider.ErrConflict) || git(t, f.origin, "rev-list", "--count", "trunk") != "3" {
		t.Fatalf("cross-client replay resurrected the record: %v", err)
	}
}
