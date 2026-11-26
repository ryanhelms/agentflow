package configconsole

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/consolepublish"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/policy"
)

func TestAgentPublicationSharesApprovalAndRechecksPolicy(t *testing.T) {
	ctx := t.Context()
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := &gitx.Repo{Dir: dir}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://github.com/example/console"}, {"-c", "user.name=Test", "-c", "user.email=agentflow@example.test", "commit", "-q", "--allow-empty", "-m", "test: initial checkout"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, b)
		}
	}
	const path = ".agentflow/config.yml"
	const original = "agentflow: 1\ncommit:\n  require_key: false\n"
	file := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.Mkdir(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	caller := filepath.Join(dir, ".github", "workflows", "agentflow.yml")
	const workflow = "on: [push]\njobs:\n  verify:\n    uses: org/ci/.github/workflows/check.yml@first\n"
	if err := os.MkdirAll(filepath.Dir(caller), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caller, []byte(workflow), 0600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, "config.yml")
	setRemote := func(level string) {
		t.Helper()
		if err := os.WriteFile(cfgPath, []byte("autonomy: {remote: "+level+"}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	setRemote("auto")
	newGate := func(client string) *policy.Gate {
		t.Helper()
		g, err := policy.NewGate(home, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, client)
		if err != nil {
			t.Fatal(err)
		}
		g, err = CurrentGate(ctx, repo, g, client)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	cli, mcp := newGate("cli"), newGate("codex")
	edit := Edit{Path: path, Base: hash([]byte(original)), Content: original + "# reviewed change\n", Title: "chore: clarify the configuration", Draft: true}
	var calls atomic.Int32
	publish := func(_ context.Context, root, target, path, before, after, title string, draft bool, _ map[string]string, task ...string) (consolepublish.Result, error) {
		calls.Add(1)
		if root != dir || target != "example/console" || path != edit.Path || before != original || after != edit.Content || title != edit.Title || !draft {
			return consolepublish.Result{}, errors.New("publication differs from reviewed intent")
		}
		return consolepublish.Result{State: "draft", URL: "https://github.com/example/console/pull/1"}, nil
	}
	owner, err := Review(ctx, repo, cli, "agent", edit, "", false, publish)
	if err != nil || owner.Outcome != policy.NeedsConfirmation || owner.Token == "" || owner.Approval == nil || !strings.Contains(owner.Plan.Diff, "reviewed change") {
		t.Fatalf("missing mandatory publication preview: %+v %v", owner, err)
	}
	waiting, err := Review(ctx, repo, mcp, "agent", edit, "", true, publish)
	if err != nil || waiting.Outcome != policy.AwaitingApproval || waiting.Token != "" || waiting.Approval.OperationID != owner.Approval.OperationID || calls.Load() != 0 {
		t.Fatalf("worker obtained another prompt or wrote without approval: %+v %v", waiting, err)
	}
	if _, err = Review(ctx, repo, mcp, "browser-session", edit, owner.Token, true, publish); !errors.Is(err, policy.ErrChanged) {
		t.Fatalf("browser session accepted agent preview: %v", err)
	}
	changed := edit
	changed.Draft = false
	if _, err = Review(ctx, repo, mcp, "agent", changed, owner.Token, true, publish); !errors.Is(err, policy.ErrChanged) {
		t.Fatalf("draft approval accepted a merge: %v", err)
	}

	// Identical triggers and provider values do not imply an identical CI pin.
	if err := os.WriteFile(caller, []byte(strings.Replace(workflow, "@first", "@second", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Review(ctx, repo, mcp, "agent", edit, owner.Token, true, publish); !errors.Is(err, policy.ErrChanged) || calls.Load() != 0 {
		t.Fatalf("accepted another CI workflow pin: %v", err)
	}
	if err := os.WriteFile(caller, []byte(workflow), 0600); err != nil {
		t.Fatal(err)
	}

	// Even a long-lived MCP server must enforce a saved downgrade.
	setRemote("suggest")
	suggest, err := Review(ctx, repo, mcp, "agent", edit, owner.Token, true, publish)
	if err != nil || suggest.Outcome != policy.SuggestOnly || suggest.Published != nil || calls.Load() != 0 {
		t.Fatalf("policy downgrade: %+v %v", suggest, err)
	}
	setRemote("auto")
	id := owner.Approval.OperationID
	if _, err = owner.Gate.Approve(id, owner.Token); err != nil {
		t.Fatal(err)
	}
	// Either client can claim the grant, but only one may contact the provider.
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, actor := range []*policy.Gate{mcp, cli} {
		go func() {
			<-start
			applied, err := Review(ctx, repo, actor, "agent", edit, id, true, publish)
			if err == nil && (applied.Published == nil || applied.Published.State != "draft") {
				err = errors.New("missing draft publication receipt")
			}
			done <- err
		}()
	}
	close(start)
	first, second := <-done, <-done
	if first == nil {
		first, second = second, first
	}
	if !errors.Is(first, policy.ErrConsumed) || second != nil || calls.Load() != 1 {
		t.Fatalf("concurrent handoff: %v, %v; provider called %d times", first, second, calls.Load())
	}
}
