package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/delegate"
	"github.com/ryanhelms/agentflow/internal/gitx"
)

func TestDelegateStartsSessionAfterConfirmation(t *testing.T) {
	a, _ := issueApp(t)
	ctx := t.Context()
	other := filepath.Join(t.TempDir(), "agentflow")
	mustGit(t, &gitx.Repo{Dir: filepath.Dir(other)}, "init", "-q", other)
	a.User.Worktree.Checkouts = map[string]string{"agentflow": other}
	a.Gate.Autonomy.Local = config.Confirm
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "")

	var calls []string
	oldRun, oldLook := delegate.Run, delegate.LookPath
	delegate.Run = func(_ context.Context, _ string, args ...string) (string, error) {
		if args[0] == "list-panes" {
			return "", nil
		}
		calls = append(calls, strings.Join(args, " "))
		return "%7\n", nil
	}
	delegate.LookPath = func(string) (string, error) { return "tmux", nil }
	t.Cleanup(func() { delegate.Run, delegate.LookPath = oldRun, oldLook })

	req := delegate.Request{Repo: "ryanhelms/agentflow", Task: "merge PR 7 once checks pass", Agent: "claude"}
	dry, err := a.Delegate(ctx, delegate.Request{Repo: req.Repo, Task: req.Task, DryRun: true}, "")
	if err != nil || dry.(*delegate.Plan).Status != delegate.StatusStart || len(calls) != 0 {
		t.Fatalf("dry run %+v %v %v", dry, err, calls)
	}
	v, err := a.Delegate(ctx, req, "")
	if err != nil {
		t.Fatal(err)
	}
	tok := confirmation(t, v)
	if len(calls) != 0 {
		t.Fatalf("started before confirmation: %v", calls)
	}
	v, err = a.Delegate(ctx, req, tok)
	if err != nil {
		t.Fatal(err)
	}
	p := v.(*delegate.Plan)
	if p.Status != delegate.StatusStarted || p.Pane.ID != "%7" || len(calls) != 2 || !strings.Contains(calls[0], "-c "+other) {
		t.Fatalf("plan %+v calls %v", p, calls)
	}

	// native harness: nothing runs, the agent gets a hand-off
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/sock")
	calls = nil
	v, err = a.Delegate(ctx, req, "")
	if err != nil || v.(*delegate.Plan).Status != delegate.StatusNative || len(calls) != 0 {
		t.Fatalf("native %+v %v %v", v, err, calls)
	}
}

func TestDelegateRefusesOwnAndMissingRepository(t *testing.T) {
	a, _ := issueApp(t)
	ctx := t.Context()
	a.User.Worktree.Checkouts = map[string]string{"self": a.Repo.Dir, "gone": filepath.Join(t.TempDir(), "gone")}
	for repo, want := range map[string]string{"self": "own repository", "gone": "worktree.checkouts.gone", "": "--repo"} {
		if _, err := a.Delegate(ctx, delegate.Request{Repo: repo, Task: "x"}, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", repo, err)
		}
	}
}
