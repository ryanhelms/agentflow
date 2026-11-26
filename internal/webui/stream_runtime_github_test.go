package webui

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/watch"
	"github.com/ryanhelms/agentflow/internal/worktree"
)

// fakeGitHubPoller records the targets the extension sets.
type fakeGitHubPoller struct {
	mu   sync.Mutex
	sets []watch.GitHubTarget
}

func (p *fakeGitHubPoller) Set(t watch.GitHubTarget) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t.Branches, t.Issues = maps.Clone(t.Branches), maps.Clone(t.Issues)
	p.sets = append(p.sets, t)
}

func (p *fakeGitHubPoller) Run(ctx context.Context, _ bus.Publisher) error {
	<-ctx.Done()
	return nil
}

func (p *fakeGitHubPoller) GitHubState(bus.RepoID) (watch.GitHubState, bool) {
	return watch.GitHubState{Health: watch.GitHubOK}, true
}

func (p *fakeGitHubPoller) last() (watch.GitHubTarget, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sets) == 0 {
		return watch.GitHubTarget{}, 0
	}
	return p.sets[len(p.sets)-1], len(p.sets)
}

// waitTarget waits until the latest target satisfies ok.
func (p *fakeGitHubPoller) waitTarget(t *testing.T, desc string, ok func(watch.GitHubTarget) bool) watch.GitHubTarget {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if tg, n := p.last(); n > 0 && ok(tg) {
			return tg
		}
		if time.Now().After(deadline) {
			tg, n := p.last()
			t.Fatalf("no target with %s after %d sets; last = %+v", desc, n, tg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// githubRuntime starts an event runtime that runs only the GitHub extension
// with a fake poller and a fast refresh, and attaches a primary checkout and
// a linked worktree bound to issue 35.
func githubRuntime(t *testing.T, slugErr error) (rt *eventRuntime, poller *fakeGitHubPoller, primary, linked registeredWorktree, linkedDir string) {
	t.Helper()
	dir := eventRepo(t)
	linkedDir = filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-feature")
	gitIn(t, dir, "worktree", "add", "-q", "-b", "feature", linkedDir)
	ctx := context.Background()
	if err := worktree.WriteRecord(ctx, linkedDir, worktree.Record{Issue: "acme/repo#35", Branch: "feature"}); err != nil {
		t.Fatal(err)
	}
	var err error
	if primary, _, err = inspectWorktree(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if linked, _, err = inspectWorktree(ctx, linkedDir); err != nil {
		t.Fatal(err)
	}

	poller = &fakeGitHubPoller{}
	oldPoller, oldSlug, oldEvery, oldExts := newGitHubPoller, githubSlug, githubRefreshEvery, runtimeExtensions
	t.Cleanup(func() {
		newGitHubPoller, githubSlug, githubRefreshEvery, runtimeExtensions = oldPoller, oldSlug, oldEvery, oldExts
	})
	newGitHubPoller = func(func(context.Context, watch.GitHubTarget) (watch.GitHubAPI, error), func(error)) watch.GitHubPoller {
		return poller
	}
	githubSlug = func(context.Context, string) (string, error) {
		if slugErr != nil {
			return "", slugErr
		}
		return "acme/repo", nil
	}
	githubRefreshEvery = 10 * time.Millisecond
	runtimeExtensions = []func() runtimeExtension{func() runtimeExtension { return &githubExtension{} }}

	rt, stop, err := startEventRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	for _, w := range []registeredWorktree{primary, linked} {
		if _, err := rt.attach(w); err != nil {
			t.Fatal(err)
		}
	}
	return rt, poller, primary, linked, linkedDir
}

// The extension tells the poller which branch each served worktree has
// checked out, which issue each is bound to, and whether anyone is watching,
// and follows changes to all three.
func TestGitHubExtensionKeepsTargetCurrent(t *testing.T) {
	rt, poller, primary, linked, linkedDir := githubRuntime(t, nil)
	linkedID, primaryID := bus.WorktreeID(linked.ID), bus.WorktreeID(primary.ID)

	tg := poller.waitTarget(t, "both worktrees", func(tg watch.GitHubTarget) bool { return len(tg.Branches) == 2 })
	if tg.Repo == "" || tg.Slug != "acme/repo" || tg.Watched ||
		tg.Branches[primaryID] != "main" || tg.Branches[linkedID] != "feature" ||
		len(tg.Issues) != 1 || tg.Issues[linkedID] != 35 {
		t.Fatalf("target = %+v", tg)
	}
	if rt.githubCache() == nil {
		t.Fatal("no cache for projections")
	}

	// An open event stream makes the repository watched.
	r := rt.byID[tg.Repo]
	_, release := r.projector.listen()
	poller.waitTarget(t, "a viewer", func(tg watch.GitHubTarget) bool { return tg.Watched })
	release()
	poller.waitTarget(t, "no viewer", func(tg watch.GitHubTarget) bool { return !tg.Watched })

	// A branch switch.
	gitIn(t, linkedDir, "checkout", "-q", "-b", "other")
	poller.waitTarget(t, "the new branch", func(tg watch.GitHubTarget) bool { return tg.Branches[linkedID] == "other" })

	// A detached HEAD leaves the worktree out.
	gitIn(t, linkedDir, "checkout", "-q", "--detach")
	poller.waitTarget(t, "a detached worktree", func(tg watch.GitHubTarget) bool {
		_, ok := tg.Branches[linkedID]
		return !ok && len(tg.Branches) == 1
	})
}

// An issue of another repository is not bound, and a repository without a
// GitHub remote gets an empty slug, which disables its poller.
func TestGitHubExtensionWithoutGitHubRemote(t *testing.T) {
	_, poller, _, _, _ := githubRuntime(t, errors.New("remote origin is not a GitHub.com repository"))
	tg := poller.waitTarget(t, "a target", func(watch.GitHubTarget) bool { return true })
	if tg.Slug != "" || len(tg.Issues) != 0 {
		t.Fatalf("target = %+v", tg)
	}
}

func TestBoundIssue(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	for _, tc := range []struct {
		issue string
		n     int
		ok    bool
	}{
		{"acme/repo#35", 35, true},
		{"ACME/Repo#7", 7, true},
		{"other/repo#35", 0, false},
		{"PLAT-123", 0, false},
		{"acme/repo#0", 0, false},
		{"acme/repo#x", 0, false},
		{"", 0, false},
	} {
		if err := worktree.WriteRecord(context.Background(), dir, worktree.Record{Issue: tc.issue}); err != nil {
			t.Fatal(err)
		}
		if n, ok := boundIssue(dir, "acme/repo"); n != tc.n || ok != tc.ok {
			t.Errorf("boundIssue(%q) = %d, %v", tc.issue, n, ok)
		}
	}
	if _, ok := boundIssue(t.TempDir(), "acme/repo"); ok {
		t.Error("a directory without a record is bound")
	}
}

func TestCheckedOutBranch(t *testing.T) {
	dir := t.TempDir()
	for content, want := range map[string]string{
		"ref: refs/heads/feature/x\n": "feature/x",
		"ref: refs/heads/main":        "main",
		"0123456789abcdef\n":          "",
		"ref: refs/remotes/o/x\n":     "",
	} {
		if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := checkedOutBranch(dir); got != want {
			t.Errorf("checkedOutBranch(%q) = %q, want %q", content, got, want)
		}
	}
	if got := checkedOutBranch(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("missing HEAD = %q", got)
	}
}

// One client is made per repository and kept while its worktree exists.
func TestGitHubExtensionReusesClient(t *testing.T) {
	rt, _, primary, _, _ := githubRuntime(t, nil)
	var made []string
	old := githubClient
	t.Cleanup(func() { githubClient = old })
	githubClient = func(_ context.Context, root string) (watch.GitHubAPI, error) {
		made = append(made, root)
		return &fakeGitHubAPI{}, nil
	}
	x := rt.exts[0].(*githubExtension)
	var id bus.RepoID
	for repo := range rt.byID {
		id = repo
	}
	for range 3 {
		if _, err := x.api(context.Background(), watch.GitHubTarget{Repo: id}); err != nil {
			t.Fatal(err)
		}
	}
	if len(made) != 1 || made[0] != primary.Root {
		t.Fatalf("clients made for %v, want one for the primary checkout %s", made, primary.Root)
	}
	if _, err := x.api(context.Background(), watch.GitHubTarget{Repo: "ffffffffffffffff"}); err == nil {
		t.Fatal("a client for an unserved repository")
	}
}

type fakeGitHubAPI struct{}

func (*fakeGitHubAPI) GetConditional(context.Context, string, string, any) (watch.GitHubMeta, error) {
	return watch.GitHubMeta{}, nil
}

func (*fakeGitHubAPI) GraphQL(context.Context, string, map[string]any, any) error { return nil }
