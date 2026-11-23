package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nscaledev/devflow/internal/provider/codexhost"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/setup"
)

const nativeInstructions = `For native_read_required, first find the requested operation in the current client's provider MCP. Use the exact arguments and retry the original Devflow tool with native_results containing the request ID, status, and provider JSON. When the connector is missing, retry with unavailable so the read can use its fallback. Present setup_offer.prompt_if_missing to the user once per provider per session, even when the fallback succeeds. If a connected provider lacks only the operation, use unavailable. Report access_denied for access restrictions. Ask for consent before registering a missing connector. On approval, run setup_offer.register_args with devflow when present. Otherwise use the current client's setup action with the official connector details. Prefer provider OAuth whenever the provider and client support it. Start native sign-in when needed and let the user complete consent. For a newly registered local GitHub connector in Claude Code, retry the requested GitHub read in a fresh session to launch browser OAuth. Never offer to extract a CLI token, write credentials into client configuration, or ask the user to export credentials. If the client blocks initiation, show its specific Connect or Sign in action. Keep credentials in the provider's sign-in flow. Retry the read after consent and request a fresh preview for writes.`

type connectorSetupOffer struct {
	setup.MCPTool
	PromptIfMissing string `json:"prompt_if_missing"`
}

type nativeReadResponse struct {
	*native.Request
	SetupOffer *connectorSetupOffer `json:"setup_offer,omitempty"`
}

func (s *Server) nativeReadResponse(req *native.Request) nativeReadResponse {
	response := nativeReadResponse{Request: req}
	if tool := setup.ProviderMCP(req.Provider, s.app.Client); tool.Name != "" {
		response.SetupOffer = &connectorSetupOffer{
			MCPTool:         tool,
			PromptIfMissing: fmt.Sprintf("Would you like me to connect %s MCP in this client?", tool.Name),
		}
	}
	return response
}

func (s *Server) interactiveClient() bool {
	switch s.app.Client {
	case "claude", "claude-desktop", "codex", "cursor", "gemini", "grok", "vscode":
		return true
	default:
		return false
	}
}

func (s *Server) providerContext(ctx context.Context, input nativeReadIn) context.Context {
	if !input.ProviderMCP && len(input.NativeResults) == 0 && !s.interactiveClient() {
		return ctx
	}
	client := native.Client{Replies: input.NativeResults}
	if s.app.Client == "codex" {
		client.Direct = codexProviderHost{dir: s.app.Repo.Dir}
	}
	return native.WithHost(ctx, client)
}

type codexProviderHost struct{ dir string }

func (h codexProviderHost) Read(ctx context.Context, call native.Call) (json.RawMessage, error) {
	raw, err := codexhost.Call(ctx, h.dir, call.Server, call.Tool, call.Arguments)
	var hostError *codexhost.Error
	if !errors.As(err, &hostError) {
		return raw, err
	}
	switch hostError.Kind {
	case codexhost.Unavailable:
		return nil, native.ErrUnavailable
	case codexhost.Authentication:
		return nil, fmt.Errorf("%s: %w; start the client's provider OAuth flow", call.Provider, native.ErrSignIn)
	case codexhost.Permission:
		return nil, fmt.Errorf("%s: %w", call.Provider, native.ErrDenied)
	default:
		return nil, err
	}
}
