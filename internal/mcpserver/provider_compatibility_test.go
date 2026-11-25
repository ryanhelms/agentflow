package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLegacyProviderToolFields(t *testing.T) {
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	ctx := t.Context()
	server, err := New(ctx, gitRepo(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = server.MCP().Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "compatibility", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string][]string{
		"agentflow_context":          {"key"},
		"agentflow_start":            {"key", "worktree", "confirm_token"},
		"agentflow_transition":       {"key", "event", "dry_run", "confirm_token"},
		"agentflow_ticket_create":    {"project", "type", "summary", "description", "link_to", "confirm_token"},
		"agentflow_jira_remote_link": {"key", "url", "title", "confirm_token"},
		"agentflow_ac_update":        {"key", "item", "done", "evidence", "post", "confirm_token"},
		"agentflow_docs_publish":     {"ref", "title", "body", "operation_id", "links", "expected_revision", "confirm_token", "type", "description", "tags"},
		"agentflow_docs_search":      {"query", "space"},
		"agentflow_pr_open":          {"title", "body", "draft", "reviewers", "labels", "confirm_token"},
	}
	for _, tool := range result.Tools {
		fields, ok := expected[tool.Name]
		if !ok {
			continue
		}
		delete(expected, tool.Name)
		body, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(body, &schema); err != nil {
			t.Fatal(err)
		}
		for _, field := range fields {
			if _, ok := schema.Properties[field]; !ok {
				t.Errorf("%s removed input field %s", tool.Name, field)
			}
		}
	}
	for name := range expected {
		t.Errorf("removed legacy tool %s", name)
	}
}

func TestMCPReportsUnassignedTaskAndKnowledge(t *testing.T) {
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	ctx := t.Context()
	dir := gitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".agentflow", "config.yml"), []byte("agentflow: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := New(ctx, dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = server.MCP().Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "unassigned", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	for _, check := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"agentflow_context", map[string]any{"key": "PLAT-123"}, "task management is disabled"},
		{"agentflow_docs_search", map[string]any{"query": "fixture"}, "none: document search"},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: check.tool, Arguments: check.args})
		if err != nil {
			t.Fatalf("%s: %v", check.tool, err)
		}
		if len(result.Content) != 1 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, check.want) {
			t.Fatalf("%s: %+v", check.tool, result)
		}
	}
}
