package ops_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
)

func TestTransitionPublishesConfirmApplyDryRunAndRefusal(t *testing.T) {
	r := newTaskRepo(t, confirmAll)
	taskNoTerminal(t)
	key := r.createTask(t, "Review quotas", "")

	// --dry-run previews the move and is not an operation.
	em, inv := recording()
	env := &commandtest.Env{App: r.app}
	if err := taskRun(t, env, inv, "transition", key, "#done", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if got := em.Types(); len(got) != 0 {
		t.Fatalf("dry run published %v", got)
	}
	if !strings.Contains(env.Out.String(), `"to": "closed"`) {
		t.Fatalf("dry run output %q", env.Out.String())
	}

	// The move waits for approval, then the token resumes it.
	env = &commandtest.Env{App: r.app}
	if err := taskRun(t, env, inv, "transition", key, "#done", "--yes"); err != nil {
		t.Fatal(err)
	}
	payloads := checkConfirmed(t, em, "transition")
	if p := payloads[0]; p.Action != "transition" || p.Class != "remote" || p.Subject != "transition on #done" || p.Task != key {
		t.Errorf("requested payload %+v", p)
	}
	if !strings.Contains(env.Out.String(), `"applied": true`) {
		t.Errorf("output %q", env.Out.String())
	}

	// A forged token is refused by the gate.
	other := r.createTask(t, "Second task", "")
	em, inv = recording()
	if out, isErr := invokeTool(t, r.app, "transition", map[string]any{"key": other, "event": "#done", "confirm_token": "cf_forged"}, inv); !isErr {
		t.Fatalf("forged token accepted: %s", out)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("refusal events %v", got)
	}
	if p := taskPayloads(t, em)[1]; p.Code == "" || p.Code == "unexpected_result" {
		t.Errorf("refusal code %q", p.Code)
	}

	// A task that does not exist is refused by the operation itself.
	em, inv = recording()
	if err := taskRun(t, &commandtest.Env{App: r.app}, inv, "transition", "filesystem:eng/platform/missing", "#done", "--yes"); err == nil {
		t.Fatal("moved a task that does not exist")
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("refused event %v", got)
	}
}

func TestTransitionUnderASuggestPolicyPublishesTheSuggestion(t *testing.T) {
	r := newTaskRepo(t, confirmAll)
	taskNoTerminal(t)
	key := r.createTask(t, "Review quotas", "")
	r.setAutonomy(t, "autonomy: {local: suggest, remote: suggest}\n")
	em, inv := recording()
	if err := taskRun(t, &commandtest.Env{App: r.app}, inv, "transition", key, "#done"); err != nil {
		t.Fatal(err)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpSuggested}) {
		t.Fatalf("events %v", got)
	}
}

func TestTransitionCommandLineErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"transition"}, "usage: agentflow transition [KEY] EVENT"},
		{[]string{"transition", "A", "B", "C"}, "usage: agentflow transition [KEY] EVENT"},
		{[]string{"transition", "--github-event", "KEY"}, "--github-event cannot be combined"},
		{[]string{"transition", "--github-event", "--yes"}, "--github-event cannot be combined"},
		{[]string{"transition", "--github-event", "--shipped"}, "--github-event cannot be combined"},
	} {
		_, inv := recording()
		err := taskRun(t, &commandtest.Env{}, inv, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("agentflow %v: %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestTransitionShippedNeedsAKeyAndADryRun(t *testing.T) {
	r := newTaskRepo(t, confirmAll)
	key := r.createTask(t, "Review quotas", "")
	em, inv := recording()
	for _, args := range [][]string{
		{"transition", "--shipped", "#done"},
		{"transition", "--shipped", key, "#done"},
		{"transition", "--shipped", "--dry-run", "--yes", key, "#done"},
	} {
		err := taskRun(t, &commandtest.Env{App: r.app}, inv, args...)
		if err == nil || !strings.Contains(err.Error(), "--shipped requires an explicit key, event and --dry-run") {
			t.Errorf("agentflow %v: %v", args, err)
		}
	}
	env := &commandtest.Env{App: r.app}
	if err := taskRun(t, env, inv, "transition", "--shipped", "--dry-run", key, "#done"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env.Out.String(), `"to": "closed"`) {
		t.Errorf("shipped preview %q", env.Out.String())
	}
	// The shipped preview and the GitHub event mode are CI plumbing with no
	// tool: they publish no operation events.
	if got := em.Types(); len(got) != 0 {
		t.Errorf("events %v", got)
	}
}
