package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

func sessionLookup(t *testing.T, worktree string, ended bool) {
	t.Helper()
	old := app.SessionLookup
	t.Cleanup(func() { app.SessionLookup = old })
	app.SessionLookup = func(context.Context, string, string) (string, bool, error) { return worktree, ended, nil }
}

// The MCP server passed to a harness lists exactly the read-only allowlist,
// and runs on the session's own read-only client.
func TestSessionServerListsOnlyReadTools(t *testing.T) {
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv(app.SessionEnv, "")
	dir := gitRepo(t)
	// Even an autonomy of auto must not let a session write.
	_ = os.WriteFile(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "config.yml"), []byte("autonomy: {local: auto, remote: auto}\n"), 0o600)
	ctx := context.Background()

	sessionLookup(t, dir, false)
	s, err := NewSession(ctx, dir, "ais_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.app.Load().Client; got != policy.SessionClientPrefix+"ais_0123456789abcdef0123456789abcdef" {
		t.Fatalf("client %q", got)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = s.MCP().Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := slices.Clone(SessionTools)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("session tools:\n got %v\nwant %v", names, want)
	}
	// commit_context stages paths: it is not a read, so a session never gets it.
	if slices.Contains(names, "agentflow_commit_context") {
		t.Fatal("a session server lists agentflow_commit_context")
	}
	for _, name := range names {
		if strings.HasPrefix(name, "agentflow_ai_") {
			t.Fatalf("a session server lists %s", name)
		}
	}
	// A read works on the session's client.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agentflow_policy_explain", Arguments: map[string]any{"action": "commit"}})
	if err != nil || res.IsError {
		t.Fatalf("policy explain: %v %+v", err, res)
	}
	// A write through the session's App is refused whatever the autonomy.
	if _, err := s.app.Load().Gate.Check(policy.Local, "commit", map[string]any{"a": 1}, "", false); err == nil || policy.ErrorCode(err) != "session_read_only" {
		t.Fatalf("session write: %v", err)
	}
}

// Every allowlisted tool is a registered read, so the server never needs a
// legacy tool or a write.
func TestSessionToolsAreRegisteredReads(t *testing.T) {
	for _, name := range SessionTools {
		c, ok := command.Default.Tool(name)
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if c.Meta().Class != policy.Read {
			t.Errorf("%s has class %q", name, c.Meta().Class)
		}
	}
}

// The session server opens only for a running session whose worktree holds
// the server's directory.
func TestSessionServerRefusesUnknownSessions(t *testing.T) {
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv(app.SessionEnv, "")
	dir := gitRepo(t)
	ctx := context.Background()
	sessionLookup(t, dir, true)
	if _, err := NewSession(ctx, dir, "ais_1"); err == nil {
		t.Fatal("served an ended session")
	}
	sessionLookup(t, t.TempDir(), false)
	if _, err := NewSession(ctx, dir, "ais_1"); err == nil {
		t.Fatal("served a session of another worktree")
	}
	if _, err := New(ctx, dir, "ai-session:ais_1"); err == nil {
		t.Fatal("an ordinary server took the session prefix")
	}
}
