package aisession_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/config"
)

// accountRig is a rig whose session home (AGENTFLOW_HOME, as a caller may
// set it) is not the account's own agentflow home, which holds acct.
func accountRig(t *testing.T, s aisessiontest.Script, home, acct aisessiontest.Settings, extraPath ...string) (*rig, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	aisessiontest.Write(t, dir, acct)
	r := newRigWith(t, s, home, func(o *aisession.Options) { o.AccountHome = dir }, extraPath...)
	return r, dir
}

// AGENTFLOW_HOME chosen by the caller does not reset the person's own
// restrictions: agent_start off there still refuses an agent.
func TestAccountLayerKeepsAgentStartOff(t *testing.T) {
	r, _ := accountRig(t, aisessiontest.Script{}, aisessiontest.Settings{AgentStart: true}, aisessiontest.Settings{AgentStart: false})
	_, err := r.m.Start(agentCtx(context.Background()), aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: r.wt, Detach: true})
	if code := refusal(t, err); code != aisession.RefuseAgentStartOff {
		t.Errorf("agent start = %s, want agent_start_off", code)
	}
}

// The caps count the account store's sessions too.
func TestAccountLayerCapsCountBothStores(t *testing.T) {
	one := 1
	r, acct := accountRig(t, aisessiontest.Script{}, aisessiontest.Settings{},
		aisessiontest.Settings{Sessions: config.UserAISessions{MaxRunning: &one}})
	// A running session in the account store, heartbeat fresh.
	st, err := aisession.OpenStore(acct)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create(aisession.Session{ID: aisession.NewID(), Worktree: other, Host: aisession.HostInfo{Heartbeat: time.Now().UTC()}}, "x", aisession.Caps{}); err != nil {
		t.Fatal(err)
	}
	_, err = r.m.Start(person(context.Background()), aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: r.wt, Detach: true})
	if code := refusal(t, err); code != aisession.RefuseLimitRunning {
		t.Errorf("start = %s, want limit_running", code)
	}
}

// The account layer's deny rules hold against an allow in AGENTFLOW_HOME;
// its own allow rules never apply.
func TestAccountLayerRulesOnlyRestrict(t *testing.T) {
	bin := script(t, "make", "exit 0\n")
	home := aisessiontest.Settings{Rules: map[string]config.AIRule{"make": {Kind: config.AIRuleKindTerminal, Match: "make", Decision: config.AIDecisionAllow}}}
	acct := aisessiontest.Settings{Rules: map[string]config.AIRule{
		"no-make":    {Kind: config.AIRuleKindTerminal, Match: "make", Decision: config.AIDecisionDeny},
		"read-all":   {Kind: config.AIRuleKindFSRead, Match: "**", Decision: config.AIDecisionAllow},
		"zzz-unused": {Kind: config.AIRuleKindFSWrite, Match: "**", Decision: config.AIDecisionAllow},
	}}
	r, _ := accountRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": "make"}),
	}}, home, acct, bin)
	s := r.run(t, person(context.Background()), "make")
	creates := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodTerminalCreate)
	if len(creates) != 1 || creates[0].ErrorCode != acp.CodeDenied || creates[0].ErrorData != "rule:account:no-make" {
		t.Errorf("terminal make = %+v (session %s)", creates, s.Code)
	}
}
