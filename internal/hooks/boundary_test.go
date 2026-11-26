package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/boundary"
	"github.com/nscaledev/agentflow/internal/worktree"
)

// twoRepos makes a home repository (agent-commander, with a linked worktree)
// and another repository (agentflow) side by side, and starts the session in home.
func twoRepos(t *testing.T) (home, wt, other string) {
	t.Helper()
	gitEnv(t)
	root := t.TempDir()
	home, wt, other = filepath.Join(root, "agent-commander"), filepath.Join(root, "wt"), filepath.Join(root, "agentflow")
	for dir, slug := range map[string]string{home: "ryanhelms/agent-commander", other: "ryanhelms/agentflow"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "init", "-q", "-b", "develop")
		runGit(t, dir, "remote", "add", "origin", "https://github.com/"+slug+".git")
		runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	}
	runGit(t, home, "worktree", "add", "-q", "-b", "issue-1", wt)
	if err := os.MkdirAll(filepath.Join(home, ".agentflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".agentflow", "config.yml"), []byte("policy: {protected_branches: [main, develop], coordinator_merge: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", home)
	t.Setenv(boundary.SessionEnv, "")
	t.Setenv("GH_REPO", "")
	t.Chdir(home)
	return home, wt, other
}

func TestPreBashRepoBoundary(t *testing.T) {
	home, wt, other := twoRepos(t)
	MergeGH = func(context.Context, string, ...string) (string, error) { return "develop\n", nil }
	t.Cleanup(func() { MergeGH = worktree.ExecGH })
	input := func(cmd, cwd string) string {
		b, _ := json.Marshal(map[string]any{"cwd": cwd, "tool_input": map[string]string{"command": cmd}})
		return string(b)
	}
	cases := []struct {
		cmd, cwd string
		want     int
	}{
		// the incident: coordinator_merge must not reach another repository
		{"gh pr merge 5 -R ryanhelms/agentflow --squash", home, 2},
		{"gh pr merge https://github.com/ryanhelms/agentflow/pull/5", home, 2},
		{"git -C " + other + " commit --allow-empty -m 'feat: x'", home, 2},
		{"cd " + other + " && git push origin issue-1", home, 2},
		{"gh pr close 3", other, 2}, // the session's folder moved into another repository
		// allowed
		{"gh pr merge 5 --squash", home, 0},
		{"gh pr merge 5 -R ryanhelms/agent-commander --squash", home, 0},
		{"git -C " + other + " log --oneline", home, 0},
		{"gh pr view 5 -R ryanhelms/agentflow", home, 0},
		{"git commit --allow-empty -m 'feat: add x'", wt, 0},
		{"cd " + wt + " && git push origin issue-1", home, 0},
	}
	for _, conventionOnly := range []bool{false, true} {
		for _, c := range cases {
			var stderr bytes.Buffer
			got := exitCode(PreBash(t.Context(), strings.NewReader(input(c.cmd, c.cwd)), &stderr, conventionOnly))
			if got != c.want {
				t.Errorf("convention-only=%v %s (in %s): exit %d want %d: %s", conventionOnly, c.cmd, c.cwd, got, c.want, stderr.String())
			}
			if c.want == 2 && !strings.Contains(stderr.String(), "agentflow delegate --repo agentflow") {
				t.Errorf("%s: refusal does not say how to delegate: %s", c.cmd, stderr.String())
			}
		}
	}
}

func TestMergeBaseUsesThePRRepository(t *testing.T) {
	var asked []string
	MergeGH = func(_ context.Context, _ string, args ...string) (string, error) {
		asked = append(asked, strings.Join(args, " "))
		return "main\n", nil
	}
	t.Cleanup(func() { MergeGH = worktree.ExecGH })
	t.Setenv("GH_REPO", "")
	for cmd, want := range map[string]string{
		"gh pr merge 5 -R ryanhelms/agentflow --squash":    "pr view 5 --repo ryanhelms/agentflow --json baseRefName --jq .baseRefName",
		"gh pr merge --repo=ryanhelms/agentflow 5":         "pr view 5 --repo ryanhelms/agentflow --json baseRefName --jq .baseRefName",
		"GH_REPO=ryanhelms/agentflow gh pr merge 5":        "pr view 5 --repo ryanhelms/agentflow --json baseRefName --jq .baseRefName",
		"gh pr merge https://github.com/o/r/pull/3 -R o/r": "pr view https://github.com/o/r/pull/3 --json baseRefName --jq .baseRefName",
		"gh pr merge 12 --squash":                          "pr view 12 --json baseRefName --jq .baseRefName",
	} {
		asked = nil
		base, err := MergeBase(t.Context(), t.TempDir(), cmd)
		if err != nil || base != "main" {
			t.Fatalf("%s: %q %v", cmd, base, err)
		}
		if !slices.Contains(asked, want) {
			t.Errorf("%s: asked %v want %q", cmd, asked, want)
		}
	}
}

func TestGitHooksRefuseOtherRepository(t *testing.T) {
	home, wt, other := twoRepos(t)
	homeRepo, err := boundary.ResolveDir(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv(boundary.SessionEnv, homeRepo.CommonDir)
	push := "refs/heads/issue-1 " + strings.Repeat("1", 40) + " refs/heads/issue-1 " + strings.Repeat("0", 40) + "\n"

	t.Chdir(other)
	var stderr bytes.Buffer
	if got := exitCode(PrePush(t.Context(), strings.NewReader(push), &stderr)); got != 1 || !strings.Contains(stderr.String(), "agentflow delegate") {
		t.Errorf("push from another repository: exit %d %s", got, stderr.String())
	}
	t.Chdir(wt)
	stderr.Reset()
	if got := exitCode(PrePush(t.Context(), strings.NewReader(push), &stderr)); got != 0 {
		t.Errorf("push from a home worktree: exit %d %s", got, stderr.String())
	}
	// a push to a URL that is not one of the repository's remotes
	stderr.Reset()
	if got := exitCode(PrePush(t.Context(), strings.NewReader(push), &stderr, "https://github.com/ryanhelms/agentflow.git", "https://github.com/ryanhelms/agentflow.git")); got != 1 {
		t.Errorf("push to another repository's URL: exit %d %s", got, stderr.String())
	}
	stderr.Reset()
	if got := exitCode(PrePush(t.Context(), strings.NewReader(push), &stderr, "origin", "https://github.com/ryanhelms/agent-commander.git")); got != 0 {
		t.Errorf("push to origin: exit %d %s", got, stderr.String())
	}
}

func TestSessionStartExportsSessionRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	home, wt, _ := twoRepos(t)
	envFile := filepath.Join(t.TempDir(), "env")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Setenv("CLAUDE_PROJECT_DIR", wt)
	t.Chdir(wt)
	var out bytes.Buffer
	if err := SessionStart(t.Context(), "claude", "", &out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(envFile)
	want, _ := boundary.ResolveDir(t.Context(), home)
	if err != nil || !strings.Contains(string(b), "export "+boundary.SessionEnv+"='"+want.CommonDir+"'") {
		t.Fatalf("env file = %q %v", b, err)
	}
}
