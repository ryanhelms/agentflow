package mcpserver

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/provider"
)

func TestRepositoryDocsTools(t *testing.T) {
	ctx := t.Context()
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	dir := gitRepo(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	cmd := exec.CommandContext(ctx, "git", "clone", "--bare", dir, bare)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v: %s", err, out)
	}
	cfg := "devflow: 1\nknowledge_base:\n  provider: repository\n  settings:\n    publication: push\n    repository: " + bare + "\n    branch: PLAT-123-gpu-quota\n    directory: handbook\n"
	if err := os.WriteFile(filepath.Join(dir, ".devflow/config.yml"), []byte(cfg), 0600); err != nil {
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
	call("devflow_docs_publish", args, &pending)
	if pending.Status != "needs_confirmation" || pending.ConfirmToken == "" {
		t.Fatalf("pending: %+v", pending)
	}
	args["confirm_token"] = pending.ConfirmToken
	var result provider.WriteResult
	call("devflow_docs_publish", args, &result)
	var search struct {
		Pages []provider.Document
		Note  string
	}
	call("devflow_docs_search", map[string]any{"query": "capacity", "space": "engineering"}, &search)
	if result.Status != provider.Applied || search.Note == "" || len(search.Pages) != 1 || search.Pages[0].Ref != ref || search.Pages[0].Revision != result.Revision {
		t.Fatalf("publish %+v, search %+v", result, search)
	}
}
