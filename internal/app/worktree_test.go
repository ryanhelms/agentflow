package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/gitx"
	"github.com/nscaledev/agentflow/internal/worktree"
)

func TestStartWorktreeRecordsTaskAndWritesGoWork(t *testing.T) {
	a, _ := issueApp(t)
	ctx := t.Context()
	root := t.TempDir()
	r := &gitx.Repo{Dir: a.Repo.Dir}
	if err := os.WriteFile(filepath.Join(a.Repo.Dir, "go.mod"), []byte("module example.com/app\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, r, "add", "go.mod")
	mustGit(t, r, "commit", "-qm", "chore: add go.mod")
	mustGit(t, r, "switch", "main")
	mustGit(t, r, "merge", "-q", "feature")
	mustGit(t, r, "switch", "feature")
	sibling := filepath.Join(t.TempDir(), "acp")
	a.Cfg.Worktree = config.Worktree{Root: root, GoWork: &config.GoWork{
		Use:      []string{"."},
		Siblings: map[string]config.GoWorkSibling{"agent-acp.local/sdks/go": {Repo: "agent-acp", Path: "sdks/go"}},
	}}
	a.User.Worktree.Checkouts = map[string]string{"agent-acp": sibling}
	a.Gate.Autonomy.Remote, a.Gate.Autonomy.Local = config.Auto, config.Auto
	v, err := a.Start(ctx, "#42", true, "")
	if err != nil {
		t.Fatal(err)
	}
	res := v.(StartResult)
	if want := filepath.Join(root, worktree.RepoName(ctx, a.Repo), res.Branch); res.Worktree != want {
		t.Fatalf("worktree %s want %s", res.Worktree, want)
	}
	if !strings.Contains(res.Next, res.Worktree) {
		t.Fatalf("next = %q", res.Next)
	}
	rec, err := worktree.ReadRecord(res.Worktree)
	if err != nil || rec.Issue != "org/repo#42" || rec.Branch != res.Branch || rec.Base != "main" || rec.BaseSHA == "" || rec.Created.IsZero() {
		t.Fatalf("record %+v %v", rec, err)
	}
	b, err := os.ReadFile(filepath.Join(res.Worktree, "go.work"))
	if err != nil || !strings.Contains(string(b), "go 1.23") || !strings.Contains(string(b), "replace agent-acp.local/sdks/go => "+filepath.Join(sibling, "sdks", "go")) {
		t.Fatalf("go.work %s %v", b, err)
	}
	if out, _ := (&gitx.Repo{Dir: res.Worktree}).Git(ctx, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("worktree files are not excluded: %q", out)
	}

	// worktree go-work regenerates it from inside the worktree.
	if err := os.Remove(filepath.Join(res.Worktree, "go.work")); err != nil {
		t.Fatal(err)
	}
	wt, err := Open(ctx, res.Worktree, "test")
	if err != nil {
		t.Fatal(err)
	}
	wt.Cfg.Worktree, wt.User.Worktree = a.Cfg.Worktree, a.User.Worktree
	if out, err := wt.WorktreeGoWork(ctx); err != nil || out.(*worktree.GoWorkResult).Go != "1.23" {
		t.Fatalf("go-work %v %v", out, err)
	}
	wt.Cfg.Worktree.GoWork = nil
	if _, err := wt.WorktreeGoWork(ctx); err == nil {
		t.Fatal("go-work without configuration should fail")
	}
}

func TestFinishAsksForConfirmation(t *testing.T) {
	a, _ := issueApp(t)
	ctx := t.Context()
	a.Gate.Autonomy.Remote, a.Gate.Autonomy.Local = config.Auto, config.Auto
	a.Cfg.Worktree.Root = t.TempDir()
	v, err := a.Start(ctx, "#42", true, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := v.(StartResult).Worktree
	branch := v.(StartResult).Branch
	// Work lands on main through a merge, then the branch is finished.
	if err := os.WriteFile(filepath.Join(dir, "fix.txt"), []byte("fix"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &gitx.Repo{Dir: dir}
	mustGit(t, w, "add", "fix.txt")
	mustGit(t, w, "commit", "-qm", "fix: add fix")
	mustGit(t, a.Repo, "switch", "main")
	mustGit(t, a.Repo, "merge", "-q", "--no-ff", "-m", "merge", branch)
	// The fixture has no origin; treat the local main as origin/main.
	mustGit(t, a.Repo, "update-ref", "refs/remotes/origin/main", "main")
	mustGit(t, a.Repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	opt := a.worktreeOptions()
	opt.Offline = true
	dry, err := a.finish(ctx, branch, true, "", opt)
	if err != nil || dry.(FinishResult).State != worktree.StateWouldFinish {
		t.Fatalf("dry run %+v %v", dry, err)
	}
	a.Gate.Autonomy.Remote = config.Confirm
	v, err = a.finish(ctx, branch, false, "", opt)
	if err != nil {
		t.Fatal(err)
	}
	tok := confirmation(t, v)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("finish ran before confirmation")
	}
	v, err = a.finish(ctx, branch, false, tok, opt)
	if err != nil || v.(FinishResult).State != worktree.StateFinished || !strings.Contains(v.(FinishResult).Next, a.Repo.Dir) {
		t.Fatalf("finish %+v %v", v, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("worktree remains")
	}
	v, err = a.finish(ctx, branch, false, "", opt)
	if err != nil || v.(FinishResult).State != worktree.StateAlreadyFinished {
		t.Fatalf("repeat %+v %v", v, err)
	}
}

func TestPromote(t *testing.T) {
	a, _ := issueApp(t)
	ctx := t.Context()
	a.Gate.Autonomy.Remote = config.Auto
	var calls []string
	existing := ""
	gh := func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[1] {
		case "list":
			return existing, nil
		case "create":
			return "https://github.com/o/r/pull/9\n", nil
		}
		return "", errors.New("unexpected")
	}
	t.Setenv("CLAUDECODE", "1")
	if _, err := a.Promote(ctx, "", "", false, "", gh); err == nil || !strings.Contains(err.Error(), "not an AI agent") {
		t.Fatalf("agent promote: %v", err)
	}
	t.Setenv("CLAUDECODE", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "abc")
	if AgentEnvironment() != "CODEX_THREAD_ID" {
		t.Fatalf("codex detection: %q", AgentEnvironment())
	}
	t.Setenv("AGENTFLOW_ALLOW_PROMOTE", "1")
	v, err := a.Promote(ctx, "", "", false, "", gh)
	if err != nil {
		t.Fatal(err)
	}
	res := v.(PromoteResult)
	// The fixture's default branch is main, so the integration branch defaults to develop.
	if res.Head != "develop" || res.Base != "main" || res.URL != "https://github.com/o/r/pull/9" {
		t.Fatalf("promote %+v", res)
	}
	if last := calls[len(calls)-1]; !strings.HasPrefix(last, "pr create --base main --head develop --title chore(release): promote develop to main --body") || !strings.Contains(last, "merge commit") {
		t.Fatalf("gh call %q", last)
	}
	existing = "https://github.com/o/r/pull/8\n"
	v, err = a.Promote(ctx, "", "", false, "", gh)
	if err != nil || !v.(PromoteResult).Exists || v.(PromoteResult).URL != "https://github.com/o/r/pull/8" {
		t.Fatalf("existing %+v %v", v, err)
	}
}
