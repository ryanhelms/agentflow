package ops_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command/ops"
)

// With ai.sessions.agent_start unset (on by default) and every autonomy
// level at auto, an agent still starts a session only through the
// confirmation: its first agentflow_ai_run returns needs_confirmation and
// writes no session record. ai.run is on the mandatory-confirmation list
// (internal/config/autonomy.go), which is what holds this; Plan.Force only
// lets a token be redeemed. This runs the real Manager.
func TestAgentStartUnsetNeedsConfirmation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh script")
	}
	resetFake(t)
	detach, d := aisessiontest.InProcessDetach(t)
	oldOptions := ops.AIOptions
	ops.AIOptions = func(a *app.App) aisession.Options {
		o := ops.AISessionOptions(a)
		o.Detach = detach
		return o
	}
	t.Cleanup(func() { ops.AIOptions = oldOptions })
	old := app.NewAIManager
	app.NewAIManager = aisession.NewManager
	t.Cleanup(func() { app.NewAIManager = old })

	agent := aisessiontest.Install(t, "fake-agent", aisessiontest.Script{})
	home := aisessiontest.Home(t, aisessiontest.Settings{}, agent.Dir)
	// config.yml without agent_start: the default.
	cfg := "autonomy: {local: auto, remote: auto, actions: {ai.run: auto}}\n" +
		"ai:\n  harnesses:\n    auto_probe: false\n    sign_in_check: false\n    overrides:\n      demo: {name: Demo, command: fake-agent}\n"
	if err := os.WriteFile(filepath.Join(home, "config.yml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	aisessiontest.Ready(t, home, "demo")
	wt, _ := aisessiontest.Worktree(t)
	a, err := app.Open(t.Context(), wt, "claude")
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := aiTool(t, a, "agentflow_ai_run")
	text, isErr := callText(t, cs, "agentflow_ai_run", map[string]any{"harness": "demo", "prompt": "say hi"})
	if isErr || !strings.Contains(text, `"needs_confirmation"`) {
		t.Fatalf("first call: %s", text)
	}
	var pending struct {
		Token   string         `json:"confirm_token"`
		Summary aisession.Plan `json:"summary"`
	}
	if err := json.Unmarshal([]byte(text), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Token == "" || pending.Summary.Ceiling != "confirm" || pending.Summary.StartedBy.Kind != aisession.RequesterAgent {
		t.Fatalf("summary %+v", pending.Summary)
	}
	if _, err := os.Stat(filepath.Join(home, "ai", "sessions")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(home, "ai", "sessions"))
		if len(entries) != 0 {
			t.Fatalf("a session record exists before the confirmation: %v", entries)
		}
	}
	// The same call again without a token asks again; nothing starts.
	if text, _ := callText(t, cs, "agentflow_ai_run", map[string]any{"harness": "demo", "prompt": "say hi"}); !strings.Contains(text, `"needs_confirmation"`) && !strings.Contains(text, `"awaiting_approval"`) {
		t.Fatalf("second call: %s", text)
	}
	st, err := aisession.OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := st.List(); len(list) != 0 {
		t.Fatalf("sessions without a confirmation: %+v", list)
	}
	// Applying the token is what starts it: detached, as the agent's.
	d.Use(aisession.NewManager(ops.AISessionOptions(a)))
	text, isErr = callText(t, cs, "agentflow_ai_run", map[string]any{"harness": "demo", "prompt": "say hi", "confirm_token": pending.Token})
	var res app.AIRunResult
	if isErr || json.Unmarshal([]byte(text), &res) != nil || !res.Detached || res.Session == "" {
		t.Fatalf("confirmed call: %s", text)
	}
	s := d.Wait(t, res.Session, 60*time.Second)
	if s.StartedBy.Kind != aisession.RequesterAgent || s.Ceiling != "confirm" || s.Code != aisession.CodeEndTurn {
		t.Errorf("session %+v", s)
	}
}
