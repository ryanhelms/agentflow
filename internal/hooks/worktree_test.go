package hooks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/worktree"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func gitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "Dev", "GIT_AUTHOR_EMAIL": "dev@example.com", "GIT_COMMITTER_NAME": "Dev", "GIT_COMMITTER_EMAIL": "dev@example.com"} {
		t.Setenv(k, v)
	}
}

func exitCode(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	if err != nil {
		return -1
	}
	return 0
}

func TestCoordinatorMerge(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	gitEnv(t)
	runGit(t, dir, "init", "-q", "-b", "develop")
	write := func(config string) {
		if err := os.MkdirAll(".agentflow", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(".agentflow/config.yml", []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bases := map[string]string{"12": "develop", "13": "main"}
	var asked []string
	MergeGH = func(_ context.Context, _ string, args ...string) (string, error) {
		asked = append(asked, strings.Join(args, " "))
		if slices.Contains(args, "--repo") {
			return "main\n", nil // PR 12 of another repository targets main
		}
		if len(args) > 2 {
			if base, ok := bases[args[2]]; ok {
				return base + "\n", nil
			}
		}
		return "", errors.New("no PR")
	}
	t.Cleanup(func() { MergeGH = worktree.ExecGH })
	cmd := func(c string) string { return `{"tool_input":{"command":"` + c + `"}}` }
	check := func(command string, conventionOnly bool) int {
		var stderr bytes.Buffer
		return exitCode(PreBash(t.Context(), strings.NewReader(cmd(command)), &stderr, conventionOnly))
	}

	write("policy: {protected_branches: [main, develop]}\n")
	if got := check("gh pr merge 12 --squash", false); got != 2 {
		t.Fatalf("merge without coordinator_merge: exit %d", got)
	}
	write("policy: {protected_branches: [main, develop], coordinator_merge: true}\n")
	for command, want := range map[string]int{
		"gh pr merge 12 --squash --delete-branch":         0,
		"gh pr merge --squash --subject 'feat: add x' 12": 0,
		"gh pr merge 13 --merge":                          2,
		"gh pr merge 99 --squash":                         2, // base unknown
		"gh pr merge 12 --squash && git push":             2,
		"git push --force origin feature":                 2,
		"git push origin HEAD:develop":                    2,
		"gh pr merge 12 --squash; rm -rf /tmp/xyz":        2,
		"gh pr merge 12 --squash\\ngh pr merge 13":         2, // newline chains a second merge
		"gh pr merge 12 --squash -R o/other":              2,
		"gh pr merge 12 --squash --repo=o/other":          2,
		"gh pr create --title 'feat: add x' --body":       0,
	} {
		if got := check(command, false); got != want {
			t.Errorf("%s: exit %d want %d", command, got, want)
		}
	}
	if !slices.Contains(asked, "pr view 12 --json baseRefName --jq .baseRefName") {
		t.Fatalf("gh calls %v", asked)
	}
	if got := check("gh pr merge 13 --merge", true); got != 0 {
		t.Fatalf("--convention-only blocked a merge: exit %d", got)
	}
}

func TestMergeTarget(t *testing.T) {
	for cmd, want := range map[string]string{
		"gh pr merge 12 --squash":                           "12",
		"gh pr merge --squash -t \"feat: x\" feat-1":        "feat-1",
		"gh pr merge --auto --squash":                       "",
		"cd x && gh pr merge https://github.com/o/r/pull/3": "https://github.com/o/r/pull/3",
	} {
		if got := mergeTarget(cmd); got != want {
			t.Errorf("mergeTarget(%q) = %q want %q", cmd, got, want)
		}
	}
}

func TestPrePush(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	gitEnv(t)
	runGit(t, dir, "init", "-q")
	zero := strings.Repeat("0", 40)
	line := func(ref string) string {
		return "refs/heads/x " + strings.Repeat("1", 40) + " " + ref + " " + zero + "\n"
	}
	cases := []struct {
		in      string
		promote bool
		want    int
	}{
		{line("refs/heads/feature"), false, 0},
		{line("refs/heads/develop"), false, 1},
		{line("refs/heads/release/1.2"), false, 1},
		{line("refs/heads/main"), false, 1},
		{line("refs/heads/main"), true, 0},
		{line("refs/heads/develop"), true, 1},
		{line("refs/tags/v1.0.0"), false, 0},
	}
	for _, c := range cases {
		t.Setenv("AGENTFLOW_ALLOW_PROMOTE", map[bool]string{true: "1", false: ""}[c.promote])
		var stderr bytes.Buffer
		if got := exitCode(PrePush(t.Context(), strings.NewReader(c.in), &stderr)); got != c.want {
			t.Errorf("%q promote=%t: exit %d want %d (%s)", c.in, c.promote, got, c.want, stderr.String())
		}
	}
}

// The template hooks must work without agentflow installed.
func TestTemplateHooksWithoutAgentflow(t *testing.T) {
	gitEnv(t)
	hooksDir, err := filepath.Abs("../../templates/repo/.githooks")
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("PATH", filepath.Dir(gitPath)+":/usr/bin:/bin")
	t.Setenv("AGENTFLOW_ALLOW_PRIMARY", "")
	t.Setenv("AGENTFLOW_ALLOW_PROMOTE", "")
	root := t.TempDir()
	primary := filepath.Join(root, "app")
	runGit(t, root, "init", "-q", "-b", "main", primary)
	if err := os.MkdirAll(filepath.Join(primary, ".agentflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "policy:\n  protected_branches:\n    - main\n    - \"release/*\"\n  test_cmd: make test\n"
	if err := os.WriteFile(filepath.Join(primary, ".agentflow", "config.yml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(dir, hook, stdin string, env ...string) int {
		cmd := exec.Command("bash", filepath.Join(hooksDir, hook), "origin", "url")
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		if err != nil {
			t.Fatalf("%s: %v %s", hook, err, out)
		}
		return 0
	}
	// No commits yet: the primary checkout may commit.
	if got := run(primary, "pre-commit", ""); got != 0 {
		t.Fatalf("pre-commit before the first commit: %d", got)
	}
	runGit(t, primary, "commit", "-q", "--allow-empty", "-m", "chore: init")
	if got := run(primary, "pre-commit", ""); got != 1 {
		t.Fatalf("pre-commit in the primary checkout: %d", got)
	}
	if got := run(primary, "pre-commit", "", "AGENTFLOW_ALLOW_PRIMARY=1"); got != 0 {
		t.Fatalf("pre-commit bypass: %d", got)
	}
	linked := filepath.Join(root, "wt")
	runGit(t, primary, "worktree", "add", "-q", "-b", "feature", linked)
	if got := run(linked, "pre-commit", ""); got != 0 {
		t.Fatalf("pre-commit in a linked worktree: %d", got)
	}
	// A session that belongs to another repository may not commit or push here.
	other := filepath.Join(root, "other")
	runGit(t, root, "init", "-q", "-b", "main", other)
	for _, session := range []string{other, filepath.Join(other, ".git")} {
		if got := run(linked, "pre-commit", "", "AGENTFLOW_SESSION_REPO="+session); got != 1 {
			t.Errorf("pre-commit from a session of another repository (%s): %d", session, got)
		}
		if got := run(linked, "pre-push", "refs/heads/feature abc refs/heads/feature def\n", "AGENTFLOW_SESSION_REPO="+session); got != 1 {
			t.Errorf("pre-push from a session of another repository (%s): %d", session, got)
		}
	}
	for _, session := range []string{primary, filepath.Join(primary, ".git"), linked} {
		if got := run(linked, "pre-commit", "", "AGENTFLOW_SESSION_REPO="+session); got != 0 {
			t.Errorf("pre-commit from this repository's session (%s): %d", session, got)
		}
	}
	push := func(ref string) string { return "refs/heads/feature abc " + ref + " def\n" }
	for ref, want := range map[string]int{"refs/heads/feature": 0, "refs/heads/main": 1, "refs/heads/release/2.0": 1, "refs/heads/develop": 0} {
		if got := run(primary, "pre-push", push(ref)); got != want {
			t.Errorf("pre-push %s: exit %d want %d", ref, got, want)
		}
	}
	if got := run(primary, "pre-push", push("refs/heads/main"), "AGENTFLOW_ALLOW_PROMOTE=1"); got != 0 {
		t.Errorf("promotion bypass: exit %d", got)
	}
	if got := run(primary, "pre-push", push("refs/heads/release/2.0"), "AGENTFLOW_ALLOW_PROMOTE=1"); got != 1 {
		t.Errorf("promotion bypass must not cover release/*: exit %d", got)
	}
}

func TestSessionStartWorktreeNotes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	gitEnv(t)
	primary := filepath.Join(home, "GitHub", "app")
	runGit(t, home, "init", "-q", "-b", "develop", primary)
	if err := os.MkdirAll(filepath.Join(primary, ".agentflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primary, ".agentflow", "config.yml"), []byte("task_management: {provider: none}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, primary, "add", ".")
	runGit(t, primary, "commit", "-q", "-m", "chore: init")
	linked := filepath.Join(home, ".worktrees", "app", "wip")
	runGit(t, primary, "worktree", "add", "-q", "-b", "wip", linked)
	if err := os.WriteFile(filepath.Join(linked, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(primary)
	var out bytes.Buffer
	if err := SessionStart(t.Context(), "claude", "", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "primary checkout of app") || !strings.Contains(out.String(), "agentflow start <issue> --worktree") ||
		!strings.Contains(out.String(), "1 dirty") {
		t.Fatalf("session context = %s", out.String())
	}
	t.Chdir(linked)
	out.Reset()
	if err := SessionStart(t.Context(), "claude", "", &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "primary checkout") {
		t.Fatalf("linked worktree got the primary note: %s", out.String())
	}
}
