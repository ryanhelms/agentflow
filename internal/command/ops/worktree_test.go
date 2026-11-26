package ops_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
)

func TestWorktreeGoWorkAuditsThroughTheInvoker(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\nworktree: {go_work: {use: ['.']}}\n", "")
	writeOpsFile(t, filepath.Join(r.dir, "go.mod"), "module example.test/app\n\ngo 1.23\n")
	before := len(r.auditLines(t))

	em, env, err := r.cli(t, false, "worktree", "go-work")
	if err != nil {
		t.Fatal(err)
	}
	evs := wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpApplied)
	wantWorktreeScope(t, evs, false)
	wantOpPayload(t, evs[0], "worktree.go-work", "worktree_go_work", "local")
	if _, err := os.Stat(filepath.Join(r.dir, "go.work")); err != nil || !strings.Contains(env.Out.String(), "go.work") {
		t.Fatalf("go.work: %v, output %q", err, env.Out.String())
	}
	// The method writes no audit line, so the Invoker writes one, with the
	// operation ID the timeline links to.
	lines := r.auditLines(t)[before:]
	if len(lines) != 1 || lines[0]["operation"] != "worktree.go-work" || lines[0]["operation_id"] != evs[0].CorrelationID ||
		lines[0]["result"] != "applied" || lines[0]["action"] != "worktree_go_work" || lines[0]["class"] != "local" {
		t.Fatalf("audit lines %v, want one for operation %s", lines, evs[0].CorrelationID)
	}

	// A refusal: the tool without a configuration to read.
	r.reopen(t, "agentflow: 1\n")
	em, out, isErr := r.tool(t, "worktree.go-work", map[string]any{})
	if !isErr || !strings.Contains(out, "worktree.go_work is not configured") {
		t.Fatalf("unconfigured go.work: %v %s", isErr, out)
	}
	evs = wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	if p, _ := commandtest.Payload(evs[1]); p.Code != "error" || strings.Contains(string(evs[1].Payload), "not configured") {
		t.Errorf("failure payload %s", evs[1].Payload)
	}
	lines = r.auditLines(t)[before+1:]
	if len(lines) != 1 || lines[0]["result"] != "failed" || lines[0]["code"] != "error" {
		t.Fatalf("failure audit lines %v", lines)
	}

	if _, _, err := r.cli(t, false, "worktree", "go-work", "extra"); err == nil || err.Error() != "usage: agentflow worktree go-work" {
		t.Errorf("extra argument: %v", err)
	}
}

