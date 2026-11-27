package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
)

// otherClone clones origin elsewhere, as another machine or session would.
func otherClone(t *testing.T, primary string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "elsewhere")
	git(t, primary, "clone", "-q", git(t, primary, "remote", "get-url", "origin"), dir)
	return dir
}

// pushFrom commits name on branch in clone and pushes it, without the primary
// checkout fetching it.
func pushFrom(t *testing.T, clone, branch, name string) string {
	t.Helper()
	git(t, clone, "fetch", "-q", "origin")
	git(t, clone, "checkout", "-q", "-B", branch, "origin/"+branch)
	commitFile(t, clone, name)
	git(t, clone, "push", "-q", "origin", branch)
	return git(t, clone, "rev-parse", "HEAD")
}

func remoteTipOf(t *testing.T, primary, branch string) string {
	t.Helper()
	out := git(t, primary, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if f := strings.Fields(out); len(f) > 0 {
		return f[0]
	}
	return ""
}

func wantRefused(t *testing.T, err error, detail string) {
	t.Helper()
	var refused *RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Outcome.Detail, detail) {
		t.Fatalf("got %v, want a refusal with %q", err, detail)
	}
}

func mergedPR(head string) string {
	return `{"state":"MERGED","mergedAt":"x","baseRefName":"develop","headRefOid":"` + head + `"}`
}

// Without a PR (gh failed), a branch is judged merged by its local tip. Commits
// that only the remote branch holds must not be deleted with it.
func TestFinishWithoutPRKeepsRemoteOnlyCommits(t *testing.T) {
	_, primary := fixture(t)
	s := Resolve(&config.Repo{}, &config.User{})
	dir := addWorktree(t, s, primary, "issue-4")
	commitFile(t, dir, "a.go")
	git(t, dir, "push", "-q", "-u", "origin", "issue-4")
	git(t, primary, "fetch", "-q", "origin")
	git(t, primary, "push", "-q", "origin", "origin/issue-4:refs/heads/develop") // merged by fast-forward
	later := pushFrom(t, otherClone(t, primary), "issue-4", "b.go")
	opt := Options{Protected: config.DefaultProtected, GH: fakeGH(nil)}
	_, err := PlanFinish(t.Context(), dir, "", opt)
	wantRefused(t, err, "origin/issue-4 has commits that are not in origin/develop")
	res, err := PlanSweep(t.Context(), []string{primary}, opt)
	if err != nil || len(res.Plans()) != 0 {
		t.Fatalf("sweep planned %d finishes: %v", len(res.Plans()), err)
	}
	if got := remoteTipOf(t, primary, "issue-4"); got != later {
		t.Fatalf("remote branch at %s, want %s", got, later)
	}
}

// A merged PR that reports no head commit gives nothing to compare the tips
// with, so finish refuses.
func TestFinishRefusesMergedPRWithoutHead(t *testing.T) {
	_, primary := fixture(t)
	s := Resolve(&config.Repo{}, &config.User{})
	dir := addWorktree(t, s, primary, "issue-5")
	commitFile(t, dir, "a.go")
	git(t, dir, "push", "-q", "-u", "origin", "issue-5")
	squashMerge(t, primary, "issue-5")
	commitFile(t, dir, "b.go") // pushed after the merge: on origin, so not "unpushed"
	git(t, dir, "push", "-q", "origin", "issue-5")
	opt := Options{Protected: config.DefaultProtected, GH: fakeGH(map[string]string{
		"issue-5": `{"state":"MERGED","mergedAt":"x","baseRefName":"develop"}`,
	})}
	_, err := PlanFinish(t.Context(), dir, "", opt)
	wantRefused(t, err, "no head commit")
}

