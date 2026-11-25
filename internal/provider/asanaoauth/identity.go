package asanaoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/agentflow/internal/provider/native"
)

// The enrollment's only resource operation is get_me. The SDK owns the
// Streamable HTTP handshake and accepts both JSON and finite POST event streams.
func (e *enrollment) verifyIdentity(ctx context.Context, token, workspace string) (string, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "agentflow", Version: "1"}, &mcp.ClientOptions{
		Logger: slog.New(slog.DiscardHandler), Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	httpClient := *e.client
	httpClient.Transport = identityTransport{httpClient.Transport, token}
	conn, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: Endpoint, HTTPClient: &httpClient, DisableStandaloneSSE: true, MaxRetries: -1}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		return "", identityError(ctx, err)
	}
	defer conn.Close()
	out, err := conn.CallTool(ctx, &mcp.CallToolParams{Name: "get_me", Arguments: map[string]any{}})
	if err != nil {
		return "", identityError(ctx, err)
	}
	if out.IsError || out.NeedsInput() {
		// The tool error can contain arbitrary provider text and does not
		// establish an authorization decision. HTTP denials are typed below.
		return "", errResponse
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", errResponse
	}
	type ref struct {
		GID string `json:"gid"`
	}
	type profile struct {
		GID        string `json:"gid"`
		Workspaces []ref  `json:"workspaces"`
	}
	var result struct {
		profile
		Data *profile `json:"data"`
	}
	if native.Decode(raw, &result) != nil {
		return "", errResponse
	}
	p := result.profile
	if result.Data != nil {
		if p.GID != "" || p.Workspaces != nil {
			return "", errResponse
		}
		p = *result.Data
	}
	if !gid.MatchString(p.GID) || p.Workspaces == nil {
		return "", errResponse
	}
	if !slices.ContainsFunc(p.Workspaces, func(w ref) bool { return w.GID == workspace }) {
		return "", ErrGrantChanged
	}
	return p.GID, nil
}

func identityError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, safe := range []error{ErrDenied, ErrTransport, errResponse} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return errResponse
}

type identityTransport struct {
	base  http.RoundTripper
	token string
}

func (t identityTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != Endpoint || (req.Host != "" && req.Host != req.URL.Host) ||
		(req.Method != http.MethodPost && req.Method != http.MethodDelete) {
		return nil, ErrDenied
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	r.GetBody = nil
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, ErrTransport
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrDenied
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent:
	default:
		return nil, ErrTransport
	}
	const limit = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, ErrTransport
	}
	if len(body) > limit {
		return nil, errResponse
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.Request = nil
	return resp, nil
}
