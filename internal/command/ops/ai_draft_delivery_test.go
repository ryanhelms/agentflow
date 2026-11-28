package ops_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/ops"
	"github.com/ryanhelms/agentflow/internal/config"
)

// nothingEnv fails the test if a command opens the App, asks or prints.
type nothingEnv struct{ t *testing.T }

func (e nothingEnv) Open(context.Context) (*app.App, error) {
	e.t.Error("the command opened the repository")
	return nil, errors.New("opened")
}
func (e nothingEnv) Origin(*app.App) command.Origin { return command.Origin{Source: "cli"} }
func (e nothingEnv) Confirm(bool, func(string) (any, error)) error {
	e.t.Error("the command reached the confirmation")
	return errors.New("confirm")
}
func (e nothingEnv) Print(any, error) error {
	e.t.Error("the command printed a result")
	return errors.New("print")
}

func aiVerb(t *testing.T, args ...string) error {
	t.Helper()
	inv := command.InvokerFunc(func(context.Context, *command.Call) (any, error) {
		t.Errorf("%v ran an operation", args)
		return nil, errors.New("invoked")
	})
	return taskRun(t, nothingEnv{t}, inv, args...)
}

func aiCode(t *testing.T, err error) (string, string) {
	t.Helper()
	var r *app.AIRefusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a refusal", err)
	}
	return r.Refusal.Code, r.Refusal.Fix
}

func terminal(t *testing.T, is bool) {
	t.Helper()
	old := ops.StdinIsTerminal
	ops.StdinIsTerminal = func() bool { return is }
	t.Cleanup(func() { ops.StdinIsTerminal = old })
}

func person(t *testing.T) {
	t.Helper()
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "AGENTFLOW_CODEX_HOST_ACTIVE", app.SessionEnv} {
		t.Setenv(name, "")
	}
	if name := app.AgentEnvironment(); name != "" {
		t.Skipf("the environment looks like an agent's (%s)", name)
	}
	terminal(t, true)
}

// --ai is for a person at a terminal: anyone else is refused before
// anything is opened, gathered or sent, with the command to run instead.
func TestDeliveryAIFlagNeedsPerson(t *testing.T) {
	for feature, args := range map[string][]string{"commit": {"commit", "--ai"}, "pr": {"pr", "--ai"}, "ac": {"ac", "--ai"}} {
		t.Run(feature+" agent", func(t *testing.T) {
			terminal(t, true)
			t.Setenv("CLAUDECODE", "1")
			code, fix := aiCode(t, aiVerb(t, args...))
			if code != ai.CodeNeedsPerson || !strings.Contains(fix, "ai draft "+feature) {
				t.Errorf("%s: %s", code, fix)
			}
		})
		t.Run(feature+" no terminal", func(t *testing.T) {
			person(t)
			terminal(t, false)
			if code, _ := aiCode(t, aiVerb(t, args...)); code != ai.CodeNeedsPerson {
				t.Errorf("%s", code)
			}
		})
		t.Run(feature+" in a session", func(t *testing.T) {
			person(t)
			t.Setenv(app.SessionEnv, "ais_0123456789abcdef0123456789abcdef")
			if code, _ := aiCode(t, aiVerb(t, args...)); code != ai.CodeNeedsPerson {
				t.Errorf("%s", code)
			}
		})
	}
}

// --ai takes neither --yes nor the text it would draft.
func TestDeliveryAIFlagUsage(t *testing.T) {
	person(t)
	for _, args := range [][]string{
		{"commit", "--ai", "--yes"},
		{"commit", "--ai", "-m", "feat: x"},
		{"commit", "--ai", "--confirm-token", "cf_x"},
		{"commit", "--ai", "--split"},
		{"commit", "--ai", "--dry-run"},
		{"commit", "--ai", "stray"},
		{"pr", "--ai", "--yes"},
		{"pr", "--ai", "--title", "feat: x"},
		{"pr", "--ai", "--body", "x"},
		{"pr", "--ai", "--confirm-token", "cf_x"},
		{"pr", "--ai", "--check"},
		{"pr", "--ai", "--prepare", "--run"},
		{"pr", "--ai", "stray"},
		{"ac", "--ai", "--yes"},
		{"ac", "list", "--ai", "--yes"},
		{"ac", "--ai", "ONE-1", "TWO-2"},
	} {
		if code, _ := aiCode(t, aiVerb(t, args...)); code != ai.CodeUsage {
			t.Errorf("%v: %s", args, code)
		}
	}
	// Without --ai the commands refuse --yes as before.
	if err := aiVerb(t, "ac", "list", "--yes"); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Errorf("ac list --yes: %v", err)
	}
}

// The real commit feature through agentflow_ai_draft, as scripts/e2e-claude.sh
// calls it: pr is off, commit needs a confirmation whose manifest holds no
// diff, and the confirmed call finds the same context.
func TestDeliveryDraftToolCommit(t *testing.T) {
	feature, ok := ai.Lookup(ai.FeatureCommit)
	if !ok {
		t.Fatal("the commit feature is not registered")
	}
	r := newDraftOps(t, config.Autonomy{Local: config.Auto, Remote: config.Auto})
	t.Cleanup(ai.Replace(feature)) // newDraftOps registers the test feature
	cs, _ := aiTool(t, r.a, "agentflow_ai_draft")
	before := status(t, r.wt)

	if text, isErr := callText(t, cs, "agentflow_ai_draft", map[string]any{"feature": "pr"}); !isErr || !strings.Contains(text, "(feature_off)") {
		t.Errorf("pr: %s", text)
	}
	text, isErr := callText(t, cs, "agentflow_ai_draft", map[string]any{"feature": "commit"})
	var pending struct {
		Status string      `json:"status"`
		Token  string      `json:"confirm_token"`
		Sum    ai.Manifest `json:"summary"`
	}
	if isErr || json.Unmarshal([]byte(text), &pending) != nil || pending.Status != "needs_confirmation" || pending.Sum.PromptSHA256 == "" {
		t.Fatalf("commit: %s", text)
	}
	if strings.Contains(text, "+hello") {
		t.Errorf("the manifest holds the diff: %s", text)
	}
	if n := r.sessions(t); n != 0 {
		t.Fatalf("%d sessions before the confirmation", n)
	}
	text, isErr = callText(t, cs, "agentflow_ai_draft", map[string]any{"feature": "commit", "confirm_token": pending.Token})
	var d ai.Draft
	if isErr || json.Unmarshal([]byte(text), &d) != nil || d.State != ai.DraftReady || len(d.Candidates) != 1 || d.Candidates[0].Tool != "agentflow_commit_prepare" {
		t.Fatalf("confirmed: %s", text)
	}
	if after := status(t, r.wt); after != before {
		t.Errorf("the draft changed the worktree:\n%s", after)
	}
}
