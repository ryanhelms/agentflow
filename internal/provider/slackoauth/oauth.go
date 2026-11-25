// Package slackoauth enrolls a separately registered Slack MCP app.
// Only explicit interactive login can publish a new enrollment generation.
package slackoauth

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/nscaledev/agentflow/internal/provider/nativeoauth"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
	"golang.org/x/term"
)

const (
	Resource           = "https://mcp.slack.com"
	Endpoint           = Resource + "/mcp"
	resourceMeta       = Resource + "/.well-known/oauth-protected-resource"
	serverMeta         = Resource + "/.well-known/oauth-authorization-server"
	authorizeEndpoint  = "https://slack.com/oauth/v2_user/authorize"
	tokenEndpoint      = "https://slack.com/api/oauth.v2.user.access" //nolint:gosec // Public token endpoint from Slack metadata.
	callbackHost       = "127.0.0.1:53668"
	publicCallbackHost = "localhost:53668"
	callbackPath       = "/oauth/slack/callback"
	redirectURI        = "https://" + callbackHost + callbackPath
	publicRedirectURI  = "http://" + publicCallbackHost + callbackPath
	// User-centric OAuth uses comma-delimited scope. Only chat:write changes Slack data.
	scopes = "channels:read,chat:write,groups:read,im:read,mpim:read,users:read,users:read.email"
)

var (
	ErrNotEnrolled   = errors.New("no Agentflow Slack enrollment")
	ErrLoginRequired = errors.New("explicit Agentflow Slack login required")
	ErrGrantChanged  = errors.New("the Agentflow Slack enrollment changed")
	errMetadata      = errors.New("official Slack OAuth metadata does not match the supported profile")
	errToken         = errors.New("could not verify the Slack user grant and requested scope")
	errWrongTeam     = errors.New("received a Slack grant for a different workspace than the expected team ID")
	errStorage       = errors.New("could not verify the Agentflow Slack grant in the OS keychain")
	clientIDSyntax   = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
)

type credentials struct {
	ClientID string `json:"client_id"`
	Secret   string `json:"client_secret,omitempty"`
	Public   bool   `json:"public,omitempty"`
}

type enrollment struct {
	client  *http.Client
	open    func(context.Context, string) error
	listen  func(context.Context, *tls.Certificate) (net.Listener, error)
	get     func(string, string) (string, error)
	set     func(string, string, string) error
	remove  func(string, string) error
	lockDir func() (string, error)
}

var local = enrollment{
	client: nativeoauth.NewClient(), open: nativeoauth.OpenBrowser, listen: listenCallback,
	get: keyring.Get, set: keyring.Set, remove: keyring.Delete,
	lockDir: func() (string, error) { return nativeoauth.UserLockDir(".agentflow-slack") },
}

func listenCallback(ctx context.Context, cert *tls.Certificate) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp4", callbackHost)
	if err != nil {
		return nil, errors.New("could not bind the registered Slack loopback callback")
	}
	if cert == nil {
		return ln, nil
	}
	return tls.NewListener(ln, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*cert}}), nil
}

// Login reads registration credentials only from an interactive terminal. It never
// accepts an access token, refresh token, environment credential or client-owned login.
func Login(ctx context.Context, input io.Reader, diagnostics io.Writer) error {
	f, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) || diagnostics == nil {
		return errors.New("an interactive terminal is required for Agentflow Slack enrollment")
	}
	read := func(label string) (string, error) {
		if _, err := fmt.Fprint(diagnostics, label); err != nil {
			return "", err
		}
		b, err := term.ReadPassword(int(f.Fd()))
		_, _ = fmt.Fprintln(diagnostics)
		defer clear(b)
		if err != nil || len(b) == 0 {
			return "", errors.New("cancelled or empty Slack enrollment input")
		}
		return string(b), nil
	}
	_, err := fmt.Fprintf(diagnostics, "Use your approved internal or Marketplace Slack app. Register %s.\n"+
		"Agentflow requests %s for identity, conversation membership and sending messages.\n", redirectURI, scopes)
	if err != nil {
		return err
	}
	certFile, err := read("Callback TLS certificate PEM path (browser-trusted for 127.0.0.1): ")
	if err != nil {
		return err
	}
	keyFile, err := read("Matching TLS private key PEM path: ")
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil || cert.Leaf == nil || cert.Leaf.VerifyHostname("127.0.0.1") != nil {
		return errors.New("could not load a matching TLS certificate and key for the registered loopback address")
	}
	id, err := read("Approved Slack app client ID (hidden): ")
	if err != nil {
		return err
	}
	secret, err := read("Slack app client secret (hidden): ")
	if err != nil {
		return err
	}
	return local.login(ctx, credentials{ClientID: id, Secret: secret}, &cert, "")
}

