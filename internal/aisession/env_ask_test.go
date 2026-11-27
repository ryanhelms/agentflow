package aisession_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/config"
)

// A terminal request that carries the agent's own environment is never
// allowed by a rule: a value such as GIT_EXTERNAL_DIFF runs code. The ask
// shows each name and value and the resolved program, the fingerprint
// covers them, and the values live only in the ask file.
func TestTerminalEnvAlwaysAsksAndShowsValues(t *testing.T) {
	const value = "/tmp/grab-the-diff-1b2c"
	bin := script(t, "make", "exit 0\n")
	set := aisessiontest.Settings{
		Rules: map[string]config.AIRule{"make": {Kind: config.AIRuleKindTerminal, Match: "make", Decision: config.AIDecisionAllow}},
	}
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": "make", "args": []string{"diff"},
			"env": []map[string]string{{"name": "GIT_EXTERNAL_DIFF", "value": value}}}),
	}}, set, bin)
	ctx := person(context.Background())
	s := r.start(t, ctx, "diff")
	a := r.waitAsk(t, s.ID)
	if a.Reason != aisession.ReasonEnv || !a.CanAllow {
		t.Errorf("ask reason = %q can_allow %v, want env", a.Reason, a.CanAllow)
	}
	if len(a.Env) != 1 || a.Env[0].Name != "GIT_EXTERNAL_DIFF" || a.Env[0].Value != value {
		t.Errorf("ask env = %+v", a.Env)
	}
	if want := filepath.Join(bin, "make"); a.Program != want {
		t.Errorf("ask program = %q, want %q", a.Program, want)
	}
	// The fingerprint covers the values and the program.
	b := a
	b.Env = []aisession.AskEnv{{Name: "GIT_EXTERNAL_DIFF", Value: "/other"}}
	if aisession.Fingerprint(b) == a.Fingerprint {
		t.Error("the fingerprint does not cover the env values")
	}
	b = a
	b.Program = "/usr/bin/git"
	if aisession.Fingerprint(b) == a.Fingerprint {
		t.Error("the fingerprint does not cover the resolved program")
	}
	if _, err := r.m.Answer(ctx, aisession.AnswerRequest{Session: s.ID, Ask: a.ID, Decision: aisession.Deny}); err != nil {
		t.Fatal(err)
	}
	r.d.Wait(t, s.ID, 60*time.Second)
	for _, e := range r.drain() {
		if strings.Contains(string(e.Payload), value) {
			t.Errorf("event %s carries the env value", e.Type)
		}
	}
	assertTreeLacks(t, r.home, value)
}

// The cwd is resolved again after the approval: a folder swapped for a
// link while the person looked is refused (changed).
func TestTerminalCwdRecheckedAfterApproval(t *testing.T) {
	skipNotUnix(t)
	bin := script(t, "make", "exit 0\n")
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": "make", "cwd": "$CWD/sub"}),
	}}, aisessiontest.Settings{}, bin)
	writeFile(t, filepath.Join(r.wt, "sub", "x"), "x")
	writeFile(t, filepath.Join(r.wt, "other", "x"), "x")
	ctx := person(context.Background())
	s := r.start(t, ctx, "make")
	a := r.waitAsk(t, s.ID)
	if a.Program == "" || a.Cwd != "sub" {
		t.Errorf("ask = %+v", a)
	}
	if err := os.RemoveAll(filepath.Join(r.wt, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(r.wt, "other"), filepath.Join(r.wt, "sub")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Answer(ctx, aisession.AnswerRequest{Session: s.ID, Ask: a.ID, Decision: aisession.Allow, Fingerprint: a.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	r.d.Wait(t, s.ID, 60*time.Second)
	creates := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodTerminalCreate)
	if len(creates) != 1 || creates[0].ErrorCode != acp.CodeDenied || creates[0].ErrorData != aisession.VerdictChanged {
		t.Errorf("terminal = %+v", creates)
	}
}
