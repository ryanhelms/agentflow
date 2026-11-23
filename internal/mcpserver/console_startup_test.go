package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestUIStartupIntentOverMCP(t *testing.T) {
	// Run the same behavior in CI with an injected opener. Actual CI processes
	// are covered separately and are always suppressed by the production guard.
	for _, key := range []string{"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "CLAUDE_CODE_REMOTE", "SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY", "AGENT_COMMANDER_WORKER_ID", "AGENT_COMMANDER_TOKEN", "AGENT_COMMANDER_SOCKET", "AGENT_COMMANDER_ROLE", "AGENT_COMMANDER_FOCUSED_PROOF", "CLAUDE_INTERNAL_ASSISTANT_TEAM_NAME"} {
		t.Setenv(key, "")
	}
	t.Setenv("DISPLAY", ":fixture")
	dir := gitRepo(t)
	for _, tc := range []struct {
		name, peer, suppress string
		explicit, want       bool
	}{
		{"lead", "claude-code", "", true, true},
		{"legacy-lead", "claude-code", "", true, true},
		{"ordinary-discovery", "claude-code", "", false, false},
		{"commander-probe", "agent-commander-discovery", "", false, false},
		{"unrecognized-client", "agent-commander-discovery", "", true, false},
		{"ci", "claude-code", "CI", true, false},
		{"worker", "claude-code", "AGENT_COMMANDER_WORKER_ID", true, false},
		{"proof", "claude-code", "AGENT_COMMANDER_FOCUSED_PROOF", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.suppress != "" {
				t.Setenv(tc.suppress, "1")
			}
			ctx := t.Context()
			server, err := newUIOnly(ctx, dir)
			if err != nil {
				t.Fatal(err)
			}
			opened := make(chan string, 1)
			server.enableConsoleStartup(&consoleStartup{ctx: ctx, root: dir, client: "agent-commander", uiOnly: true, explicit: tc.explicit,
				open: func(_ context.Context, root string) error { opened <- root; return nil }})
			st, ct := mcp.NewInMemoryTransports()
			go func() { _ = server.MCP().Run(ctx, st) }()
			client := mcp.NewClient(&mcp.Implementation{Name: tc.peer, Version: "test"}, nil)
			var opts *mcp.ClientSessionOptions
			if tc.name == "legacy-lead" {
				opts = &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"}
			}
			connection, err := client.Connect(ctx, ct, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if _, err := connection.ListTools(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if tc.want {
				select {
				case root := <-opened:
					if root != dir {
						t.Fatal("opened wrong repository")
					}
				case <-time.After(time.Second):
					t.Fatal("did not open on coordinator startup")
				}
			} else {
				select {
				case <-opened:
					t.Fatal("unexpected automatic open")
				case <-time.After(25 * time.Millisecond):
				}
			}
		})
	}
}

func TestScopedCoordinatorStartup(t *testing.T) {
	for _, key := range []string{"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "CLAUDE_CODE_REMOTE", "SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY", "AGENT_COMMANDER_FOCUSED_PROOF", "CLAUDE_INTERNAL_ASSISTANT_TEAM_NAME"} {
		t.Setenv(key, "")
	}
	t.Setenv("DISPLAY", ":fixture")
	t.Setenv("AGENT_COMMANDER_WORKER_ID", "test-lead")
	t.Setenv("AGENT_COMMANDER_SOCKET", t.TempDir())
	for _, tt := range []struct {
		name, client, peer, role, token string
		uiOnly, explicit, want          bool
	}{
		{"bound lead", "agent-commander", "claude-code", "coordinator", "scoped", true, true, true},
		{"other peer", "agent-commander", "agent-commander-discovery", "coordinator", "scoped", true, true, false},
		{"discovery", "agent-commander", "claude-code", "coordinator", "scoped", true, false, false},
		{"delegated", "agent-commander", "claude-code", "worker", "scoped", true, true, false},
		{"incomplete binding", "agent-commander", "claude-code", "coordinator", "", true, true, false},
		{"full server", "agent-commander", "claude-code", "coordinator", "scoped", false, true, false},
		{"different harness", "claude", "claude-code", "coordinator", "scoped", true, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENT_COMMANDER_TOKEN", tt.token)
			t.Setenv("AGENT_COMMANDER_ROLE", tt.role)
			opened := make(chan struct{}, 1)
			start := consoleStartup{ctx: t.Context(), client: tt.client, uiOnly: tt.uiOnly, explicit: tt.explicit,
				open: func(context.Context, string) error { opened <- struct{}{}; return nil }}
			start.schedule(&mcp.Implementation{Name: tt.peer})
			select {
			case <-opened:
				if !tt.want {
					t.Fatal("unexpected automatic open")
				}
			case <-time.After(100 * time.Millisecond):
				if tt.want {
					t.Fatal("did not open for the scoped coordinator")
				}
			}
		})
	}
}