// When origin cannot be read, the plan falls back to the last fetched remote
// branch. The delete must still fail if the branch has moved on since.
func TestFinishOfflineRemoteDeleteNeedsTheCheckedTip(t *testing.T) {
	home, primary := fixture(t)
	s := Resolve(&config.Repo{}, &config.User{})
	dir := addWorktree(t, s, primary, "issue-6")
	commitFile(t, dir, "a.go")
	git(t, dir, "push", "-q", "-u", "origin", "issue-6")
	head := git(t, dir, "rev-parse", "HEAD")
	squashMerge(t, primary, "issue-6")
	later := pushFrom(t, otherClone(t, primary), "issue-6", "b.go")
	opt := Options{Protected: config.DefaultProtected, GH: fakeGH(map[string]string{"issue-6": mergedPR(head)})}

	bare := filepath.Join(home, "origin", "app.git")
	if err := os.Rename(bare, bare+".away"); err != nil { // ls-remote and fetch fail
		t.Fatal(err)
	}
	plan, err := PlanFinish(t.Context(), dir, "", opt)
	if err := os.Rename(bare+".away", bare); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatalf("plan with origin unreachable: %v", err)
	}
	if _, err := Apply(t.Context(), plan); err == nil {
		t.Fatal("apply deleted a remote branch it never checked")
	}
	if got := remoteTipOf(t, primary, "issue-6"); got != later {
		t.Fatalf("remote branch at %q, want %s", got, later)
	}
}

// Work that reaches the branch between the plan and Apply, as during a
// confirmation or a long sweep, stops Apply before it deletes anything.
func TestFinishApplyKeepsWorkAddedAfterThePlan(t *testing.T) {
	_, primary := fixture(t)
	s := Resolve(&config.Repo{}, &config.User{})
	ctx := t.Context()

	// pushed to origin from elsewhere
	dir := addWorktree(t, s, primary, "issue-7")
	commitFile(t, dir, "a.go")
	git(t, dir, "push", "-q", "-u", "origin", "issue-7")
	head := git(t, dir, "rev-parse", "HEAD")
	squashMerge(t, primary, "issue-7")
	opt := Options{Protected: config.DefaultProtected, GH: fakeGH(map[string]string{"issue-7": mergedPR(head)})}
	plan, err := PlanFinish(ctx, dir, "", opt)
	if err != nil {
		t.Fatal(err)
	}
	later := pushFrom(t, otherClone(t, primary), "issue-7", "b.go")
	if _, err := Apply(ctx, plan); err == nil {
		t.Fatal("apply deleted a remote branch that moved after the plan")
	}
	if got := remoteTipOf(t, primary, "issue-7"); got != later {
		t.Fatalf("remote branch at %q, want %s", got, later)
	}

	// committed in the worktree
	dir = addWorktree(t, s, primary, "issue-8")
	commitFile(t, dir, "c.go")
	git(t, dir, "push", "-q", "-u", "origin", "issue-8")
	head = git(t, dir, "rev-parse", "HEAD")
	squashMerge(t, primary, "issue-8")
	opt.GH = fakeGH(map[string]string{"issue-8": mergedPR(head)})
	plan, err = PlanFinish(ctx, dir, "", opt)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, dir, "d.go")
	local := git(t, dir, "rev-parse", "HEAD")
	if _, err := Apply(ctx, plan); err == nil {
		t.Fatal("apply deleted a local branch that moved after the plan")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("apply removed the worktree: %v", err)
	}
	if got := git(t, primary, "rev-parse", "refs/heads/issue-8"); got != local {
		t.Fatalf("local branch at %s, want %s", got, local)
	}
}

// Sweep reports why a merged worktree is kept.
func TestSweepShowsCommitsAfterTheMergedPR(t *testing.T) {
	_, primary := fixture(t)
	s := Resolve(&config.Repo{}, &config.User{})
	dir := addWorktree(t, s, primary, "issue-9")
	commitFile(t, dir, "a.go")
	git(t, dir, "push", "-q", "-u", "origin", "issue-9")
	head := git(t, dir, "rev-parse", "HEAD")
	squashMerge(t, primary, "issue-9")
	commitFile(t, dir, "b.go")
	git(t, dir, "push", "-q", "origin", "issue-9")
	res, err := PlanSweep(t.Context(), []string{primary}, Options{Protected: config.DefaultProtected, GH: fakeGH(map[string]string{"issue-9": mergedPR(head)})})
	if err != nil || len(res.Plans()) != 0 || len(res.Worktrees) != 1 {
		t.Fatalf("sweep %+v %v", res, err)
	}
	if o := res.Worktrees[0]; o.State != StateActive || !strings.Contains(o.Detail, "after the merged PR head") {
		t.Fatalf("sweep reports %+v", o)
	}
}
