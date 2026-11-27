package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// legacySettings is what a release before the marketplace rename wrote.
func legacySettings(t *testing.T) string {
	t.Helper()
	doc := legacyOverlay(claudeSettings)
	doc["includeCoAuthoredBy"] = false
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRemoveUndoesLegacyMarketplace(t *testing.T) {
	for _, reinit := range []bool{false, true} {
		t.Run(map[bool]string{false: "remove", true: "init-then-remove"}[reinit], func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			dir := gitRepo(t)
			// A client-only receipt from an earlier release: it created the file.
			setupFile(t, dir, claudeSettings, legacySettings(t), 0o600)
			setupFile(t, dir, receiptPath, "version: 1\nclient_only: true\nexisting_client_entries:\n    .claude/settings.json: []\n", 0o600)
			if reinit {
				res, err := Install(dir, []string{"claude"})
				if err != nil {
					t.Fatal(err)
				}
				if !slices.ContainsFunc(res.Notes, func(n string) bool { return strings.Contains(n, legacyPlugin) }) {
					t.Fatalf("notes = %v, want the old marketplace named", res.Notes)
				}
				data, err := os.ReadFile(filepath.Join(dir, claudeSettings))
				if err != nil || !strings.Contains(string(data), legacyPlugin) {
					t.Fatalf("init must keep the old entries: %s, %v", data, err)
				}
			}
			if _, err := Remove(t.Context(), dir, RemoveOptions{}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, claudeSettings))
			if err == nil && strings.Contains(string(data), legacyMarketplace) {
				t.Fatalf("old entries remain: %s", data)
			}
		})
	}
}
