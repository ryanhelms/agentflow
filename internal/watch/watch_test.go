package watch_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/watch"
	"github.com/ryanhelms/agentflow/internal/watch/watchtest"
)

func targets(t *testing.T) (primary, linked watch.Target) {
	t.Helper()
	base := t.TempDir()
	common := filepath.Join(base, "repo", ".git")
	primary = watch.Target{Repo: "0123456789abcdef", Worktree: "main", Root: filepath.Join(base, "repo"), GitDir: common, CommonDir: common}
	linked = watch.Target{Repo: primary.Repo, Worktree: "feature-1a2b3c4d", Root: filepath.Join(base, "feature"),
		GitDir: filepath.Join(common, "worktrees", "feature"), CommonDir: common}
	return primary, linked
}

func TestClassify(t *testing.T) {
	primary, linked := targets(t)
	c := primary.CommonDir
	cases := []struct {
		target watch.Target
		path   string
		want   bus.Type
	}{
		{primary, filepath.Join(c, "HEAD"), bus.TypeGitHeadChanged},
		{primary, filepath.Join(c, "index"), bus.TypeGitIndexChanged},
		{primary, filepath.Join(c, "rebase-merge", "done"), bus.TypeGitOperationChanged},
		{primary, filepath.Join(c, "MERGE_HEAD"), bus.TypeGitOperationChanged},
		{primary, filepath.Join(c, "refs", "heads", "main"), bus.TypeGitRefsChanged},
		{primary, filepath.Join(c, "packed-refs"), bus.TypeGitRefsChanged},
		{primary, filepath.Join(c, "worktrees", "feature"), bus.TypeGitWorktreesChanged},
		{primary, filepath.Join(c, "worktrees", "feature", "HEAD"), ""},
		{primary, filepath.Join(primary.Root, ".agentflow", "config.yml"), bus.TypeConfigLayerChanged},
		{primary, filepath.Join(primary.Root, ".agentflow", "providers", "jira.yaml"), bus.TypeConfigLayerChanged},
		{primary, filepath.Join(primary.Root, ".agentflow", "bin", "agentflow"), ""},
		{primary, filepath.Join(primary.Root, "main.go"), ""},
		{linked, filepath.Join(c, "worktrees", "feature", "HEAD"), bus.TypeGitHeadChanged},
		{linked, filepath.Join(c, "worktrees", "feature", "index"), bus.TypeGitIndexChanged},
		{linked, filepath.Join(c, "refs", "heads", "feature"), bus.TypeGitRefsChanged},
		{linked, filepath.Join(linked.Root, ".agentflow", "releases.yml"), bus.TypeConfigLayerChanged},
	}
	for _, tc := range cases {
		got, ok := watch.Classify(tc.target, tc.path)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("Classify(%s, %s) = %q, %v; want %q", tc.target.Worktree, tc.path, got, ok, tc.want)
		}
	}
}

func TestExcludedCoversTheEventStore(t *testing.T) {
	primary, _ := targets(t)
	c := primary.CommonDir
	for _, p := range []string{
		filepath.Join(c, "agentflow", "events", "00000000000000000001.jsonl"),
		filepath.Join(c, "agentflow"),
		filepath.Join(c, "objects", "ab", "cdef"),
		filepath.Join(c, "logs", "HEAD"),
		filepath.Join(c, "index.lock"),
		filepath.Join(c, "refs", "heads", "main.lock"),
	} {
		if !watch.Excluded(c, p) {
			t.Errorf("Excluded(%s) = false", p)
		}
		if _, ok := watch.Classify(primary, p); ok {
			t.Errorf("Classify(%s) reported an excluded path", p)
		}
	}
	if watch.Excluded(c, filepath.Join(c, "HEAD")) {
		t.Error("HEAD excluded")
	}
	for _, p := range append(watch.WorktreePaths(primary), watch.RepoPaths(c)...) {
		if watch.Excluded(c, p.Dir) {
			t.Errorf("watch list contains excluded %s", p.Dir)
		}
	}
}

func TestFakeSource(t *testing.T) {
	s := watchtest.New(watch.ModeNotify)
	s.Fail("/limited")
	if err := s.Add("/limited"); !errors.Is(err, watch.ErrUnsupported) {
		t.Fatalf("Add err = %v", err)
	}
	if err := s.Add("/repo/.git"); err != nil || !s.Watching("/repo/.git") {
		t.Fatalf("Add = %v", err)
	}
	s.Emit("/repo/.git/HEAD", watch.Modified)
	if c := <-s.Changes(); c.Path != "/repo/.git/HEAD" || c.Kind != watch.Modified || c.Time.IsZero() {
		t.Fatalf("change = %+v", c)
	}
	_ = s.Remove("/repo/.git")
	_ = s.Close()
	if _, ok := <-s.Changes(); ok || s.Watching("/repo/.git") {
		t.Fatal("source not closed")
	}
}
