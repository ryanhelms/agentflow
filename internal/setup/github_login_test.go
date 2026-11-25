package setup

import (
	"reflect"
	"strings"
	"testing"
)

func TestGitHubOffersLocalOAuthForClaude(t *testing.T) {
	for _, client := range []string{"claude", claudeDesktop} {
		tool := ProviderMCP("github-issues", client)
		args := "setup login --provider github-issues --client " + client + " --register"
		if tool.Endpoint != "" || tool.Guide != githubOAuthGuide || strings.Join(tool.RegisterArgs, " ") != args {
			t.Fatalf("%s: %+v", client, tool)
		}
	}
	if tool := ProviderMCP("github-issues", "vscode"); tool.Endpoint != githubMCP.Endpoint || len(tool.RegisterArgs) != 0 {
		t.Fatalf("remote connector: %+v", tool)
	}
	entry := githubOAuthEntry("/pinned/github-mcp-server")
	args := []string{"stdio", "--gh-host", "https://github.com", "--toolsets", "issues", "--read-only"}
	if entry["type"] != "stdio" || entry["command"] != "/pinned/github-mcp-server" || !reflect.DeepEqual(entry["args"], args) {
		t.Fatalf("OAuth entry: %+v", entry)
	}
	env := entry["env"].(map[string]string)
	for _, name := range []string{"GITHUB_PERSONAL_ACCESS_TOKEN", "GITHUB_APP_ID", "GITHUB_OAUTH_CLIENT_ID"} {
		if value, ok := env[name]; !ok || value != "" {
			t.Fatalf("expected %s to be cleared", name)
		}
	}
}
