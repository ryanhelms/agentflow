package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLegacyProviderToolFields(t *testing.T) {
	t.Setenv("DEVFLOW_HOME", t.TempDir())
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
		"devflow_context":          {"key"},
		"devflow_start":            {"key", "worktree", "confirm_token"},
		"devflow_transition":       {"key", "event", "dry_run", "confirm_token"},
		"devflow_ticket_create":    {"project", "type", "summary", "description", "link_to", "confirm_token"},
		"devflow_jira_remote_link": {"key", "url", "title", "confirm_token"},
		"devflow_ac_update":        {"key", "item", "done", "evidence", "post", "confirm_token"},
		"devflow_docs_search":      {"query", "space"},
		"devflow_pr_open":          {"title", "body", "draft", "reviewers", "labels", "confirm_token"},
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
