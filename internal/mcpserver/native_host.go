package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nscaledev/devflow/internal/provider/codexhost"
	"github.com/nscaledev/devflow/internal/provider/native"
)

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
