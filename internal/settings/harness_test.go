package settings_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/settings"
)

func (f *fixture) writePersonal(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.home, "config.yml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
}

func TestHarnessTogglesLoosenOnlyWhenTurnedOn(t *testing.T) {
	cases := []struct {
		name, file string
		edit       settings.Edit
		loose      bool
		message    string
	}{
		{"auto_probe on", "ai:\n  harnesses:\n    auto_probe: false\n", set("personal:ai.harnesses.auto_probe", true), true, "without asking"},
		{"auto_probe unset", "ai:\n  harnesses:\n    auto_probe: false\n", settings.Edit{ID: "personal:ai.harnesses.auto_probe", Op: settings.OpUnset}, true, "without asking"},
		{"auto_probe off", "", set("personal:ai.harnesses.auto_probe", false), false, ""},
		{"auto_probe true while unset", "", set("personal:ai.harnesses.auto_probe", true), false, ""},
		{"sign_in_check on", "ai:\n  harnesses:\n    sign_in_check: false\n", set("personal:ai.harnesses.sign_in_check", true), true, "real home folder"},
		{"sign_in_check unset", "ai:\n  harnesses:\n    sign_in_check: false\n", settings.Edit{ID: "personal:ai.harnesses.sign_in_check", Op: settings.OpUnset}, true, "real home folder"},
		{"sign_in_check off", "", set("personal:ai.harnesses.sign_in_check", false), false, ""},
		{"stop ignoring", "ai:\n  harnesses:\n    disabled: [kimi, goose]\n", set("personal:ai.harnesses.disabled", []string{"goose"}), true, "Stops ignoring the harness kimi"},
		{"ignore more", "ai:\n  harnesses:\n    disabled: [kimi]\n", set("personal:ai.harnesses.disabled", []string{"kimi", "goose"}), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "task-1")
			f.writePersonal(t, tc.file)
			pv, err := f.svc.Preview(t.Context(), settings.ChangeSet{Edits: []settings.Edit{tc.edit}})
			if err != nil {
				t.Fatal(err)
			}
			got := has(codes(pv), "info:loosens")
			if got != tc.loose || (len(pv.Confirm) == 1) != tc.loose {
				t.Fatalf("loosens = %v (%v, %+v), want %v", got, codes(pv), pv.Confirm, tc.loose)
			}
			if tc.loose && !strings.Contains(pv.Confirm[0].Message, tc.message) {
				t.Fatalf("message %q, want %q", pv.Confirm[0].Message, tc.message)
			}
		})
	}
}

func TestHarnessOverridesAreChecked(t *testing.T) {
	f := newFixture(t, "task-1")
	for name, tc := range map[string]struct {
		edit settings.Edit
		bad  bool
	}{
		"good":            {set("personal:ai.harnesses.overrides", map[string]any{"my-agent": map[string]any{"command": "my-agent", "args": []string{"acp"}}}), false},
		"absolute path":   {set("personal:ai.harnesses.overrides.my-agent", map[string]any{"command": "/opt/me/agent"}), false},
		"launcher":        {set("personal:ai.harnesses.overrides.my-agent", map[string]any{"command": "npx"}), true},
		"unsafe arg":      {set("personal:ai.harnesses.overrides", map[string]any{"gemini": map[string]any{"args": []string{"--acp; rm -rf ~"}}}), true},
		"one field":       {set("personal:ai.harnesses.overrides.gemini.command", "bash"), true},
		"base on native":  {set("personal:ai.harnesses.overrides.gemini", map[string]any{"status_from": "base"}), true},
		"base on adapter": {set("personal:ai.harnesses.overrides.claude-acp", map[string]any{"status_args": []string{"auth", "status"}, "status_from": "base"}), false},
	} {
		pv, err := f.svc.Preview(t.Context(), settings.ChangeSet{Edits: []settings.Edit{tc.edit}})
		if err != nil {
			t.Fatal(err)
		}
		if got := has(codes(pv), "error:"+settings.CodeHarness); got != tc.bad {
			t.Errorf("%s: harness error = %v (%v)", name, got, pv.Diagnostics)
		}
		for _, d := range pv.Diagnostics {
			if strings.Contains(d.Message, "rm -rf") {
				t.Errorf("%s: a diagnostic quotes the value: %q", name, d.Message)
			}
		}
	}
}
