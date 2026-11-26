package ops_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/command/commandtest"
)

// The read operations (work, context, scope, standup) are registered with the
// same CLI and tool as before and publish no events.

func TestWorkAndContextPrintTextAndPublishNothing(t *testing.T) {
	r := newTaskRepo(t, confirmAll)
	key := r.createTask(t, "Review quotas", "- [ ] Run tests")
	em, inv := recording()
	env := &commandtest.Env{App: r.app}

	var err error
	text := taskStdout(t, func() { err = taskRun(t, env, inv, "work") })
	if err != nil {
		t.Fatal(err)
	}
	if want := key + " "; !strings.HasPrefix(text, want) || !strings.Contains(text, "Open") || !strings.Contains(text, "Review quotas") {
		t.Errorf("work output %q, want a row for %s", text, key)
	}
	// Like the legacy command, work ignores arguments.
	if taskStdout(t, func() { err = taskRun(t, env, inv, "work", "ignored") }); err != nil {
		t.Errorf("work with an argument: %v", err)
	}

	text = taskStdout(t, func() { err = taskRun(t, env, inv, "context", key) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Review quotas") || !strings.Contains(text, "Run tests") {
		t.Errorf("context output %q", text)
	}

	// The tools return the same data as JSON.
	out, isErr := invokeTool(t, r.app, "work", map[string]any{}, inv)
	if isErr || !strings.Contains(out, key) {
		t.Errorf("my_work tool: %v %s", isErr, out)
	}
	out, isErr = invokeTool(t, r.app, "context", map[string]any{"key": key}, inv)
	if isErr || !strings.Contains(out, "Review quotas") {
		t.Errorf("context tool: %v %s", isErr, out)
	}
	if got := em.Types(); len(got) != 0 {
		t.Errorf("reads published %v", got)
	}
}

func TestScopePrintsTextAndJSON(t *testing.T) {
	r := newTaskRepo(t, confirmAll)
	if err := os.WriteFile(filepath.Join(r.dir, "todo.go"), []byte("package x\n// TODO: unrelated cleanup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	em, inv := recording()
	env := &commandtest.Env{App: r.app}
	var err error
	text := taskStdout(t, func() { err = taskRun(t, env, inv, "scope") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "  todo: ") || !strings.Contains(text, "unrelated cleanup") {
		t.Errorf("scope text %q", text)
	}
	if err := taskRun(t, env, inv, "scope", "--json"); err != nil {
		t.Fatal(err)
	}
	var drift struct {
		Summary string
		TODOs   []string
	}
	if err := json.Unmarshal(env.Out.Bytes(), &drift); err != nil || drift.Summary == "" || len(drift.TODOs) != 1 {
		t.Errorf("scope JSON %q, %v", env.Out.String(), err)
	}
	if err := taskRun(t, env, inv, "scope", "extra"); err == nil || err.Error() != "usage: agentflow scope [--json]" {
		t.Errorf("scope with an argument: %v", err)
	}
	if got := em.Types(); len(got) != 0 {
		t.Errorf("reads published %v", got)
	}
}

func TestStandupPrintsTextAndJSON(t *testing.T) {
	r := newTaskRepo(t, confirmAll)
	em, inv := recording()
	env := &commandtest.Env{App: r.app}
	if err := taskRun(t, env, inv, "standup", "--json", "--since", "90m"); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(env.Out.Bytes(), &report); err != nil {
		t.Fatalf("standup JSON %q, %v", env.Out.String(), err)
	}
	var err error
	text := taskStdout(t, func() { err = taskRun(t, env, inv, "standup", "--since", "90m") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Commits\n  - ") {
		t.Errorf("standup text %q", text)
	}
	if err := taskRun(t, env, inv, "standup", "--since", "soon"); err == nil {
		t.Error("standup took a duration it cannot parse")
	}
	if got := em.Types(); len(got) != 0 {
		t.Errorf("reads published %v", got)
	}
}