// mergedTask adds a linked worktree on a branch whose work is merged into
// main, as finish and sweep need it, and keeps gh from asking GitHub.
func (r *opsRepo) mergedTask(t *testing.T, branch string) string {
	t.Helper()
	wt := filepath.Join(os.Getenv("HOME"), ".worktrees", "app", branch)
	r.git(t, "worktree", "add", "-q", "-b", branch, wt, "main")
	writeOpsFile(t, filepath.Join(wt, branch+".txt"), branch+"\n")
	repoGit(t, wt, "add", ".")
	repoGit(t, wt, "commit", "-q", "-m", "fix: "+branch)
	r.git(t, "switch", "-q", "main")
	r.git(t, "merge", "-q", "--no-ff", "-m", "merge "+branch, branch)
	r.git(t, "update-ref", "refs/remotes/origin/main", "main")
	r.git(t, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	r.fakeGH(t, "#!/bin/sh\nexit 1\n")
	return wt
}

// fakeGH puts a stand-in gh first on PATH.
func (r *opsRepo) fakeGH(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestFinishPublishesConfirmApplyAndRefusal(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", confirmEverything)
	wt := r.mergedTask(t, "task-2")
	quietStdout(t)
	quietStderr(t)

	// A dry run publishes nothing.
	em, env, err := r.cli(t, false, "finish", "task-2", "--dry-run")
	if err != nil || !strings.Contains(env.Out.String(), `"state": "would_finish"`) {
		t.Fatalf("dry run: %v, output %q", err, env.Out.String())
	}
	wantOpTypes(t, em)

	// Confirm, then apply.
	em, env, err = r.cli(t, false, "finish", "--yes", "task-2")
	if err != nil {
		t.Fatal(err)
	}
	evs := wantOpConfirmFlow(t, em, "finish", "finish", "remote")
	wantWorktreeScope(t, evs, false)
	if p, _ := commandtest.Payload(evs[0]); p.Branch != "task-2" || p.Subject != "finish a merged task" {
		t.Errorf("payload %+v", p)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) || !strings.Contains(env.Out.String(), `"state": "finished"`) {
		t.Fatalf("worktree left behind: %v, output %q", err, env.Out.String())
	}

	// Finishing again changes nothing and needs no approval.
	em, _, err = r.cli(t, false, "finish", "task-2", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpApplied)

	// A refusal: unmerged work keeps the worktree and fails the operation,
	// and the command prints what it checked.
	other := filepath.Join(os.Getenv("HOME"), ".worktrees", "app", "task-3")
	r.git(t, "worktree", "add", "-q", "-b", "task-3", other, "main")
	writeOpsFile(t, filepath.Join(other, "wip.txt"), "wip\n")
	repoGit(t, other, "add", ".")
	repoGit(t, other, "commit", "-q", "-m", "wip: unmerged")
	em, env, err = r.cli(t, false, "finish", "task-3", "--yes")
	if err == nil {
		t.Fatal("finishing unmerged work succeeded")
	}
	evs = wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	if p, _ := commandtest.Payload(evs[1]); p.Code != "error" {
		t.Errorf("refusal payload %s", evs[1].Payload)
	}
	if !strings.Contains(env.Out.String(), `"state": "unmerged"`) {
		t.Errorf("the refusal did not print its outcome: %q", env.Out.String())
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("the refused worktree is gone: %v", err)
	}
	noTokensInEvents(t, em)

	if _, _, err := r.cli(t, false, "finish", "a", "b"); err == nil || err.Error() != "usage: agentflow finish [BRANCH] [--dry-run] [--yes]" {
		t.Errorf("two branches: %v", err)
	}
}

func TestSweepToolPublishesRepositoryWideEvents(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", confirmEverything)
	wt := r.mergedTask(t, "task-4")

	// A report publishes nothing and removes nothing.
	em, out, isErr := r.tool(t, "sweep", map[string]any{"dry_run": true})
	if isErr {
		t.Fatalf("sweep dry run: %s", out)
	}
	wantOpTypes(t, em)
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("a report removed the worktree: %v", err)
	}

	// The first call asks; the second resumes with the token and applies.
	em, out, isErr = r.tool(t, "sweep", map[string]any{})
	var pending struct {
		Status       string `json:"status"`
		ConfirmToken string `json:"confirm_token"`
	}
	if isErr || json.Unmarshal([]byte(out), &pending) != nil || pending.Status != "needs_confirmation" || pending.ConfirmToken == "" {
		t.Fatalf("sweep did not ask: %v %s", isErr, out)
	}
	evs := wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval)
	wantWorktreeScope(t, evs, true)
	wantOpPayload(t, evs[0], "sweep", "sweep", "remote")
	em2, out, isErr := r.tool(t, "sweep", map[string]any{"confirm_token": pending.ConfirmToken})
	if isErr {
		t.Fatalf("confirmed sweep: %s", out)
	}
	evs2 := wantOpTypes(t, em2, bus.TypeOpRequested, bus.TypeOpApplied)
	wantWorktreeScope(t, evs2, true)
	if evs2[0].CorrelationID != evs[1].CorrelationID || evs2[1].CorrelationID != evs[1].CorrelationID {
		t.Errorf("the confirmed call does not join the approval %s: %+v", evs[1].CorrelationID, evs2)
	}
	noTokensInEvents(t, em2)
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("sweep left the merged worktree: %v", err)
	}
}

func TestPromotePublishesConfirmApplyAndRefusal(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", confirmEverything)
	r.fakeGH(t, "#!/bin/sh\ncase \"$2\" in\n  list) exit 0 ;;\n  create) echo https://github.com/acme/app/pull/9 ;;\nesac\n")
	quietStdout(t)

	// Inside an agent session, promote refuses: the people-only rule.
	em, _, err := r.cli(t, false, "promote", "--from", "develop", "--yes")
	if err == nil || !strings.Contains(err.Error(), "promote is for the developer") {
		t.Fatalf("promote in an agent: %v", err)
	}
	evs := wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	wantWorktreeScope(t, evs, true)

	t.Setenv("AGENTFLOW_ALLOW_PROMOTE", "1")
	em, env, err := r.cli(t, false, "promote", "--from", "develop", "--dry-run")
	if err != nil || !strings.Contains(env.Out.String(), `"dry_run": true`) {
		t.Fatalf("dry run: %v, output %q", err, env.Out.String())
	}
	wantOpTypes(t, em)

	em, env, err = r.cli(t, false, "promote", "--from", "develop", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	evs = wantOpConfirmFlow(t, em, "promote", "promote", "remote")
	wantWorktreeScope(t, evs, true)
	if p, _ := commandtest.Payload(evs[0]); p.Branch != "main" || p.Subject != "promote to main" {
		t.Errorf("payload %+v", p)
	}
	if !strings.Contains(env.Out.String(), "https://github.com/acme/app/pull/9") {
		t.Errorf("output %q", env.Out.String())
	}
	noTokensInEvents(t, em)
	if lines := r.auditLines(t); len(lines) != 1 || lines[0]["action"] != "promote" {
		t.Errorf("audit lines %v, want the method's one line", lines)
	}
}
