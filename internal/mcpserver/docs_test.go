package mcpserver

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/agentflow/internal/app"
	"github.com/nscaledev/agentflow/internal/policy"
	"github.com/nscaledev/agentflow/internal/provider"
)

func TestRepositoryDocsTools(t *testing.T) {
	ctx := t.Context()
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	dir := gitRepo(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	cmd := exec.CommandContext(ctx, "git", "clone", "--bare", dir, bare)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v: %s", err, out)
	}
	cfg := "agentflow: 1\nknowledge_base:\n  provider: repository\n  settings:\n    publication: push\n    repository: " + bare + "\n    branch: PLAT-123-gpu-quota\n    directory: handbook\n"
	if err := os.WriteFile(filepath.Join(dir, ".agentflow/config.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(ctx, dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = s.MCP().Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var readOnlyGet bool
	for _, tool := range list.Tools {
		if tool.Name == "agentflow_docs_get" {
			readOnlyGet = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		}
	}
	if !readOnlyGet {
		t.Fatal("expected read-only agentflow_docs_get")
	}
	call := func(name string, args map[string]any, result any) {
		t.Helper()
		resp, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		text := resp.Content[0].(*mcp.TextContent).Text
		if resp.IsError {
			t.Fatalf("%s: %s", name, text)
		}
		if err := json.Unmarshal([]byte(text), result); err != nil {
			t.Fatal(err)
		}
	}
	ref := provider.Ref{Instance: "repository", Scope: "engineering", ID: "quota"}
	args := map[string]any{"ref": ref, "title": "Quota", "body": "Capacity decision", "operation_id": "create"}
	var pending app.Pending
	call("agentflow_docs_publish", args, &pending)
	if pending.Status != "needs_confirmation" || pending.ConfirmToken == "" || pending.Summary.(map[string]any)["format"] != "okf" {
		t.Fatalf("pending: %+v", pending)
	}
	args["type"], args["description"], args["tags"] = "Runbook", "GPU quota policy", []string{"gpu", "ops"}
	args["confirm_token"] = pending.ConfirmToken
	denied, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "agentflow_docs_publish", Arguments: args})
	if err != nil || !denied.IsError || !strings.Contains(denied.Content[0].(*mcp.TextContent).Text, policy.ErrToken.Error()) {
		t.Fatalf("changed metadata accepted with original approval: %+v, %v", denied, err)
	}
	delete(args, "confirm_token")
	call("agentflow_docs_publish", args, &pending)
	args["confirm_token"] = pending.ConfirmToken
	var result provider.WriteResult
	call("agentflow_docs_publish", args, &result)
	var search struct {
		Pages []provider.Document
		Note  string
	}
	call("agentflow_docs_search", map[string]any{"query": "capacity", "space": "engineering"}, &search)
	if result.Status != provider.Applied || search.Note == "" || len(search.Pages) != 1 || search.Pages[0].Ref != ref || search.Pages[0].Revision != result.Revision ||
		search.Pages[0].Type != "Runbook" || search.Pages[0].Description != "GPU quota policy" || !slices.Equal(search.Pages[0].Tags, []string{"gpu", "ops"}) {
		t.Fatalf("publish %+v, search %+v", result, search)
	}
}
