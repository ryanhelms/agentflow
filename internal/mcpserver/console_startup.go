package mcpserver

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/webui"
)

// One stdio server belongs to one harness connection. Initialization confirms
// the identity on the other end before optional desktop work is scheduled.
type consoleStartup struct {
	ctx      context.Context
	root     string
	client   string
	uiOnly   bool
	explicit bool
	once     sync.Once
	open     func(context.Context, string) error
}

func (s *Server) enableConsoleStartup(start *consoleStartup) {
	s.startup = start
	// As of the 2026-07-28 protocol the handshake is server/discover. It
	// does not emit notifications/initialized. Use the same host checks for
	// both handshakes, after the SDK has accepted the connection.
	s.mcp.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if request, ok := req.(*mcp.ServerRequest[*mcp.DiscoverParams]); ok && err == nil {
				start.schedule(request.ClientInfo())
			}
			return result, err
		}
	})
}

func (s *Server) initialized(_ context.Context, req *mcp.InitializedRequest) {
	s.startup.schedule(req.ClientInfo())
}

func (start *consoleStartup) schedule(peer *mcp.Implementation) {
	if start == nil {
		return
	}
	coordinator := start.client == "agent-commander" && start.uiOnly && start.explicit
	if peer == nil || !startupPeer(start.client, peer.Name) || webui.AutoOpenSuppressed(coordinator) {
		return
	}
	if start.uiOnly && !coordinator {
		return
	}
	start.once.Do(func() {
		go func() {
			ctx, cancel := context.WithTimeout(start.ctx, 25*time.Second)
			defer cancel()
			if !coordinator && !webui.InteractiveHarness(ctx, start.client, false) {
				return
			}
			// The explicit tool remains available if the desktop is unavailable.
			// An automatic launch must never block or write to MCP stdout.
			_ = start.open(ctx, start.root)
		}()
	})
}

// A capability verifier, test transport or arbitrary SDK client cannot turn
// a tool listing into a browser launch merely by passing --client.
func startupPeer(client, peer string) bool {
	peer = strings.ToLower(peer)
	switch client {
	case "agent-commander":
		return peer == "claude-code" || peer == "agent-commander"
	case "claude", "claude-desktop":
		return peer == "claude-code" || peer == "claude-desktop"
	case "codex":
		return peer == "codex-mcp-client"
	case "cursor":
		return peer == "cursor" || peer == "cursor-vscode" || peer == "cursor-agent"
	case "vscode":
		return peer == "visual studio code" || peer == "visual studio code - insiders" || peer == "vscode" || peer == "github copilot"
	case "gemini":
		return peer == "gemini-cli-mcp-client"
	case "grok":
		return peer == "grok" || peer == "grok-cli" || peer == "grok-mcp-client"
	default:
		return false
	}
}
