package setup

import (
	"bytes"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestProviderToolingPlan(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	cfg := strings.Replace(headlessConfig, "github-issues, settings: {repository: sample/project}", "jira", 1)
	cfg = strings.Replace(cfg, "filesystem, settings: {root: records}", "confluence", 1)
	cfg = strings.Replace(cfg, "notifications: {provider: none}", "notifications: {provider: slack}", 1)
	setupFile(t, root, ".agentflow/config.yml", cfg, 0600)
	plan := planFixture(t, root, "")
	if plan.Ready || len(plan.Tooling) != 3 {
		t.Fatalf("plan %+v", plan)
	}
	for _, tool := range plan.Tooling {
		if len(tool.Roles) != 1 || tool.MCP.Endpoint == "" {
			t.Fatalf("tool %+v", tool)
		}
		if tool.Provider == "slack" {
			if tool.MCP.Name != "slack" || tool.CLI != nil {
				t.Fatalf("Slack tool %+v", tool)
			}
		} else if tool.MCP.Name != "atlassian" || tool.CLI == nil || tool.CLI.Installed {
			t.Fatalf("Atlassian tool %+v", tool)
		}
	}
}

func TestProviderLoginCommand(t *testing.T) {
	for _, tt := range []struct {
		provider, client, server string
		want                     []string
	}{
		{"jira", "claude-desktop", "", []string{"claude", "mcp", "login", "atlassian"}},
		{"slack", "codex", "workspace-slack", []string{"codex", "mcp", "login", "workspace-slack"}},
		{"confluence", "", "", []string{"twg", "login"}},
		{"github", "", "", []string{"gh", "auth", "login", "--web"}},
		{"slack", "", "", nil},
		{"jira", "vscode", "", nil},
		{"jira", "claude", "--help", nil},
		{"jira", "", "atlassian", nil},
		{"filesystem", "", "", nil},
	} {
		got, err := providerLoginCommand(tt.provider, tt.client, tt.server)
		if (err == nil) != (tt.want != nil) || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("login %s/%s/%s: %v, %v", tt.provider, tt.client, tt.server, got, err)
		}
	}
}

func TestLoginProviderStartsHostOAuth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixture")
	}
	root := t.TempDir()
	setupFile(t, root, "bin/claude", "#!/bin/sh\nprintf '%s\\n' \"$@\"\n", 0700)
	t.Setenv("PATH", filepath.Join(root, "bin"))
	var output bytes.Buffer
	err := LoginProvider(t.Context(), root, "jira", "claude-desktop", "", strings.NewReader(""), &output, &output)
	if err != nil || output.String() != "mcp\nlogin\natlassian\n" {
		t.Fatalf("provider login did not start OAuth: %q, %v", output.String(), err)
	}
}