// LoginPublic starts local browser consent using only public registration identifiers.
func LoginPublic(ctx context.Context, clientID, expectedTeam string) error {
	return local.login(ctx, credentials{ClientID: clientID, Public: true}, nil, expectedTeam)
}

func supportsScopes(list []string) bool {
	for required := range strings.SplitSeq(scopes, ",") {
		if !slices.Contains(list, required) {
			return false
		}
	}
	return true
}

func (e *enrollment) discover(ctx context.Context, public bool) (bool, error) {
	r, err := oauthex.GetProtectedResourceMetadata(ctx, resourceMeta, Resource, e.client)
	if err != nil || !slices.Equal(r.AuthorizationServers, []string{Resource}) ||
		!slices.Contains(r.BearerMethodsSupported, "header") || r.DPOPBoundAccessTokensRequired || !supportsScopes(r.ScopesSupported) {
		return false, errMetadata
	}
	a, err := oauthex.GetAuthServerMeta(ctx, serverMeta, Resource, e.client)
	if err != nil || a == nil || a.Issuer != Resource || a.AuthorizationEndpoint != authorizeEndpoint || a.TokenEndpoint != tokenEndpoint ||
		!slices.Contains(a.ResponseTypesSupported, "code") ||
		!slices.Contains(a.GrantTypesSupported, "authorization_code") || !slices.Contains(a.GrantTypesSupported, "refresh_token") ||
		!supportsScopes(a.ScopesSupported) {
		return false, errMetadata
	}
	// Slack advertises client_secret_post for confidential apps. Its desktop
	// PKCE profile uses the same pinned endpoints without client authentication.
	if public {
		if !slices.Contains(a.CodeChallengeMethodsSupported, "S256") {
			return false, errMetadata
		}
	} else if !slices.Contains(a.TokenEndpointAuthMethodsSupported, "client_secret_post") {
		return false, errMetadata
	}
	return a.AuthorizationResponseIssParameterSupported, nil
}

func (e *enrollment) login(ctx context.Context, app credentials, cert *tls.Certificate, expectedTeam string) error {
	if !app.valid() || app.Public != (cert == nil) {
		return errors.New("invalid Slack app credentials")
	}
	if app.Public && !teamID.MatchString(expectedTeam) {
		return errors.New("public Slack login requires the expected workspace team ID")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	requireIssuer, err := e.discover(ctx, app.Public)
	if err != nil {
		return err
	}
	lock, err := nativeoauth.Lock(ctx, e.lockDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	ln, err := e.listen(ctx, cert)
	if err != nil {
		return err
	}
	defer ln.Close()
	host, redirect := callbackHost, redirectURI
	if app.Public {
		host, redirect = publicCallbackHost, publicRedirectURI
	}
	state := rand.Text()
	query := url.Values{
		"client_id": {app.ClientID}, "response_type": {"code"}, "scope": {scopes},
		"resource": {Resource}, "state": {state}, "redirect_uri": {redirect},
	}
	form := url.Values{"grant_type": {"authorization_code"}, "redirect_uri": {redirect}}
	if app.Public {
		query.Set("team", expectedTeam)
		verifier := oauth2.GenerateVerifier()
		query.Set("code_challenge_method", "S256")
		query.Set("code_challenge", oauth2.S256ChallengeFromVerifier(verifier))
		form.Set("code_verifier", verifier)
	}
	code, err := nativeoauth.WaitCallback(ctx, ln, host, callbackPath, Resource, state, requireIssuer, func() error {
		return e.open(ctx, authorizeEndpoint+"?"+query.Encode())
	})
	if err != nil {
		return err
	}
	g := grant{App: app, Generation: rand.Text(), Scope: scopes, Team: expectedTeam}
	form.Set("code", code)
	if err := e.exchange(ctx, &g, form); err != nil {
		return err
	}
	return e.save(g)
}

func (c credentials) valid() bool {
	return len(c.ClientID) <= 128 && clientIDSyntax.MatchString(c.ClientID) &&
		((c.Public && c.Secret == "") || (!c.Public && nativeoauth.Opaque(c.Secret)))
}
