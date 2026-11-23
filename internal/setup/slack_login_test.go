package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSlackWorkspaceLogin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixture")
	}
	const (
		list     = "mcp list\n"
		add      = "mcp add --transport http --scope local --client-id 1601185624273.8899143856786 --callback-port 3118 slack https://mcp.slack.com/mcp\n"
		login    = "mcp login slack\n"
		absent   = "Checking MCP server health…\nother: example - ✔ Connected"
		official = "https://mcp.slack.com/mcp (HTTP) - "
	)
	for _, tc := range []struct {
		test, inventory, server    string
		headless                   bool
		fail, wantCalls, wantError string
	}{
		{"absent", absent, "", false, "", list + add + login, ""},
		{"empty workspace", "No MCP servers configured. Use claude mcp add to add a server.", "", true, "", list + add, "sign-in deferred"},
		{"connected alias", "plugin:team:slack: " + official + "✔ Connected", "", true, "", list, ""},
		{"configured", "slack: custom - ! Needs authentication", "", false, "", list + login, ""},
		{"named", "team: custom - ! Needs authentication", "team", false, "", list + "mcp login team\n", ""},
		{"missing alias", absent, "team", false, "", list, "absent"},
		{"existing incompatible", "slack: " + official + "✘ Failed to connect — Incompatible auth server", "", false, "", list, "existing"},
		{"denied", "slack: " + official + "✘ Failed to connect — 403 Forbidden", "", false, "", list, "access denied"},
		{"unknown inventory", "unexpected format", "", false, "", list, "inventory"},
		{"ambiguous", "slack: custom - ✔ Connected\nteam: " + official + "✔ Connected", "", false, "", list, "multiple"},
		{"failed list", absent, "", false, "list", list, "list Claude"},
		{"failed add", absent, "", false, "add", list + add, "register Slack"},
		{"failed login", absent, "", false, "login", list + add + login, "sign-in via claude"},
	} {
		t.Run(tc.test, func(t *testing.T) {
			root := t.TempDir()
			setupFile(t, root, "bin/claude", `#!/bin/sh
printf '%s\n' "$*" >> calls
if [ "$2" = "$FAIL" ]; then exit 1; fi
if [ "$2" = list ]; then printf '%s\n' "$INVENTORY"; fi
`, 0700)
			t.Setenv("PATH", filepath.Join(root, "bin"))
			t.Setenv("FAIL", tc.fail)
			t.Setenv("INVENTORY", tc.inventory)
			var out strings.Builder
			err := RegisterProviderLogin(t.Context(), root, "slack", claudeDesktop, tc.server, "", !tc.headless, nil, &out, &out)
			if (err == nil) != (tc.wantError == "") || err != nil && !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("want error containing %q, got %v", tc.wantError, err)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil || string(calls) != tc.wantCalls {
				t.Fatalf("calls %q, err %v", calls, err)
			}
		})
	}
}

func TestRegisterProviderLoginUnsupported(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, pair := range [][2]string{{"slack", "codex"}, {"slack", "claude"}, {"jira", claudeDesktop}} {
		err := RegisterProviderLogin(t.Context(), ".", pair[0], pair[1], "", "", false, nil, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "--register supports only") {
			t.Fatalf("%v: %v", pair, err)
		}
	}
}
