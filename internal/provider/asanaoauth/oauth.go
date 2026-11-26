// Package asanaoauth enrolls a separately registered Agentflow Asana V2 MCP app.
package asanaoauth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/ryanhelms/agentflow/internal/provider/nativeoauth"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
	"golang.org/x/term"
)

const (
	Origin            = "https://mcp.asana.com"
	Resource          = Origin + "/v2"
	Endpoint          = Resource + "/mcp"
	resourceMeta      = Origin + "/.well-known/oauth-protected-resource/v2/mcp"
	issuer            = "https://app.asana.com"
	serverMeta        = issuer + "/.well-known/oauth-authorization-server"
	authorizeEndpoint = issuer + "/-/oauth_authorize"
	tokenEndpoint     = issuer + "/-/oauth_token"
	callbackHost      = "127.0.0.1:53669"
	callbackPath      = "/oauth/asana/callback"
	redirectURI       = "http://" + callbackHost + callbackPath
)

var (
	ErrNotEnrolled   = errors.New("no Agentflow Asana MCP enrollment")
	ErrLoginRequired = errors.New("explicit Agentflow Asana MCP login required")
	ErrGrantChanged  = errors.New("changed Asana MCP enrollment binding")
	ErrDenied        = errors.New("authorization denied or app restricted for Asana MCP")
	ErrTransport     = errors.New("could not complete Asana MCP enrollment request")
	errMetadata      = errors.New("unsupported Asana V2 OAuth metadata")
	errStorage       = errors.New("could not verify the Agentflow Asana OS keychain entry")
	errResponse      = errors.New("invalid Asana MCP enrollment response")
	gid              = regexp.MustCompile(`^[1-9][0-9]{0,31}$`)
)

// Binding identifies one explicit consent and the intended workspace. The writer
// must also verify its project's workspace with this generation before dispatch.
type Binding struct {
	Generation string `json:"generation"`
	User       string `json:"user"`
	Workspace  string `json:"workspace"`
}

type registration struct {
	ID     string `json:"client_id"`
	Secret string `json:"client_secret"`
}

type enrollment struct {
	client  *http.Client
	open    func(context.Context, string) error
	listen  func(context.Context) (net.Listener, error)
	get     func(string, string) (string, error)
	set     func(string, string, string) error
	remove  func(string, string) error
	lockDir func() (string, error)
	// All reads and writes are under the per-user OS lock.
	cached atomic.Pointer[accessGrant]
}

var local = enrollment{
	client: nativeoauth.NewClient(), open: nativeoauth.OpenBrowser,
	listen: func(ctx context.Context) (net.Listener, error) {
		var lc net.ListenConfig
		return lc.Listen(ctx, "tcp4", callbackHost)
	},
	get: keyring.Get, set: keyring.Set, remove: keyring.Delete,
	lockDir: func() (string, error) { return nativeoauth.UserLockDir(".agentflow-asana") },
}

// Login accepts only a local terminal and an owner-provisioned, per-install
// registration. The browser sees only public OAuth request parameters.
func Login(ctx context.Context, input io.Reader, diagnostics io.Writer) error {
	f, ok := input.(*os.File)
	if !ok || f == nil || !term.IsTerminal(int(f.Fd())) || diagnostics == nil {
		return errors.New("a local interactive terminal is required for Asana MCP enrollment")
	}
	if _, err := fmt.Fprintf(diagnostics, "Use the owner-approved Agentflow MCP app registered for this installation and workspace. Register %s and select the same workspace in Asana.\n", redirectURI); err != nil {
		return err
	}
	read := func(prompt string) (string, error) {
		if _, err := fmt.Fprint(diagnostics, prompt); err != nil {
			return "", err
		}
		b, err := term.ReadPassword(int(f.Fd()))
		defer clear(b)
		_, _ = fmt.Fprintln(diagnostics)
		if err != nil || len(b) == 0 {
			return "", errors.New("cancelled or empty Asana enrollment input")
		}
		return string(b), nil
	}
	id, err := read("Agentflow MCP app client ID: ")
	if err != nil {
		return err
	}
	secret, err := read("Agentflow MCP app client secret (hidden): ")
	if err != nil {
		return err
	}
	ws, err := read("Intended workspace GID: ")
	if err != nil {
		return err
	}
	return local.login(ctx, registration{id, secret}, ws)
}

func (e *enrollment) discover(ctx context.Context) (bool, error) {
	// The resource server describes the MCP endpoint. Asana documents the
	// parent /v2 identifier for the authorization and token request forms.
	var r oauthex.ProtectedResourceMetadata
	if err := e.metadata(ctx, resourceMeta, &r); err != nil {
		return false, err
	}
	if r.Resource != Endpoint || !slices.Equal(r.AuthorizationServers, []string{issuer}) || !slices.Contains(r.BearerMethodsSupported, "header") || r.DPOPBoundAccessTokensRequired {
		return false, errMetadata
	}
	var a oauthex.AuthServerMeta
	if err := e.metadata(ctx, serverMeta, &a); err != nil {
		return false, err
	}
	if a.Issuer != issuer || a.AuthorizationEndpoint != authorizeEndpoint || a.TokenEndpoint != tokenEndpoint ||
		!slices.Contains(a.CodeChallengeMethodsSupported, "S256") || !slices.Contains(a.TokenEndpointAuthMethodsSupported, "client_secret_post") ||
		!slices.Contains(a.ResponseTypesSupported, "code") || !slices.Contains(a.GrantTypesSupported, "authorization_code") ||
		!slices.Contains(a.GrantTypesSupported, "refresh_token") {
		return false, errMetadata
	}
	return a.AuthorizationResponseIssParameterSupported, nil
}

func (e *enrollment) login(ctx context.Context, app registration, workspace string) error {
	if !gid.MatchString(app.ID) || !nativeoauth.Opaque(app.Secret) || !gid.MatchString(workspace) {
		return errors.New("invalid Asana MCP registration or workspace GID")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	requireIssuer, err := e.discover(ctx)
	if err != nil {
		return err
	}
	lock, err := nativeoauth.Lock(ctx, e.lockDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	ln, err := e.listen(ctx)
	if err != nil {
		return errors.New("could not bind the registered Asana loopback callback")
	}
	defer ln.Close()
	state, verifier := rand.Text(), oauth2.GenerateVerifier()
	config := oauth2.Config{ClientID: app.ID, RedirectURL: redirectURI, Endpoint: oauth2.Endpoint{AuthURL: authorizeEndpoint}}
	address := config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("resource", Resource))
	code, err := nativeoauth.WaitCallback(ctx, ln, callbackHost, callbackPath, issuer, state, requireIssuer, func() error { return e.open(ctx, address) })
	if err != nil {
		// WaitCallback deliberately combines a rejected consent and a malformed
		// state-matched response. Both must stop enrollment.
		if err.Error() == "consent denied or invalid OAuth callback" {
			return ErrDenied
		}
		return err
	}
	g := grant{Registration: app, Resource: Resource, Binding: Binding{Generation: rand.Text(), Workspace: workspace}}
	return e.exchange(ctx, &g, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirectURI}})
}
