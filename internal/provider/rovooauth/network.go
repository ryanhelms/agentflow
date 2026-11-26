package rovooauth

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/provider/nativeoauth"
)

// NewTransport provides the shared native OAuth transport to the Rovo wire client.
func NewTransport() *http.Transport { return nativeoauth.NewTransport() }
func newClient() *http.Client       { return nativeoauth.NewClient() }
func opaque(s string) bool          { return nativeoauth.Opaque(s) }

// The value returned to the MCP transport must fit RFC 6750 header syntax.
var bearerSyntax = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)

type tokenResponse struct {
	Access    string  `json:"access_token"`
	Refresh   string  `json:"refresh_token"`
	Type      string  `json:"token_type"`
	Scope     *string `json:"scope"`
	ExpiresIn int64   `json:"expires_in"`
	Error     string  `json:"error"`
	expiry    time.Time
}

func (e *enrollment) exchange(ctx context.Context, clientID string, form url.Values) (*tokenResponse, error) {
	form.Set("resource", Resource)
	form.Set("client_id", clientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errToken
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	started := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, errToken
	}
	defer resp.Body.Close()
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || media != "application/json" {
		return nil, errToken
	}
	const maxBody = 64 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	var t tokenResponse
	if err != nil || len(body) > maxBody || json.Unmarshal(body, &t) != nil || t.Error != "" ||
		!strings.EqualFold(t.Type, "Bearer") || !opaque(t.Access) || !bearerSyntax.MatchString(t.Access) || (t.Refresh != "" && !opaque(t.Refresh)) ||
		(t.Scope != nil && !e.sameScopes(*t.Scope)) || t.ExpiresIn <= 0 || t.ExpiresIn > 1<<31 {
		return nil, errToken
	}
	t.expiry = started.Add(time.Duration(t.ExpiresIn) * time.Second)
	if !time.Now().Before(t.expiry) {
		return nil, errToken
	}
	return &t, nil
}
