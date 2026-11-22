// Package rovooauth enrolls Devflow as a native public client of Atlassian Rovo MCP.
package rovooauth

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/nscaledev/devflow/internal/provider/nativeoauth"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

const (
	// Resource identifies the audience on both OAuth endpoints. The transport's
	// ?tools=all catalog selector is never part of this identifier.
	Resource          = "https://mcp.atlassian.com/v2/mcp"
	resourceMeta      = "https://mcp.atlassian.com/.well-known/oauth-protected-resource/v2/mcp"
	issuer            = "https://auth.atlassian.com/VCeDsk8ZHncYF1g234fKtc4lNipbBhu3"
	registerEndpoint  = issuer + "/dcr/register"
	authorizeEndpoint = "https://auth.atlassian.com/authorize"
	tokenEndpoint     = "https://auth.atlassian.com/oauth/token" //nolint:gosec // Public issuer metadata endpoint.
	baseScopes        = "read:me read:account offline_access"
	jiraScope         = baseScopes + " read:jira:agent-interface write:jira:agent-interface"
	confluenceScope   = baseScopes + " read:confluence:agent-interface write:confluence:agent-interface"
)

var (
	ErrNotEnrolled   = errors.New("no Devflow Rovo enrollment")
	ErrLoginRequired = errors.New("explicit Devflow Rovo login required")
	errMetadata      = errors.New("official Rovo OAuth metadata does not match the supported profile")
	errRegistration  = errors.New("could not register Devflow as a public Rovo client")
	errToken         = errors.New("could not obtain a usable Rovo grant")
	errStorage       = errors.New("OS keyring unavailable or failed verification for the Devflow Rovo grant")
	errGrantSize     = errors.New("secure storage size limit exceeded for the Devflow Rovo grant")
	errLock          = errors.New("could not lock Devflow Rovo grant storage")
)

// Each provider has an independent consent, credential namespace and bearer cache.
type enrollment struct {
	service string
	scope   string
	listen  func(context.Context) (net.Listener, error)
	client  *http.Client
	open    func(context.Context, string) error
	get     func(string, string) (string, error)
	set     func(string, string, string) error
	remove  func(string, string) error
	lockDir func() (string, error)
	cached  atomic.Pointer[accessGrant]
}

func newEnrollment(service, scope string) *enrollment {
	return &enrollment{service: service, scope: scope, client: newClient(), listen: listenCallback, open: nativeoauth.OpenBrowser,
		get: keyring.Get, set: keyring.Set, remove: keyring.Delete, lockDir: userLockDir}
}

var enrollments = map[string]*enrollment{
	"jira":       newEnrollment("devflow.rovooauth.v2.jira.1", jiraScope),
	"confluence": newEnrollment("devflow.rovooauth.v2.confluence.1", confluenceScope),
}

var errProvider = errors.New("select jira or confluence for Devflow Rovo enrollment")

// Login starts browser consent only for the selected provider.
func Login(ctx context.Context, provider string) error {
	if e := enrollments[provider]; e != nil {
		return e.login(ctx)
	}
	return errProvider
}

// Disconnect retires only this provider's local grant.
func Disconnect(ctx context.Context, provider string) error {
	if e := enrollments[provider]; e != nil {
		return e.disconnect(ctx)
	}
	return errProvider
}

// BoundToken returns the selected provider's bearer and enrollment generation together.
func BoundToken(ctx context.Context, provider string) (accessToken, generation string, err error) {
	if e := enrollments[provider]; e != nil {
		return e.boundToken(ctx)
	}
	return "", "", errProvider
}

func (e *enrollment) discover(ctx context.Context) (*oauthex.AuthServerMeta, error) {
	r, err := oauthex.GetProtectedResourceMetadata(ctx, resourceMeta, Resource, e.client)
	if err != nil || !slices.Equal(r.AuthorizationServers, []string{issuer}) ||
		!slices.Contains(r.BearerMethodsSupported, "header") || r.DPOPBoundAccessTokensRequired ||
		!e.containsScopes(r.ScopesSupported) {
		return nil, errMetadata
	}
	a, err := auth.GetAuthServerMetadata(ctx, issuer, e.client)
	if err != nil || a == nil || a.Issuer != issuer || a.RegistrationEndpoint != registerEndpoint ||
		a.TokenEndpoint != tokenEndpoint || a.AuthorizationEndpoint != authorizeEndpoint ||
		!slices.Contains(a.CodeChallengeMethodsSupported, "S256") ||
		!slices.Contains(a.TokenEndpointAuthMethodsSupported, "none") ||
		!slices.Contains(a.ResponseTypesSupported, "code") ||
		!slices.Contains(a.GrantTypesSupported, "authorization_code") ||
		!slices.Contains(a.GrantTypesSupported, "refresh_token") {
		return nil, errMetadata
	}
	return a, nil
}

func (e *enrollment) containsScopes(scopes []string) bool {
	for _, required := range strings.Fields(e.scope) {
		if !slices.Contains(scopes, required) {
			return false
		}
	}
	return true
}

func (e *enrollment) sameScopes(s string) bool {
	scopes := strings.Fields(s)
	return len(scopes) == len(strings.Fields(e.scope)) && e.containsScopes(scopes)
}

func (e *enrollment) login(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	as, err := e.discover(ctx)
	if err != nil {
		return err
	}
	listener, err := e.listen(ctx)
	if err != nil {
		return err
	}
	defer listener.Close()
	redirect := "http://" + listener.Addr().String() + callbackPath
	meta := &oauthex.ClientRegistrationMetadata{
		ClientName: "Devflow", ApplicationType: "native", TokenEndpointAuthMethod: "none",
		RedirectURIs: []string{redirect}, GrantTypes: []string{"authorization_code", "refresh_token"},
		ResponseTypes: []string{"code"}, Scope: e.scope,
	}
	reg, err := oauthex.RegisterClient(ctx, registerEndpoint, meta, e.client)
	if err != nil || !opaque(reg.ClientID) || reg.TokenEndpointAuthMethod != "none" ||
		!slices.Equal(reg.RedirectURIs, meta.RedirectURIs) ||
		(reg.ApplicationType != "" && reg.ApplicationType != "native") ||
		(reg.GrantTypes != nil && (!slices.Contains(reg.GrantTypes, "authorization_code") || !slices.Contains(reg.GrantTypes, "refresh_token"))) ||
		(reg.ResponseTypes != nil && !slices.Contains(reg.ResponseTypes, "code")) ||
		(reg.Scope != "" && !e.sameScopes(reg.Scope)) {
		return errRegistration
	}
	// Some public registrations also return a client_secret. Only the issued
	// client ID participates in the negotiated "none" authentication method.
	cfg := oauth2.Config{ClientID: reg.ClientID, RedirectURL: redirect, Scopes: strings.Fields(e.scope),
		Endpoint: oauth2.Endpoint{AuthURL: authorizeEndpoint}}
	state, verifier := rand.Text(), oauth2.GenerateVerifier()
	browserURL := cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("resource", Resource))
	code, err := waitCallback(ctx, listener, state, as.AuthorizationResponseIssParameterSupported, func() error {
		return e.open(ctx, browserURL)
	})
	if err != nil {
		return err
	}
	t, err := e.exchange(ctx, reg.ClientID, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect},
	})
	if err != nil {
		return err
	}
	lock, err := e.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	head, err := e.currentHead(true)
	if err != nil && !errors.Is(err, ErrNotEnrolled) {
		return err
	}
	return e.save(head, grant{Generation: rand.Text(), ClientID: reg.ClientID, Refresh: t.Refresh}, t)
}
