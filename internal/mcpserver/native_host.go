package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/managedcli"
	"github.com/ryanhelms/agentflow/internal/provider/codexhost"
	"github.com/ryanhelms/agentflow/internal/provider/native"
	"github.com/ryanhelms/agentflow/internal/setup"
)

const nativeInstructions = `For native_read_required, use the requested operation and exact arguments in the current client's provider MCP. Resume with native_results and the request ID. Prefer provider OAuth when sign-in is needed. If the connector is absent, use unavailable. If OAuth cannot complete, use sign_in_required. Use oauth_app_restricted only when the vendor explicitly reports an OAuth app registration or organization approval restriction. Use connection_failed for connector transport failures. Report access_denied for denied user or resource permissions. Agentflow tries supported vendor CLIs automatically with their own saved login, including installing and checking a pinned release. When the fallback succeeds, finish the request without prompting about setup. If it fails, present the relevant setup offer or start the vendor's native sign-in and let the user complete consent. Ask before registering an MCP connector. On approval, run setup_offer.register_args with agentflow when present. For a new local GitHub connector in Claude Code, retry the requested GitHub read in a fresh session to launch browser OAuth. Keep credentials in the provider's sign-in flow and request a fresh preview for writes.`

type connectorSetupOffer struct {
	setup.MCPTool
	PromptIfMissing string `json:"prompt_if_missing"`
}

type nativeReadResponse struct {
	*native.Request
	CLIFallback string               `json:"cli_fallback,omitempty"`
	SetupOffer  *connectorSetupOffer `json:"setup_offer,omitempty"`
}

func newNativeReadResponse(a *app.App, req *native.Request) nativeReadResponse {
	response := nativeReadResponse{Request: req}
	if tool, ok := managedcli.ForProvider(req.Provider); ok {
		response.CLIFallback = tool.Command
	}
	if tool := setup.ProviderMCP(req.Provider, a.Client); tool.Name != "" {
		response.SetupOffer = &connectorSetupOffer{
			MCPTool:         tool,
			PromptIfMissing: fmt.Sprintf("Would you like me to connect %s MCP in this client?", tool.Name),
		}
	}
	return response
}

func interactiveClient(a *app.App) bool {
	switch a.Client {
	case "claude", "claude-desktop", "codex", "cursor", "gemini", "grok", "vscode":
		return true
	default:
		return false
	}
}

func providerContext(ctx context.Context, a *app.App, input nativeReadIn) context.Context {
	if !input.ProviderMCP && len(input.NativeResults) == 0 && !interactiveClient(a) {
		return ctx
	}
	client := native.Client{Replies: input.NativeResults}
	if a.Client == "codex" {
		client.Direct = codexProviderHost{dir: a.Repo.Dir}
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
