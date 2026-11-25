package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func commitFixture(t *testing.T) *Repo {
	t.Helper()
	r := &Repo{Dir: t.TempDir()}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Developer"}, {"config", "user.email", "developer@example.test"}, {"commit", "-q", "--allow-empty", "-m", "initial"}} {
		if _, err := r.Git(t.Context(), args...); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestExactCommitSnapshotAndAtomicSelection(t *testing.T) {
	r := commitFixture(t)
	ctx := t.Context()
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(r.Dir, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := r.Git(ctx, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	write("existing", "pre-staged")
	git("add", "existing")
	write("new", "reviewed")
	idx := git("write-tree")
	selected := []string{"new"}
	candidate, err := r.PreviewCommit(ctx, selected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(candidate.Files, ",") != "new" || git("write-tree") != idx {
		t.Fatal("preview included unrelated changes or modified the real index")
	}
	write("new", "changed")
	if _, err = r.CommitExact(ctx, candidate, "message", selected); err == nil {
		t.Fatal("allowed changed content at same path")
	}
	if got := git("write-tree"); got != idx {
		t.Fatalf("failed attempt changed index: %s", got)
	}
	write("new", "reviewed")
	if _, err = r.CommitExact(ctx, candidate, "message", selected); err != nil {
		t.Fatal(err)
	}
	if got := git("show", "HEAD:new"); got != "reviewed" {
		t.Fatalf("committed %q", got)
	}
	if got := git("diff", "--cached", "--name-only"); got != "existing" {
		t.Fatalf("lost staged file: %q", got)
	}
	if _, err = r.CommitExact(ctx, candidate, "message", selected); err == nil {
		t.Fatal("repeated the applied commit")
	}
}

func TestStagedSnapshotUsesIndexAndGuardsHooks(t *testing.T) {
	r := commitFixture(t)
	ctx := t.Context()
	path := filepath.Join(r.Dir, "file")
	if err := os.WriteFile(path, []byte("approved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(ctx, "file"); err != nil {
		t.Fatal(err)
	}
	candidate, err := r.PreviewCommit(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("unstaged and different size"), 0600); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(r.Dir, ".git", "hooks", "pre-commit")
	if err = os.WriteFile(hook, []byte("#!/bin/sh\ngit add -- file\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = r.CommitExact(ctx, candidate, "content", nil); err == nil {
		out, _ := r.Git(ctx, "show", "HEAD:file")
		t.Fatalf("commit unexpectedly succeeded with content %q", out)
	}
	head, err := r.Git(ctx, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != candidate.Parent {
		t.Fatalf("moved original branch: %s %v", head, err)
	}
	if err = os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if _, err = r.CommitExact(ctx, candidate, "content", nil); err != nil {
		t.Fatal(err)
	}
	body, err := r.Git(ctx, "show", "HEAD:file")
	if err != nil || body != "approved" {
		t.Fatalf("committed %q %v", body, err)
	}
}

func TestExactCommitBindsParentBranchAndRepository(t *testing.T) {
	r := commitFixture(t)
	ctx := t.Context()
	write := func(r *Repo) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(r.Dir, "file"), []byte("change"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := r.Add(ctx, "file"); err != nil {
			t.Fatal(err)
		}
	}
	write(r)
	s, err := r.PreviewCommit(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Git(ctx, "switch", "-c", "other"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.CommitExact(ctx, s, "content", nil); err == nil {
		t.Fatal("accepted another branch")
	}
	if _, err = r.Git(ctx, "switch", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Git(ctx, "commit", "--allow-empty", "--only", "-m", "another parent"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.CommitExact(ctx, s, "content", nil); err == nil {
		t.Fatal("accepted another parent")
	}
	other := commitFixture(t)
	write(other)
	if _, err = other.CommitExact(ctx, s, "content", nil); err == nil {
		t.Fatal("accepted another repository")
	}
}

func TestPrepareUnbornAndSelectedDeletion(t *testing.T) {
	ctx := t.Context()
	r := &Repo{Dir: t.TempDir()}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Developer"}, {"config", "user.email", "developer@example.test"}} {
		if _, err := r.Git(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(r.Dir, "file with spaces")
	if err := os.WriteFile(path, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	paths := []string{"file with spaces"}
	s, err := r.PreviewCommit(ctx, paths)
	if err != nil || s.Parent != "" {
		t.Fatalf("unborn preview: %+v %v", s, err)
	}
	if _, err := r.CommitExact(ctx, s, "first", paths); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s, err = r.PreviewCommit(ctx, paths)
	if err != nil || len(s.Files) != 1 || s.Files[0] != paths[0] {
		t.Fatalf("deletion: %+v %v", s, err)
	}
	if _, err := r.CommitExact(ctx, s, "delete", paths); err != nil {
		t.Fatal(err)
	}
}

func TestCommitMessageHookCannotReplaceApprovedMessage(t *testing.T) {
	r := commitFixture(t)
	ctx := t.Context()
	if err := os.WriteFile(filepath.Join(r.Dir, "f"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(ctx, "f"); err != nil {
		t.Fatal(err)
	}
	s, err := r.PreviewCommit(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(r.Dir, ".git", "hooks", "commit-msg")
	if err = os.WriteFile(hook, []byte("#!/bin/sh\necho different > \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = r.CommitExact(ctx, s, "approved message", nil); err == nil {
		t.Fatal("allowed a different commit message")
	}
}

func TestCleanCommitMessageMatchesNoninteractiveGit(t *testing.T) {
	r := commitFixture(t)
	for _, tt := range []struct{ mode, input, want string }{
		{"default", "summary  \n\n# comment\n\n", "summary\n\n# comment\n"},
		{"strip", "summary  \n\n# comment\n\n", "summary\n"},
		{"verbatim", "summary  ", "summary  "},
	} {
		if _, err := r.Git(t.Context(), "config", "commit.cleanup", tt.mode); err != nil {
			t.Fatal(err)
		}
		got, err := r.CleanCommitMessage(t.Context(), tt.input)
		if err != nil || got != tt.want {
			t.Errorf("%s: got %q, %v", tt.mode, got, err)
		}
	}
}
