package settings_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/settings"
)

func (f *fixture) writeRepo(t *testing.T, extra string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, ".agentflow", "config.yml"), []byte(repoConfig+extra), 0o644); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
}

func TestAISessionSettingsLoosenOnEffectiveValues(t *testing.T) {
	unset := func(id string) settings.Edit { return settings.Edit{ID: id, Op: settings.OpUnset} }
	cases := []struct {
		name, file, repo string
		edit             settings.Edit
		loose            bool
		message          string
	}{
		{"agent_start on", "ai:\n  sessions:\n    agent_start: false\n", "", set("personal:ai.sessions.agent_start", true), true, "Lets agents start"},
		{"agent_start unset", "ai:\n  sessions:\n    agent_start: false\n", "", unset("personal:ai.sessions.agent_start"), true, "Lets agents start"},
		{"agent_start off", "", "", set("personal:ai.sessions.agent_start", false), false, ""},
		{"agent_start true while unset", "", "", set("personal:ai.sessions.agent_start", true), false, ""},
		{"home real", "ai:\n  sessions:\n    home: isolated\n", "", set("personal:ai.sessions.home", "real"), true, "real home folder"},
		{"home unset", "ai:\n  sessions:\n    home: isolated\n", "", unset("personal:ai.sessions.home"), true, "real home folder"},
		{"home isolated", "", "", set("personal:ai.sessions.home", "isolated"), false, ""},
		{"max_running raised", "", "", set("personal:ai.sessions.max_running", 3), true, "3 sessions"},
		{"max_running lowered", "", "", set("personal:ai.sessions.max_running", 1), false, ""},
		{"max_duration raised", "", "", set("personal:ai.sessions.max_duration", "1h"), true, "1h"},
		{"max_duration lowered", "", "", set("personal:ai.sessions.max_duration", "10m"), false, ""},
		{"unasked warn", "ai:\n  sessions:\n    unasked: cancel\n", "", set("personal:ai.sessions.unasked", "warn"), true, "without asking"},
		{"unasked cancel", "", "", set("personal:ai.sessions.unasked", "cancel"), false, ""},
		{"agentflow_mcp on", "ai:\n  sessions:\n    agentflow_mcp: false\n", "", set("personal:ai.sessions.agentflow_mcp", true), true, "read-only tools"},
		{"allow rule added", "", "", set("personal:ai.permissions.rules", map[string]any{"src": map[string]any{"kind": "fs_write", "match": "src/**", "decision": "allow"}}), true, "rule src"},
		{"deny rule added", "", "", set("personal:ai.permissions.rules", map[string]any{"env": map[string]any{"kind": "fs_read", "match": "**/.env", "decision": "deny"}}), false, ""},
		{"deny rule removed", "ai:\n  permissions:\n    rules:\n      env:\n        kind: fs_read\n        match: secrets/*\n        decision: deny\n", "", unset("personal:ai.permissions.rules.env"), true, "rule env"},
		{"ask rule changed", "ai:\n  permissions:\n    rules:\n      go:\n        kind: terminal\n        match: go\n        decision: ask\n", "", set("personal:ai.permissions.rules.go.decision", "deny"), true, "rule go"},
		{"repo disabled off", "", "ai:\n  sessions:\n    disabled: true\n", set("repo:ai.sessions.disabled", false), true, "start in this repository"},
		{"repo disabled on", "", "", set("repo:ai.sessions.disabled", true), false, ""},
		{"repo ask removed", "", "ai:\n  permissions:\n    ask: [\"secrets/**\"]\n", set("repo:ai.permissions.ask", []string{}), true, "secrets/**"},
		{"repo deny added", "", "", set("repo:ai.permissions.deny", []string{"prod/**"}), false, ""},
		{"repo deny removed", "", "ai:\n  permissions:\n    deny: [\"prod/**\"]\n", unset("repo:ai.permissions.deny"), true, "prod/**"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "task-1")
			f.writePersonal(t, tc.file)
			if tc.repo != "" {
				f.writeRepo(t, tc.repo)
			}
			cs := settings.ChangeSet{Edits: []settings.Edit{tc.edit}}
			if strings.HasPrefix(tc.edit.ID, "repo:") {
				cs.Repo.Mode = settings.RepoDirect
			}
			pv, err := f.svc.Preview(t.Context(), cs)
			if err != nil {
				t.Fatal(err)
			}
			got := has(codes(pv), "info:loosens")
			if got != tc.loose {
				t.Fatalf("loosens = %v (%v, %+v), want %v", got, codes(pv), pv.Diagnostics, tc.loose)
			}
			if tc.loose && (len(pv.Confirm) != 1 || !strings.Contains(pv.Confirm[0].Message, tc.message)) {
				t.Fatalf("confirm %+v, want a message with %q", pv.Confirm, tc.message)
			}
		})
	}
}

