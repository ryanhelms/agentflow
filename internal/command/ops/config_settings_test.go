package ops_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
)

func dispatch(t *testing.T, env command.CLIEnv, inv command.Invoker, args ...string) error {
	t.Helper()
	run, rest, ok := command.Default.Dispatch(args)
	if !ok {
		t.Fatalf("%v is not registered", args)
	}
	return run(t.Context(), env, inv, rest)
}

func personalFile(t *testing.T, f *fixture) string {
	b, _ := os.ReadFile(filepath.Join(f.app.Home, "config.yml"))
	return string(b)
}

func TestConfigSetPublishesConfirmApplyAndRefusal(t *testing.T) {
	f := newFixture(t)
	em, inv := recording()
	env := &commandtest.Env{App: f.app, Answer: true}
	if err := dispatch(t, env, inv, "config", "set", "--json", "autonomy.remote=suggest", "approval_window=10m"); err != nil {
		t.Fatal(err)
	}
	if len(env.Tokens) != 2 || !strings.Contains(personalFile(t, f), "suggest") {
		t.Fatalf("tokens %v, file %q, out %s", env.Tokens, personalFile(t, f), env.Out.String())
	}
	want := []bus.Type{bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpApplied}
	if !slices.Equal(em.Types(), want) {
		t.Fatalf("events %v, want %v", em.Types(), want)
	}
	evs := em.Events()
	if evs[3].CorrelationID != evs[1].CorrelationID {
		t.Errorf("the applied save does not join its approval")
	}
	p, _ := commandtest.Payload(evs[0])
	if p.Operation != "config.set" || !strings.Contains(p.Subject, "personal:autonomy.remote") {
		t.Errorf("payload %+v", p)
	}
	for _, ev := range evs {
		if strings.Contains(string(ev.Payload), "suggest") || strings.Contains(string(ev.Payload), env.Tokens[1]) {
			t.Errorf("%s payload carries a value or the token: %s", ev.Type, ev.Payload)
		}
	}
	audits := 0
	for _, line := range f.auditLines(t) {
		if line["action"] == "console.settings" {
			audits++
		}
	}
	if audits != 2 { // one line per call: the request, then the confirmed save
		t.Errorf("%d audit lines for the save, want 2", audits)
	}

	// A refusal is a result: op.failed with the status as code.
	em2, inv2 := recording()
	env2 := &commandtest.Env{App: f.app, Answer: true}
	if err := dispatch(t, env2, inv2, "config", "set", "--json", "personal:autonomy.remote=always"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(em2.Types(), []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("refusal events %v", em2.Types())
	}
	if p, _ := commandtest.Payload(em2.Events()[1]); p.Code != "invalid" {
		t.Errorf("code %q", p.Code)
	}

	// A dry run is a read: no events.
	em3, inv3 := recording()
	env3 := &commandtest.Env{App: f.app}
	if err := dispatch(t, env3, inv3, "config", "set", "--dry-run", "--json", "autonomy.local=auto"); err != nil {
		t.Fatal(err)
	}
	if len(em3.Events()) != 0 || !strings.Contains(env3.Out.String(), `"loosens"`) {
		t.Errorf("dry run: events %v, out %s", em3.Types(), env3.Out.String())
	}

	// Undo, then undo again (redo).
	em4, inv4 := recording()
	env4 := &commandtest.Env{App: f.app, Answer: true}
	if err := dispatch(t, env4, inv4, "config", "undo", "--json"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(personalFile(t, f), "suggest") || !slices.Equal(em4.Types(), want) {
		t.Fatalf("undo: file %q, events %v", personalFile(t, f), em4.Types())
	}
	if p, _ := commandtest.Payload(em4.Events()[0]); p.Operation != "config.undo" {
		t.Errorf("undo operation %q", p.Operation)
	}
	if err := dispatch(t, &commandtest.Env{App: f.app, Answer: true}, inv4, "config", "undo", "--json", "--yes"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(personalFile(t, f), "suggest") {
		t.Errorf("redo: %q", personalFile(t, f))
	}
}

func TestConfigSetKeyErrors(t *testing.T) {
	f := newFixture(t)
	_, inv := recording()
	for _, args := range [][]string{
		{"config", "set", "worktree.root=/tmp/x"}, // both layers have it
		{"config", "set", "no.such.setting=1"},
		{"config", "set", "commit.subject_max=abc"},
		{"config", "set"},
	} {
		if err := dispatch(t, &commandtest.Env{App: f.app}, inv, args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
	err := dispatch(t, &commandtest.Env{App: f.app}, inv, "config", "set", "worktree.root=/tmp/x")
	if err == nil || !strings.Contains(err.Error(), "personal:worktree.root") || !strings.Contains(err.Error(), "repo:worktree.root") {
		t.Errorf("ambiguous key error %v", err)
	}
}

func TestConfigFieldsTool(t *testing.T) {
	f := newFixture(t)
	_, inv := recording()
	cs, _ := connectTool(t, lookup(t, "config.fields"), f.app, inv)
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "agentflow_config_fields", Arguments: map[string]any{"group": "autonomy"}})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	var view struct {
		Fields []struct {
			ID string `json:"id"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &view); err != nil || len(view.Fields) == 0 {
		t.Fatalf("view %v %s", err, res.Content[0].(*mcp.TextContent).Text)
	}
	// The set tool accepts any JSON value in an edit.
	cs2, _ := connectTool(t, lookup(t, "config.preview"), f.app, inv)
	res, err = cs2.CallTool(context.Background(), &mcp.CallToolParams{Name: "agentflow_config_preview", Arguments: map[string]any{
		"edits": []any{map[string]any{"id": "personal:autonomy.actions", "op": "set", "value": map[string]any{"commit": "auto"}}}}})
	if err != nil || res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, `"status":"ok"`) {
		t.Fatalf("%v %+v", err, res)
	}
}
