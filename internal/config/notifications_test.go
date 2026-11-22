package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeSlackNotificationSettings(t *testing.T) {
	const scope = "{server: work-slack, workspace_id: T0123456789, channel_id: C0123456789, actor_id: U0123456789}"
	for _, test := range []struct{ settings, wantError string }{
		{"{provider: slack, settings: {native: " + scope + "}}", ""},
		{"{provider: slack, settings: {native: {channel_id: C0123456789}}}", "native.workspace_id"},
		{"{provider: slack, settings: {native: {workspace_id: T0123456789, channel_id: '#general'}}}", "native.channel_id"},
		{"{provider: slack, settings: {native: {workspace_id: T0123456789, channel_id: C0123456789, actor_id: alice}}}", "native.actor_id"},
		{"{provider: slack, settings: {native: " + scope + ", credential_ref: SLACK_WEBHOOK_URL}}", "webhook settings cannot be used"},
		{"{provider: webhook, settings: {native: " + scope + "}}", "unknown property"},
		{"{provider: router, settings: {destinations: {team: {provider: slack, native: " + scope + "}, ci: {provider: slack, credential_ref: CI_WEBHOOK_URL}}}}", ""},
		{"{provider: router, settings: {destinations: {team: {provider: webhook, native: " + scope + "}}}}", "only the slack provider"},
		{"{provider: router, settings: {destinations: {team: {provider: slack, native: " + scope + "}, duplicate: {provider: slack, native: " + scope + "}}}}", "native scope already used"},
	} {
		dir := t.TempDir()
		configDir := filepath.Join(dir, ".devflow")
		if err := os.Mkdir(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "config.yml"), []byte("notifications: "+test.settings), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := LoadRepo(dir)
		if test.wantError == "" && err != nil || test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
			t.Errorf("notifications %s: %v, want error %q", test.settings, err, test.wantError)
		}
	}
}
