package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func stageHookFixture(t *testing.T, r *Repo) *CommitTree {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.Dir, "candidate"), []byte("approved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(t.Context(), "candidate"); err != nil {
		t.Fatal(err)
	}
	candidate, err := r.PreviewCommit(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func TestExactCommitPreservesConfiguredHooks(t *testing.T) {
	r := commitFixture(t)
	ctx := t.Context()
	hooks := filepath.Join(t.TempDir(), "team hooks")
	if err := os.Mkdir(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "events")
	t.Setenv("AGENTFLOW_TEST_HOOK_LOG", filepath.ToSlash(log))
	const script = `#!/bin/sh
name=${0##*/}
printf '%s|%s\n' "$name" "$*" >> "$AGENTFLOW_TEST_HOOK_LOG"
if [ "$name" = reference-transaction ]; then cat >> "$AGENTFLOW_TEST_HOOK_LOG"; fi
`
	for _, name := range []string{"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit", "reference-transaction"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Git(ctx, "config", "core.hooksPath", filepath.ToSlash(hooks)); err != nil {
		t.Fatal(err)
	}
	candidate := stageHookFixture(t, r)
	if _, err := r.CommitExact(ctx, candidate, "approved\n", nil); err != nil {
		t.Fatal(err)
	}
	events, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pre-commit|\n", "prepare-commit-msg|", "commit-msg|", "post-commit|\n", "reference-transaction|prepared\n", " refs/heads/main\n", "reference-transaction|committed\n"} {
		if !strings.Contains(string(events), want) {
			t.Fatalf("missing %q in %q", want, events)
		}
	}
}

func TestExactCommitRespectsDisabledHooks(t *testing.T) {
	r := commitFixture(t)
	ctx := t.Context()
	// A non-directory hooks path disables traditional hooks on every platform.
	disabled := filepath.Join(t.TempDir(), "disabled")
	if err := os.WriteFile(disabled, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Git(ctx, "config", "core.hooksPath", filepath.ToSlash(disabled)); err != nil {
		t.Fatal(err)
	}
	candidate := stageHookFixture(t, r)
	if _, err := r.Git(ctx, "config", "hook.reference-transaction.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PreviewCommit(ctx, nil); err == nil || !strings.Contains(err.Error(), "event to be enabled") {
		t.Fatalf("disabled guard preview: %v", err)
	}
	if _, err := r.CommitExact(ctx, candidate, "approved\n", nil); err == nil || !strings.Contains(err.Error(), "event to be enabled") {
		t.Fatalf("disabled guard apply: %v", err)
	}
	head, err := r.Git(ctx, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != candidate.Parent {
		t.Fatalf("unexpected HEAD %q, %v", head, err)
	}
	if _, err := r.Git(ctx, "config", "--unset", "hook.reference-transaction.enabled"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitExact(ctx, candidate, "approved\n", nil); err != nil {
		t.Fatal(err)
	}
}

func TestGuardHookDiscovery(t *testing.T) {
	original := t.TempDir()
	for _, name := range []string{"commit-msg", "commit-msg.exe", "pre-commit.exe", "reference-transaction.exe"} {
		if err := os.WriteFile(filepath.Join(original, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	guard, err := (&commitIndex{dir: t.TempDir()}).guardHooks(original, true)
	if err != nil {
		t.Fatal(err)
	}
	for name, suffix := range map[string]string{"commit-msg": "", "pre-commit": ".exe", "reference-transaction": ".exe"} {
		b, err := os.ReadFile(filepath.Join(guard, name))
		if err != nil || !strings.Contains(string(b), `"$AGENTFLOW_ORIGINAL_HOOKS/${0##*/}`+suffix+`"`) {
			t.Fatalf("wrong Windows hook target for %s: %s, %v", name, b, err)
		}
	}
	if runtime.GOOS != "windows" {
		unix, err := (&commitIndex{dir: t.TempDir()}).guardHooks(original, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(unix, "commit-msg")); !os.IsNotExist(err) {
			t.Fatalf("non-executable POSIX hook was forwarded: %v", err)
		}
	}
}

func TestExactCommitWindowsNativeHooks(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires native Git for Windows")
	}
	r := commitFixture(t)
	ctx := t.Context()
	hooks := t.TempDir()
	source := filepath.Join(t.TempDir(), "fixture.go")
	const program = `package main
import ("fmt"; "os")
func main() { fmt.Fprintln(os.Stderr, "native hook refused the candidate"); os.Exit(1) }
`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(hooks, "pre-commit.exe")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, source).CombinedOutput(); err != nil {
		t.Fatalf("build hook: %s, %v", out, err)
	}
	if _, err := r.Git(ctx, "config", "core.hooksPath", filepath.ToSlash(hooks)); err != nil {
		t.Fatal(err)
	}
	candidate := stageHookFixture(t, r)
	for _, name := range []string{"pre-commit", "reference-transaction"} {
		if name == "reference-transaction" {
			if err := os.Rename(bin, filepath.Join(hooks, name+".exe")); err != nil {
				t.Fatal(err)
			}
		}
		_, err := r.CommitExact(ctx, candidate, "approved\n", nil)
		if err == nil || !strings.Contains(err.Error(), "native hook refused the candidate") {
			t.Fatalf("%s: %v", name, err)
		}
		head, err := r.Git(ctx, "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(head) != candidate.Parent {
			t.Fatalf("%s: unexpected HEAD %q, %v", name, head, err)
		}
	}
}