func TestAISessionSettingsAreChecked(t *testing.T) {
	f := newFixture(t, "task-1")
	rule := func(kind, match, decision string) map[string]any {
		return map[string]any{"kind": kind, "match": match, "decision": decision}
	}
	for name, tc := range map[string]struct {
		edit settings.Edit
		bad  bool
	}{
		"good rule":         {set("personal:ai.permissions.rules", map[string]any{"src": rule("fs_write", "src/**", "allow")}), false},
		"unknown kind":      {set("personal:ai.permissions.rules.x", rule("network", "*", "allow")), true},
		"bad decision":      {set("personal:ai.permissions.rules.x", rule("fs_read", "*", "maybe")), true},
		"bad glob":          {set("personal:ai.permissions.rules.x", rule("fs_read", "[", "deny")), true},
		"allow execute":     {set("personal:ai.permissions.rules.x", rule("tool", "execute", "allow")), true},
		"allow switch_mode": {set("personal:ai.permissions.rules.x", rule("tool", "switch_mode", "allow")), true},
		"allow other":       {set("personal:ai.permissions.rules.x", rule("tool", "other", "allow")), true},
		"deny execute":      {set("personal:ai.permissions.rules.x", rule("tool", "execute", "deny")), false},
		"one field":         {set("personal:ai.permissions.rules", map[string]any{"x": rule("tool", "fetch", "allow")}), false},
		"good mode":         {set("personal:ai.sessions.modes", map[string]any{"claude-acp": "default"}), false},
		"mode with a space": {set("personal:ai.sessions.modes.claude-acp", "rm -rf"), true},
		"max_running high":  {set("personal:ai.sessions.max_running", 9), true},
		"ask_timeout short": {set("personal:ai.sessions.ask_timeout", "5s"), true},
		"repo good pattern": {set("repo:ai.permissions.ask", []string{"secrets/**", "*.pem"}), false},
		"repo absolute":     {set("repo:ai.permissions.deny", []string{"/etc/**"}), true},
		"repo dot dot":      {set("repo:ai.permissions.ask", []string{"../other/**"}), true},
		"repo bad glob":     {set("repo:ai.permissions.ask", []string{"a/[b"}), true},
	} {
		cs := settings.ChangeSet{Edits: []settings.Edit{tc.edit}}
		if strings.HasPrefix(tc.edit.ID, "repo:") {
			cs.Repo.Mode = settings.RepoDirect
		}
		pv, err := f.svc.Preview(t.Context(), cs)
		if err != nil {
			t.Fatal(err)
		}
		// The schema's enum refuses an unknown kind or decision first; the
		// ai link refuses the rest.
		got := false
		for _, d := range pv.Diagnostics {
			got = got || d.Severity == settings.SeverityError
		}
		if got != tc.bad {
			t.Errorf("%s: error = %v (%v)", name, got, pv.Diagnostics)
		}
		for _, d := range pv.Diagnostics {
			if strings.Contains(d.Message, "rm -rf") {
				t.Errorf("%s: a diagnostic quotes the value: %q", name, d.Message)
			}
		}
	}
}
