package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestUserHarnessesEffectiveValues(t *testing.T) {
	var h UserHarnesses
	if !h.AutoProbeOn() || !h.SignInCheckOn() {
		t.Fatal("nil toggles must mean on")
	}
	off := false
	h.AutoProbe, h.SignInCheck = &off, &off
	if h.AutoProbeOn() || h.SignInCheckOn() {
		t.Fatal("false toggles must mean off")
	}
	if h.EffectiveProbeTimeout() != DefaultHarnessProbeTimeout || h.EffectivePollInterval() != DefaultHarnessPollInterval {
		t.Fatal("defaults")
	}
	long := time.Hour
	h.ProbeTimeout, h.PollInterval = &long, &long
	if h.EffectiveProbeTimeout() != MaxHarnessProbeTimeout || h.EffectivePollInterval() != MaxHarnessPollInterval {
		t.Fatal("clamping")
	}
	if err := ValidateAI(UserAI{Harnesses: h}); err == nil {
		t.Fatal("ValidateAI accepted an hour")
	}
	if err := ValidateAI(UserAI{Harnesses: UserHarnesses{Disabled: make([]string, 65)}}); err == nil {
		t.Fatal("ValidateAI accepted 65 disabled IDs")
	}
}

// TestRepoAIKeysAreOnlyTightening: a repository has no harness layer.
// Phase 7 reads exactly three tightening keys from a repository's ai
// section; everything else in it (harnesses, a session home, rules that
// allow) is ignored, so a repository can never name a harness or loosen.
func TestRepoAIKeysAreOnlyTightening(t *testing.T) {
	raw := []byte("ai:\n  harnesses:\n    auto_probe: true\n    overrides:\n      evil:\n        command: ./bin/evil\n" +
		"  sessions:\n    disabled: true\n    agent_start: true\n    home: real\n" +
		"  permissions:\n    ask: [\"secrets/**\"]\n    deny: [\"prod/**\"]\n    rules:\n      all:\n        kind: terminal\n        match: \"*\"\n        decision: allow\n")
	c, _, found, err := parseRepoFile(raw)
	if err != nil || !found || c == nil {
		t.Fatalf("parse: %v %v", found, err)
	}
	if !c.AI.Sessions.Disabled || len(c.AI.Permissions.Ask) != 1 || len(c.AI.Permissions.Deny) != 1 {
		t.Fatalf("repo ai = %+v", c.AI)
	}
	b, _ := yaml.Marshal(c.AI)
	for _, word := range []string{"harnesses", "evil", "agent_start", "home", "rules", "allow"} {
		if strings.Contains(string(b), word) {
			t.Errorf("repository ai kept %q: %s", word, b)
		}
	}
}

func TestUserAISessionsEffectiveValues(t *testing.T) {
	var s UserAISessions
	if !s.AgentStartOn() || !s.AgentflowMCPOn() || !s.KeepTranscriptsOn() {
		t.Fatal("nil toggles must mean on (agent_start defaults on)")
	}
	if s.EffectiveHome() != AISessionHomeReal || s.EffectiveUnasked() != AIUnaskedWarn || s.EffectiveMaxRunning() != 2 ||
		s.EffectiveKeepDays() != 7 || s.EffectiveMaxDuration() != 30*time.Minute || s.EffectiveIdleTimeout() != 10*time.Minute ||
		s.EffectiveAskTimeout() != 5*time.Minute {
		t.Fatalf("defaults: %+v", s)
	}
	off, n, d := false, 9, 5*time.Hour
	s = UserAISessions{AgentStart: &off, Home: "isolated", Unasked: "cancel", MaxRunning: &n, MaxDuration: &d}
	if s.AgentStartOn() || s.EffectiveHome() != AISessionHomeIsolated || s.EffectiveUnasked() != AIUnaskedCancel ||
		s.EffectiveMaxRunning() != MaxAIMaxRunning || s.EffectiveMaxDuration() != MaxAIMaxDuration {
		t.Fatalf("set: %+v", s)
	}
}

func TestValidateAISessions(t *testing.T) {
	five, zero := 5, 0
	short := 10 * time.Second
	for name, a := range map[string]UserAI{
		"max_running": {Sessions: UserAISessions{MaxRunning: &five}},
		"keep_days":   {Sessions: UserAISessions{KeepDays: &zero}},
		"ask_timeout": {Sessions: UserAISessions{AskTimeout: &short}},
		"home":        {Sessions: UserAISessions{Home: "elsewhere"}},
		"unasked":     {Sessions: UserAISessions{Unasked: "ignore"}},
		"modes":       {Sessions: UserAISessions{Modes: manyStrings(65)}},
		"rules":       {Permissions: UserAIPermissions{Rules: manyRules(65)}},
	} {
		if err := ValidateAI(a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := ValidateAI(UserAI{}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAIRule(t *testing.T) {
	for _, ok := range []AIRule{
		{Kind: "fs_write", Match: "src/**", Decision: "allow"},
		{Kind: "terminal", Match: "go", Decision: "allow"},
		{Kind: "tool", Match: "fetch", Decision: "allow"},
		{Kind: "tool", Match: "execute", Decision: "deny"},
		{Kind: "fs_read", Match: "*.md", Decision: "ask"},
	} {
		if err := ValidateAIRule("r", ok); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
	for _, bad := range []AIRule{
		{Kind: "network", Match: "x", Decision: "allow"},
		{Kind: "fs_read", Match: "x", Decision: "maybe"},
		{Kind: "fs_read", Match: "", Decision: "deny"},
		{Kind: "fs_read", Match: "[", Decision: "deny"},
		{Kind: "tool", Match: "execute", Decision: "allow"},
		{Kind: "tool", Match: "switch_mode", Decision: "allow"},
		{Kind: "tool", Match: "other", Decision: "allow"},
		{Kind: "tool", Match: "*", Decision: "allow"},
	} {
		if err := ValidateAIRule("r", bad); err == nil {
			t.Errorf("%+v: accepted", bad)
		}
	}
}

func manyStrings(n int) map[string]string {
	m := map[string]string{}
	for i := range n {
		m[strings.Repeat("a", i+1)] = "x"
	}
	return m
}

func manyRules(n int) map[string]AIRule {
	m := map[string]AIRule{}
	for i := range n {
		m[strings.Repeat("r", i+1)] = AIRule{Kind: "fs_read", Match: "x", Decision: "deny"}
	}
	return m
}

func TestAISessionAutonomyActions(t *testing.T) {
	classes := map[string]string{
		"ai.run": "local", "ai.respond": "local", "ai.cancel": "local", "ai.fs.read": "read",
		"ai.fs.write": "local", "ai.terminal": "local", "ai.fetch": "remote", "ai.permission": "local",
	}
	mandatory := map[string]bool{"ai.run": true, "ai.terminal": true, "ai.fetch": true, "ai.permission": true}
	for action, class := range classes {
		a, ok := DescribeAutonomyAction(action)
		if !ok || len(a.Classes) != 1 || a.Classes[0] != class {
			t.Errorf("%s: %+v %v", action, a, ok)
		}
		if a.Mandatory != mandatory[action] {
			t.Errorf("%s: mandatory = %v", action, a.Mandatory)
		}
		if mandatory[action] {
			u := &User{Autonomy: Autonomy{Local: Auto, Remote: Auto}}
			if d := ResolveAutonomy(u, nil, class, action, a.Mandatory); d.Level != Confirm {
				t.Errorf("%s: auto resolves to %s with the mandatory flag", action, d.Level)
			}
		}
	}
}
