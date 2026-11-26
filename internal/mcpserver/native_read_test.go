package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/provider/native"
)

func TestJiraWorkUsesClientProviderMCP(t *testing.T) {
	ctx := t.Context()
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("JIRA_USER", "")
	t.Setenv("JIRA_TOKEN", "")
	dir := gitRepo(t)
	cfg := `agentflow: 1
task_management:
  provider: jira
  settings: {site: "https://test-tenant.atlassian.net", projects: ["APP"]}
`
	if err := os.WriteFile(filepath.Join(dir, ".agentflow/config.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := New(ctx, dir, "claude")
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = server.MCP().Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	call := func(replies ...native.Reply) (string, bool) {
		t.Helper()
		args := map[string]any{}
		if len(replies) > 0 {
			args["native_results"] = replies
		}
		resp, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "agentflow_my_work", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Content[0].(*mcp.TextContent).Text, resp.IsError
	}
	text, isError := call()
	var req native.Request
	if err := json.Unmarshal([]byte(text), &req); isError || err != nil ||
		req.Status != "native_read_required" || req.Server != "atlassian" ||
		req.Tool != "searchJiraIssuesUsingJql" || req.Arguments["cloudId"] != "https://test-tenant.atlassian.net" {
		t.Fatalf("native request: %s, %v", text, err)
	}
	text, isError = call(native.Reply{ID: req.ID, Status: "ok", Result: json.RawMessage(
		`{"structuredContent":{"issues":{"nodes":[{"key":"APP-7","fields":{"summary":"Example","status":{"name":"Open"},"issuetype":{"name":"Task"}}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`)})
	var items []app.WorkItem
	if err := json.Unmarshal([]byte(text), &items); isError || err != nil || len(items) != 1 || items[0].Key != "APP-7" {
		t.Fatalf("mapped work: %s, %v", text, err)
	}
	text, isError = call(native.Reply{ID: req.ID, Status: "access_denied"})
	if !isError || !strings.Contains(text, "access denied") {
		t.Fatalf("denied: %s", text)
	}
	if runtime.GOOS == "windows" {
		return
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TWG_TOKEN", "test-override-must-be-removed")
	twg := `#!/bin/sh
if [ -n "${TWG_TOKEN:-}" ] || [ -n "${JIRA_TOKEN:-}" ]; then
  exit 1
fi
printf '%s\n' '{"data":{"issues":[{"key":"APP-8","fields":{"summary":"CLI result","status":{"name":"Open"},"issuetype":{"name":"Task"}}}]}}'
`
	if err := os.WriteFile(filepath.Join(bin, "twg"), []byte(twg), 0700); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"unavailable", "sign_in_required", "connection_failed", "oauth_app_restricted"} {
		text, isError = call(native.Reply{ID: req.ID, Status: status})
		items = nil
		if err := json.Unmarshal([]byte(text), &items); isError || err != nil || len(items) != 1 || items[0].Key != "APP-8" {
			t.Fatalf("vendor CLI fallback after %s: %s, %v", status, text, err)
		}
	}
}
