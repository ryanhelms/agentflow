package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAsanaWorkspaceRegistration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixture")
	}
	const (
		clientID = "12345678"
		endpoint = "https://mcp.asana.com/v2/mcp"
		list     = "mcp list\n"
		add      = "mcp add --transport http --scope local --client-id " + clientID + " --client-secret --callback-port 8080 "
		absent   = "No MCP servers configured. Use claude mcp add to add a server."
	)
	needsAuth := "asana: " + endpoint + " (HTTP) - ! Needs authentication"
	connected := "asana: " + endpoint + " (HTTP) - ✔ Connected"
	for _, tc := range []struct {
		name, before, after, server, client, calls, problem string
		tty                                                 bool
	}{
		{name: "register and start OAuth", before: absent, after: needsAuth, client: clientID, tty: true, calls: list + add + "asana " + endpoint + "\n" + list + "mcp login asana\n"},
		{name: "add already authenticated", before: absent, after: connected, client: clientID, tty: true, calls: list + add + "asana " + endpoint + "\n" + list},
		{name: "reuse named registration", before: "team-asana: " + endpoint + " - ✔ Connected", server: "team-asana", calls: list},
		{name: "start configured OAuth", before: needsAuth, tty: true, calls: list + "mcp login asana\n"},
		{name: "owner denial", before: "asana: " + endpoint + " - ✗ Failed — 403 Forbidden", tty: true, calls: list, problem: "access denied"},
		{name: "deny after registration", before: absent, after: "asana: " + endpoint + " - ✗ 403 Forbidden", client: clientID, tty: true, calls: list + add + "asana " + endpoint + "\n" + list, problem: "access denied"},
		{name: "retired connector", before: "asana: https://mcp.asana.com/sse (SSE) - ! Needs authentication", client: clientID, tty: true, calls: list, problem: "official Asana V2"},
		{name: "unrelated named connector", before: "team: https://other.example.org/mcp - ✔ Connected", server: "team", calls: list, problem: "different endpoint"},
		{name: "missing approved registration", before: absent, tty: true, calls: list, problem: "approved Asana MCP app"},
		{name: "noninteractive registration", before: absent, client: clientID, calls: list, problem: "interactive terminal"},
		{name: "noninteractive login", before: needsAuth, calls: list, problem: "sign-in deferred"},
		{name: "invalid app ID", client: "--client-secret", tty: true, problem: "public OAuth client ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			setupFile(t, root, "bin/claude", `#!/bin/sh
printf '%s\n' "$*" >> calls
test -z "${MCP_CLIENT_SECRET+x}${ASANA_CLIENT_ID+x}${ASANA_CLIENT_SECRET+x}${ASANA_ACCESS_TOKEN+x}" || exit 18
case "$2" in
  list) if [ -f registered ]; then printf '%s\n' "$AFTER"; else printf '%s\n' "$BEFORE"; fi ;;
  add) IFS= read -r secret; test "$secret" = "fixture-hidden-prompt" || exit 19; : > registered ;;
  login) exit 0 ;;
  *) exit 1 ;;
esac
`, 0700)
			t.Setenv("PATH", filepath.Join(root, "bin"))
			t.Setenv("MCP_CLIENT_SECRET", "fixture-unrelated-value")
			t.Setenv("ASANA_CLIENT_ID", "fixture-unrelated-value")
			t.Setenv("ASANA_CLIENT_SECRET", "fixture-unrelated-value")
			t.Setenv("ASANA_ACCESS_TOKEN", "fixture-unrelated-value")
			t.Setenv("BEFORE", tc.before)
			t.Setenv("AFTER", tc.after)
			var out strings.Builder
			err := RegisterProviderLogin(t.Context(), root, "asana", claudeDesktop, tc.server, tc.client, tc.tty, strings.NewReader("fixture-hidden-prompt\n"), &out, &out)
			if (err == nil) != (tc.problem == "") || err != nil && !strings.Contains(err.Error(), tc.problem) {
				t.Fatalf("wanted %q, got %v", tc.problem, err)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil && (tc.calls != "" || !os.IsNotExist(err)) || string(calls) != tc.calls {
				t.Fatalf("calls %q, want %q, err %v", calls, tc.calls, err)
			}
			if strings.Contains(out.String(), "fixture-hidden-prompt") {
				t.Fatal("the app prompt was exposed")
			}
		})
	}
}

func TestAsanaRegistrationInDesktopPlan(t *testing.T) {
	for _, client := range []string{claudeDesktop, "codex", ""} {
		plan := &ProviderSetupPlan{}
		plan.addProviderTooling("task_management", "asana", client, nil)
		args := strings.Join(plan.Tooling[0].MCP.RegisterArgs, " ")
		if client == claudeDesktop {
			if args != "setup login --provider asana --client claude-desktop --register" {
				t.Fatal(args)
			}
		} else if args != "" {
			t.Fatalf("unexpected Claude registration command for %q", client)
		}
	}
}
