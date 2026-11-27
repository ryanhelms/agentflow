package worktree

import (
	"os/exec"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
)

// A worktree in the middle of a rebase or a bisect shows a detached HEAD, but
// git still counts the branch as in use there. Finish must not delete it, or
// `git rebase --continue` and `git bisect reset` fail afterwards.
func TestFinishKeepsABranchAnotherWorktreeIsUsing(t *testing.T) {
	for name, start := range map[string][]string{
		"rebase": {"-c", "core.editor=true", "rebase", "--exec", "false", "HEAD~2"},
		"bisect": {"bisect", "start", "HEAD", "HEAD~2"},
	} {
		t.Run(name, func(t *testing.T) {
			_, primary := fixture(t)
			s := Resolve(&config.Repo{}, &config.User{})
			branch := "issue-" + name
			dir := addWorktree(t, s, primary, branch)
			commitFile(t, dir, "a.go")
			commitFile(t, dir, "b.go")
			git(t, dir, "push", "-q", "-u", "origin", branch)
			head := git(t, dir, "rev-parse", "HEAD")
			squashMerge(t, primary, branch)
			runGit(dir, start...) // a stopped rebase exits non-zero
			if got := git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
				t.Fatalf("%s did not detach HEAD: %s", name, got)
			}
			opt := Options{Protected: config.DefaultProtected, GH: fakeGH(map[string]string{branch: mergedPR(head)})}
			plan, err := PlanFinish(t.Context(), primary, branch, opt)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if _, err := Apply(t.Context(), plan); err == nil {
				t.Fatal("apply deleted a branch that a worktree is using")
			}
			if got := git(t, primary, "rev-parse", "refs/heads/"+branch); got != head {
				t.Fatalf("local branch at %q, want %s", got, head)
			}
		})
	}
}

// runGit runs git and ignores its exit status.
func runGit(dir string, args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	_ = cmd.Run()
}
