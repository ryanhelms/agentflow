package asanaoauth

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/provider/nativeoauth"
)

func (e *enrollment) metadata(ctx context.Context, address string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return errMetadata
	}
	req.Header.Set("Accept", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return ErrTransport
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ErrTransport
	}
	raw, err := readJSON(resp)
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, out) != nil {
		return errMetadata
	}
	return nil
}

func readJSON(resp *http.Response) ([]byte, error) {
	const limit = 64 << 10
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if media != "application/json" {
		return nil, errResponse
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, ErrTransport
	}
	if len(data) > limit {
		return nil, errResponse
	}
	return data, nil
}

type tokenResponse struct {
	Type      string  `json:"token_type"`
	Access    string  `json:"access_token"`
	Refresh   *string `json:"refresh_token"`
	ExpiresIn int64   `json:"expires_in"`
	Resource  *string `json:"resource"`
	Scope     *string `json:"scope"`
	Data      *struct {
		GID string      `json:"gid"`
		ID  json.Number `json:"id"`
	} `json:"data"`
	Error string `json:"error"`
}

func (e *enrollment) tokenRequest(ctx context.Context, app registration, form url.Values) (tokenResponse, error) {
	form.Set("client_id", app.ID)
	form.Set("client_secret", app.Secret)
	form.Set("resource", Resource)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, errResponse
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return tokenResponse{}, ErrTransport
	}
	defer resp.Body.Close()
	// A 401 is ambiguous at Asana, including a workspace-disabled app.
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return tokenResponse{}, ErrDenied
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		return tokenResponse{}, ErrTransport
	}
	raw, err := readJSON(resp)
	if err != nil {
		return tokenResponse{}, err
	}
	var t tokenResponse
	if json.Unmarshal(raw, &t) != nil {
		return tokenResponse{}, errResponse
	}
	if resp.StatusCode != http.StatusOK || t.Error != "" {
		switch t.Error {
		case "access_denied", "unauthorized_client", "invalid_client", "invalid_scope", "forbidden_scopes":
			return tokenResponse{}, ErrDenied
		case "invalid_grant":
			return tokenResponse{}, ErrLoginRequired
		case "invalid_target":
			return tokenResponse{}, ErrGrantChanged
		case "server_error", "temporarily_unavailable":
			return tokenResponse{}, ErrTransport
		default:
			return tokenResponse{}, errResponse
		}
	}
	return t, nil
}

func (e *enrollment) exchange(ctx context.Context, g *grant, form url.Values) error {
	started := time.Now()
	t, err := e.tokenRequest(ctx, g.Registration, form)
	if err != nil {
		return err
	}
	if !strings.EqualFold(t.Type, "bearer") || !nativeoauth.Opaque(t.Access) || t.ExpiresIn <= 0 || t.ExpiresIn > 1<<31 {
		return errResponse
	}
	if (t.Resource != nil && *t.Resource != Resource) || (t.Scope != nil && *t.Scope != "default") {
		return ErrGrantChanged
	}
	if t.Refresh != nil {
		if !nativeoauth.Opaque(*t.Refresh) {
			return errResponse
		}
		g.Refresh = *t.Refresh
	}
	if g.Refresh == "" {
		return errResponse
	}
	if t.Data != nil {
		if !gid.MatchString(t.Data.GID) || (t.Data.ID != "" && t.Data.ID.String() != t.Data.GID) || (g.User != "" && t.Data.GID != g.User) {
			return ErrGrantChanged
		}
		g.User = t.Data.GID
	}
	// A successful token POST alone does not prove a V2 MCP grant. Read the
	// authenticated principal and memberships at the exact MCP endpoint.
	user, err := e.verifyIdentity(ctx, t.Access, g.Workspace)
	if err != nil {
		return err
	}
	if g.User != "" && user != g.User {
		return ErrGrantChanged
	}
	g.User = user
	expiry := started.Add(time.Duration(t.ExpiresIn) * time.Second)
	if !time.Now().Before(expiry) {
		return errResponse
	}
	raw, err := json.Marshal(g) //nolint:gosec // Only the OS keychain receives this record.
	if err != nil {
		return errStorage
	}
	if err := e.put(string(raw)); err != nil {
		return err
	}
	e.cached.Store(&accessGrant{g.binding(), t.Access, expiry})
	return nil
}
