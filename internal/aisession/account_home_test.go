package aisession_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/config"
)

// askOrEnd waits for session id's first ask, or for its end (nil ask).
func askOrEnd(t *testing.T, r *rig, id aisession.ID) *aisession.AskRecord {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s, asks, err := r.m.Get(person(context.Background()), id)
		if err == nil && len(asks) > 0 {
			return &asks[0]
		}
		if err == nil && s.State == aisession.StateEnded {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("neither an ask nor the end")
	return nil
}

// denyAndWait rejects ask a (when there is one) and waits for the end.
func denyAndWait(t *testing.T, r *rig, id aisession.ID, a *aisession.AskRecord) {
	t.Helper()
	if a != nil {
		_, _ = r.m.Answer(person(context.Background()), aisession.AnswerRequest{Session: id, Ask: a.ID, Decision: aisession.Deny})
	}
	r.d.Wait(t, id, 60*time.Second)
}

// rescript replaces the fake agent's script (for steps that name a path
// only known once the rig exists).
func rescript(t *testing.T, r *rig, s aisessiontest.Script) {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.agent.Log), "fake-agent.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// When AGENTFLOW_HOME is another folder, the account's own agentflow home
// and its ai folder are on the never-list too: a terminal the person's
// rules allow still cannot read them.
func TestAccountAgentflowHomeIsProtected(t *testing.T) {
	bin := script(t, "cat", "/bin/cat \"$@\"\n")
	allowCat := aisessiontest.Settings{AgentStart: true, Rules: map[string]config.AIRule{"cat": {Kind: config.AIRuleKindTerminal, Match: "cat", Decision: config.AIDecisionAllow}}}
	for _, target := range []string{"config.yml", filepath.Join("ai", "sessions")} {
		t.Run(target, func(t *testing.T) {
			r, acct := accountRig(t, aisessiontest.Script{}, allowCat, aisessiontest.Settings{AgentStart: true}, bin)
			rescript(t, r, aisessiontest.Script{Steps: []aisessiontest.Step{
				call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": "cat", "args": []string{filepath.Join(acct, target)}}),
				call(acp.MethodTerminalWaitForExit, map[string]any{"sessionId": "$SESSION", "terminalId": "$TERMINAL"}),
			}})
			s := r.start(t, person(context.Background()), "x")
			denyAndWait(t, r, s.ID, askOrEnd(t, r, s.ID))
			creates := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodTerminalCreate)
			if len(creates) != 1 || creates[0].ErrorData != aisession.VerdictAgentflowHome {
				t.Errorf("cat of the account's agentflow home = %+v, want deny agentflow_home", creates)
			}
		})
	}
}

// An allow rule in an agent-chosen AGENTFLOW_HOME does not override the
// account's ai.fs.read: confirm: the read still asks.
func TestSessionHomeAllowRuleDoesNotBeatTheAccount(t *testing.T) {
	read := aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodReadTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/README.md"}),
	}}
	home := aisessiontest.Settings{AgentStart: true, Rules: map[string]config.AIRule{"r": {Kind: config.AIRuleKindFSRead, Match: "**", Decision: config.AIDecisionAllow}}}
	acct := aisessiontest.Settings{AgentStart: true, Autonomy: config.Autonomy{Actions: map[string]config.Level{"ai.fs.read": config.Confirm}}}
	r, _ := accountRig(t, read, home, acct)
	s := r.start(t, agentCtx(context.Background()), "x")
	a := askOrEnd(t, r, s.ID)
	denyAndWait(t, r, s.ID, a)
	if a == nil {
		t.Error("the account's ai.fs.read: confirm did not ask")
	}
	for _, c := range aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodReadTextFile) {
		if c.ErrorCode == 0 {
			t.Errorf("the read was answered: %+v", c)
		}
	}
}

// A start whose detached host cannot be launched leaves no claim token in
// this process.
func TestFailedDetachForgetsTheClaimToken(t *testing.T) {
	var id aisession.ID
	r := newRigWith(t, aisessiontest.Script{}, aisessiontest.Settings{}, func(o *aisession.Options) {
		o.Detach = func(_ context.Context, sid aisession.ID) error { id = sid; return errors.New("no host") }
	})
	if _, err := r.m.Start(person(context.Background()), aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: r.wt, Detach: true}); err == nil {
		t.Fatal("start with a failing detach succeeded")
	}
	if id == "" || aisession.ClaimTokenHeldForTest(id) {
		t.Errorf("claim token of %q still held", id)
	}
}
