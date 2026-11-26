package mcpserver

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/goldentest"
)

// TestToolsGolden pins every tool's name, description, annotations and input
// schema, so deriving the tool list from the command registry (ADR, phase 3)
// cannot change any of them unnoticed. Tool names and input fields are a
// compatibility contract. Regenerate with
//
//	go test ./internal/mcpserver -run TestToolsGolden -update
//
// and review the diff.
func TestToolsGolden(t *testing.T) {
	goldentest.Isolate(t)
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	dir := gitRepo(t)
	ctx := context.Background()
	s, err := New(ctx, dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = s.MCP().Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := res.Tools
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	b, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Check(t, "tools.json", string(b))
}
