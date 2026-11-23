package slackoauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nscaledev/devflow/internal/provider/nativeoauth"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (tr roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return tr(r) }

// Drive the browser callback over HTTP or HTTPS on in-memory connections. No system
// browser, credential store, trusted root, TCP port, or Slack account is used.
type loopback struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *loopback) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53668} }
func (l *loopback) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *loopback) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

type fixture struct {
	e              enrollment
	res, as, reply map[string]any
	app            credentials
	cert           tls.Certificate
	records        map[string]string
	posts          int
	openURL        string
	callbackErr    string
	expectedTeam   string
}

func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "loopback fixture"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ss := strings.Split(scopes, ",")
	f := &fixture{
		app: credentials{ClientID: "1234.5678", Secret: "mock-app-secret"}, records: map[string]string{},
		res: map[string]any{
			"resource": Resource, "authorization_servers": []string{Resource},
			"bearer_methods_supported": []string{"header", "form"}, "scopes_supported": ss,
		},
		as: map[string]any{
			"issuer": Resource, "authorization_endpoint": authorizeEndpoint, "token_endpoint": tokenEndpoint,
			"code_challenge_methods_supported": []string{"S256"}, "response_types_supported": []string{"code"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_post"}, "scopes_supported": ss,
		},
		reply: map[string]any{
			"ok": true, "token_type": "user", "access_token": "mock-access",
			"authed_user": map[string]string{"id": "U12345", "scope": scopes},
			"team":        map[string]string{"id": "T12345"}, "app_id": "A12345",
		},
	}
	dir := filepath.Join(t.TempDir(), "lock")
	f.e = enrollment{client: nativeoauth.NewClient(), lockDir: func() (string, error) { return dir, nil }}
	f.e.get = func(s, a string) (string, error) {
		if s != service || a != account {
			t.Fatal("unexpected keychain location")
		}
		raw, ok := f.records[a]
		if !ok {
			return "", keyring.ErrNotFound
		}
		return raw, nil
	}
	f.e.set = func(s, a, v string) error {
		if s != service || a != account {
			t.Fatal("unexpected keychain write")
		}
		f.records[a] = v
		return nil
	}
	f.e.remove = func(s, a string) error {
		if s != service || a != account {
			t.Fatal("unexpected keychain delete")
		}
		delete(f.records, a)
		return nil
	}
	var roots *x509.CertPool
	f.cert, roots = testCertificate(t)
	var pipe *loopback
	f.e.listen = func(_ context.Context, c *tls.Certificate) (net.Listener, error) {
		pipe = &loopback{conns: make(chan net.Conn), closed: make(chan struct{})}
		if c == nil {
			return pipe, nil
		}
		return tls.NewListener(pipe, &tls.Config{Certificates: []tls.Certificate{*c}, MinVersion: tls.VersionTLS12}), nil
	}
	f.e.open = func(ctx context.Context, raw string) error {
		previous, _ := url.Parse(f.openURL)
		f.openURL = raw
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		state := u.Query().Get("state")
		redirect := redirectURI
		if f.app.Public {
			redirect = "http://localhost:53668/oauth/slack/callback"
		}
		want := url.Values{"client_id": {f.app.ClientID}, "scope": {scopes}, "redirect_uri": {redirect}, "state": {state}, "response_type": {"code"}, "resource": {Resource}}
		if f.app.Public {
			want.Set("team", f.expectedTeam)
			challenge := u.Query().Get("code_challenge")
			if len(challenge) != 43 || challenge == previous.Query().Get("code_challenge") {
				return errors.New("missing or reused S256 challenge")
			}
			want.Set("code_challenge_method", "S256")
			want.Set("code_challenge", challenge)
		}
		if u.Scheme+"://"+u.Host+u.Path != authorizeEndpoint || len(state) < 24 || !reflect.DeepEqual(u.Query(), want) {
			return errors.New("unexpected authorization request")
		}
		tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			a, b := net.Pipe()
			select {
			case pipe.conns <- b:
				return a, nil
			case <-ctx.Done():
			case <-pipe.closed:
			}
			a.Close()
			b.Close()
			return nil, net.ErrClosed
		}}
		defer tr.CloseIdleConnections()
		client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
		send := func(target, host string, q url.Values) (int, error) {
			r, err := http.NewRequestWithContext(ctx, "GET", target+"?"+q.Encode(), nil)
			if err != nil {
				return 0, err
			}
			r.Host = host
			res, err := client.Do(r)
			if err != nil {
				return 0, err
			}
			defer res.Body.Close()
			_, err = io.Copy(io.Discard, res.Body)
			return res.StatusCode, err
		}
		q := url.Values{"code": {"mock-code"}, "state": {state}, "iss": {Resource}}
		for _, bad := range []struct {
			target, host string
			state        []string
		}{
			{redirect, "foreign.invalid", []string{state}},
			{redirect, "localhost", []string{state}},
			{strings.Replace(redirect, "/oauth", "/%6fauth", 1), "", []string{state}},
			{redirect + "/", "", []string{state}},
			{redirect, "", []string{"wrong"}},
			{redirect, "", []string{state, state}},
		} {
			q["state"] = bad.state
			if status, err := send(bad.target, bad.host, q); err != nil || status != 400 {
				return fmt.Errorf("unexpected invalid callback status %d", status)
			}
		}
		if old := previous.Query().Get("state"); old != "" {
			q.Set("state", old)
			if status, err := send(redirect, "", q); err != nil || status != 400 {
				return errors.New("accepted a replayed authorization callback")
			}
		}
		q.Set("state", state)
		if f.callbackErr == "issuer" {
			q.Set("iss", Resource+"/")
		} else if f.callbackErr != "" {
			q.Set("error", f.callbackErr)
		}
		_, err = send(redirect, "", q)
		return err
	}
	f.e.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		var data map[string]any
		switch r.URL.String() {
		case resourceMeta:
			data = f.res
		case serverMeta:
			data = f.as
		case tokenEndpoint:
			f.posts++
			if r.Method != "POST" || r.ParseForm() != nil || r.Header.Get("Authorization") != "" {
				t.Fatal("invalid exchange request")
			}
			want := url.Values{"client_id": {f.app.ClientID}, "resource": {Resource}, "grant_type": {r.Form.Get("grant_type")}}
			if !f.app.Public {
				want.Set("client_secret", f.app.Secret)
			}
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				want.Set("code", "mock-code")
				if f.app.Public {
					want.Set("redirect_uri", "http://localhost:53668/oauth/slack/callback")
					verifier := r.Form.Get("code_verifier")
					auth, _ := url.Parse(f.openURL)
					if len(verifier) < 43 || len(verifier) > 128 || oauth2.S256ChallengeFromVerifier(verifier) != auth.Query().Get("code_challenge") {
						t.Fatal("invalid PKCE verifier")
					}
					want.Set("code_verifier", verifier)
				} else {
					want.Set("redirect_uri", redirectURI)
				}
			case "refresh_token":
				want.Set("refresh_token", "mock-refresh")
				if f.records[account] != "{}" {
					t.Fatal("refresh token not retired before exchange")
				}
			default:
				t.Fatal("unexpected grant type")
			}
			if !reflect.DeepEqual(want, r.PostForm) {
				t.Fatal("incorrect exchange parameters")
			}
			data = f.reply
		default:
			t.Fatal("request outside pinned endpoints")
		}
		b, err := json.Marshal(data)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}, err
	})
	return f
}

func newPublicFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.app = credentials{ClientID: "1234.5678", Public: true}
	f.expectedTeam = "T12345"
	return f
}

func (f *fixture) login(ctx context.Context) error {
	if f.app.Public {
		return f.e.login(ctx, f.app, nil, f.expectedTeam)
	}
	return f.e.login(ctx, f.app, &f.cert, "")
}

func TestBrowserEnrollmentAndBoundGrant(t *testing.T) {
	t.Run("confidential", func(t *testing.T) { testBoundGrant(t, newFixture(t)) })
	t.Run("public", func(t *testing.T) { testBoundGrant(t, newPublicFixture(t)) })
}

func testBoundGrant(t *testing.T, f *fixture) {
	ctx := t.Context()
	if _, _, err := f.e.token(ctx, "", nil); !errors.Is(err, ErrNotEnrolled) {
		t.Fatal(err)
	}
	if err := f.login(ctx); err != nil {
		t.Fatal(err)
	}
	token, id, err := f.e.token(ctx, "", nil)
	if token != "mock-access" || id == "" || err != nil {
		t.Fatal("missing bound grant", err)
	}
	first, _ := url.Parse(f.openURL)
	if err := f.login(ctx); err != nil {
		t.Fatal(err)
	}
	second, _ := url.Parse(f.openURL)
	if first.Query().Get("state") == second.Query().Get("state") {
		t.Fatal("reused state")
	}
	_, next, err := f.e.token(ctx, "", nil)
	if err != nil || next == id {
		t.Fatal("new consent reused old generation", err)
	}
	if _, _, err := f.e.token(ctx, id, func(string) error { t.Fatal("dispatched stale grant"); return nil }); !errors.Is(err, ErrGrantChanged) {
		t.Fatal(err)
	}
	expectedErr := errors.New("dispatch lost")
	calls := 0
	_, _, err = f.e.token(ctx, next, func(token string) error {
		calls++
		if token != "mock-access" {
			t.Fatal("wrong token")
		}
		blocked, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()
		if err := f.e.disconnect(blocked); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("logout passed active dispatch", err)
		}
		return expectedErr
	})
	if calls != 1 || !errors.Is(err, expectedErr) {
		t.Fatal("dispatch retried or its error lost")
	}
	if err := f.e.disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.e.token(ctx, "", nil); !errors.Is(err, ErrNotEnrolled) {
		t.Fatal(err)
	}
	if _, _, err := f.e.token(ctx, next, func(string) error { t.Fatal("dispatched after logout"); return nil }); !errors.Is(err, ErrGrantChanged) {
		t.Fatal("logout lost the binding conflict", err)
	}
	dir, _ := f.e.lockDir()
	if raw, err := os.ReadFile(filepath.Join(dir, "grant.lock")); err != nil || len(raw) != 0 {
		t.Fatal("lock file contains data", err)
	}
}

func TestDiscoveryAndAuthorizationDenials(t *testing.T) {
	for _, tc := range []struct {
		doc, field string
		value      any
	}{
		{"resource", "resource", Endpoint}, {"resource", "authorization_servers", []string{Resource, "https://slack.com"}},
		{"resource", "bearer_methods_supported", []string{"form"}}, {"resource", "scopes_supported", []string{"chat:write"}},
		{"server", "issuer", Resource + "/"},
		{"server", "authorization_endpoint", authorizeEndpoint + "/"},
		{"server", "token_endpoint", "https://slack.com/api/oauth.v2.access"},
		{"server", "token_endpoint_auth_methods_supported", []string{"none"}},
		{"server", "grant_types_supported", []string{"authorization_code"}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			f := newFixture(t)
			doc := f.res
			if tc.doc == "server" {
				doc = f.as
			}
			doc[tc.field] = tc.value
			if err := f.login(t.Context()); !errors.Is(err, errMetadata) || f.openURL != "" || f.posts != 0 || len(f.records) != 0 {
				t.Fatal("metadata accepted", err)
			}
		})
	}
	for _, reason := range []string{"mock-private-error", "issuer"} {
		t.Run(reason, func(t *testing.T) {
			f := newFixture(t)
			f.callbackErr = reason
			if err := f.login(t.Context()); err == nil || strings.Contains(err.Error(), reason) || f.posts != 0 || len(f.records) != 0 {
				t.Fatal("callback denial used or leaked", err)
			}
		})
	}
}

func TestGrantResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		field string
		value any
	}{
		{"ok", false}, {"token_type", "bot"}, {"resource", Endpoint}, {"scope", scopes + ",files:write"}, {"access_token", "bad\r\nheader"},
		{"authed_user", map[string]string{"id": "U12345"}}, {"authed_user", map[string]string{"id": "U12345", "scope": "chat:write"}},
		{"authed_user", map[string]string{"id": "U12345", "scope": scopes + ",chat:write"}}, {"team", nil}, {"expires_in", -1}, {"refresh_token", "mock-refresh"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			f := newFixture(t)
			f.reply[tc.field] = tc.value
			if err := f.login(t.Context()); !errors.Is(err, errToken) || len(f.records) != 0 {
				t.Fatal("invalid grant accepted", err)
			}
		})
	}
}

func TestRotatingGrantIdentity(t *testing.T) {
	t.Run("confidential", func(t *testing.T) { testRotatingGrantIdentity(t, newFixture) })
	t.Run("public", func(t *testing.T) { testRotatingGrantIdentity(t, newPublicFixture) })
}

func testRotatingGrantIdentity(t *testing.T, fixtureFor func(*testing.T) *fixture) {
	for _, scenario := range []string{"rotate", "changed workspace", "changed user", "changed top-level user", "changed app", "changed scope", "replayed refresh", "ambiguous response"} {
		t.Run(scenario, func(t *testing.T) {
			f := fixtureFor(t)
			f.reply["expires_in"] = 3600
			f.reply["refresh_token"] = "mock-refresh"
			if err := f.login(t.Context()); err != nil {
				t.Fatal(err)
			}
			var g grant
			if err := json.Unmarshal([]byte(f.records[account]), &g); err != nil {
				t.Fatal(err)
			}
			g.Expires = time.Now().Add(-time.Second).Unix()
			if err := f.e.save(g); err != nil {
				t.Fatal(err)
			}
			f.reply = map[string]any{"ok": true, "token_type": "user", "access_token": "mock-next-access", "expires_in": 43200, "refresh_token": "mock-next-refresh"}
			switch scenario {
			case "changed workspace":
				f.reply["team"] = map[string]string{"id": "T22222"}
			case "changed user":
				f.reply["authed_user"] = map[string]string{"id": "U22222"}
			case "changed top-level user":
				f.reply["id"] = "U22222"
			case "changed app":
				f.reply["app_id"] = "A22222"
			case "changed scope":
				f.reply["scope"] = scopes + ",files:write"
			case "replayed refresh":
				f.reply["refresh_token"] = g.Refresh
			case "ambiguous response":
				f.reply["error"] = "mock-secret"
			}
			token, id, err := f.e.token(t.Context(), g.Generation, nil)
			if scenario == "rotate" {
				if err != nil || id != g.Generation || token != "mock-next-access" || !strings.Contains(f.records[account], "mock-next-refresh") {
					t.Fatal("rotation lost identity or replacement token", err)
				}
				_, _, err = f.e.token(t.Context(), id, nil)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, ErrLoginRequired) || strings.Contains(err.Error(), "mock-") || token != "" || id != "" {
					t.Fatal("failed refresh authorized dispatch", err)
				}
				if _, _, err = f.e.token(t.Context(), "", nil); !errors.Is(err, ErrLoginRequired) {
					t.Fatal("spent grant reused")
				}
			}
			if f.posts != 2 {
				t.Fatal("unexpected token POST count", f.posts)
			}
		})
	}
}

func TestSecretInputAndStoreFailures(t *testing.T) {
	if err := Login(t.Context(), strings.NewReader("mock-credential"), io.Discard); err == nil {
		t.Fatal("accepted piped credentials")
	}
	f := newFixture(t)
	f.e.set = func(string, string, string) error { return errors.New("mock-secret") }
	if err := f.login(t.Context()); !errors.Is(err, errStorage) || strings.Contains(err.Error(), "mock-") {
		t.Fatal("keychain error leaked", err)
	}
	if err := WithBoundToken(t.Context(), "", func(string) error { return nil }); !errors.Is(err, ErrGrantChanged) {
		t.Fatal("empty binding accepted")
	}
}

func TestOAuthHTTPRedirectsAreTerminal(t *testing.T) {
	for _, endpoint := range []string{resourceMeta, serverMeta, tokenEndpoint} {
		t.Run(endpoint, func(t *testing.T) {
			f := newFixture(t)
			base := f.e.client.Transport
			f.e.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
				switch r.URL.String() {
				case "https://foreign.invalid/sink":
					t.Fatal("followed OAuth redirect")
				case endpoint:
					return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"https://foreign.invalid/sink"}}, Body: io.NopCloser(strings.NewReader("mock-secret"))}, nil
				}
				return base.RoundTrip(r)
			})
			if err := f.login(t.Context()); err == nil || strings.Contains(err.Error(), "mock-secret") || len(f.records) != 0 {
				t.Fatal("unsafe redirect result", err)
			}
		})
	}
}

func TestRefreshRequiresVerifiedSpend(t *testing.T) {
	f := newFixture(t)
	f.reply["expires_in"] = 1
	f.reply["refresh_token"] = "mock-refresh"
	if err := f.login(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.e.set = func(string, string, string) error { return nil } // silently ignored store write
	if access, id, err := f.e.token(t.Context(), "", nil); !errors.Is(err, errStorage) || access != "" || id != "" || f.posts != 1 {
		t.Fatal("refresh proceeded without a durable spent marker", err)
	}
}

func TestPublicLoginBindsWorkspaceBeforeReplacingGrant(t *testing.T) {
	f := newPublicFixture(t)
	ctx := t.Context()
	if err := f.login(ctx); err != nil {
		t.Fatal(err)
	}
	original := f.records[account]
	if strings.Contains(original, "client_secret") || strings.Contains(original, "code_verifier") {
		t.Fatal("unexpected credential in public record")
	}
	access, gen, err := f.e.token(ctx, "", nil)
	if err != nil || access != "mock-access" || gen == "" {
		t.Fatal("public grant unavailable", err)
	}

	f.reply["team"] = map[string]string{"id": "T99999"}
	if err := f.login(ctx); err == nil || !strings.Contains(err.Error(), "different workspace") || f.records[account] != original || f.posts != 2 {
		t.Fatal("wrong workspace replaced the previous grant", err)
	}
	f.reply["ok"], f.reply["error"] = false, "invalid_code"
	if err := f.login(ctx); !errors.Is(err, errToken) || f.records[account] != original || f.posts != 3 {
		t.Fatal("rejected authorization code was retried or saved", err)
	}
}

func TestPublicLoginPreconditions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture)
	}{
		{"missing team", func(f *fixture) { f.expectedTeam = "" }},
		{"channel as team", func(f *fixture) { f.expectedTeam = "C12345" }},
		{"invalid client ID", func(f *fixture) { f.app.ClientID = "client.invalid" }},
		{"secret in public profile", func(f *fixture) { f.app.Secret = "mock-secret" }},
		{"missing S256", func(f *fixture) { f.as["code_challenge_methods_supported"] = []string{"plain"} }},
		{"unrecognized token authority", func(f *fixture) { f.as["token_endpoint"] = "https://other.invalid/api" }},
		{"DPoP required", func(f *fixture) { f.res["dpop_bound_access_tokens_required"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPublicFixture(t)
			tc.change(f)
			if err := f.login(t.Context()); err == nil || f.openURL != "" || f.posts != 0 || len(f.records) != 0 {
				t.Fatal("opened browser or used an invalid public profile", err)
			}
		})
	}
	if (credentials{ClientID: "123.456"}).valid() {
		t.Fatal("incomplete legacy confidential enrollment became public")
	}
}

func TestPublicRefreshLostResponseRequiresNewConsent(t *testing.T) {
	f := newPublicFixture(t)
	f.reply["expires_in"], f.reply["refresh_token"] = 1, "mock-refresh"
	ctx := t.Context()
	if err := f.login(ctx); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.e.client.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != tokenEndpoint || f.records[account] != "{}" {
			t.Fatal("unexpected refresh attempt")
		}
		return nil, io.ErrUnexpectedEOF
	})
	for range 2 {
		if access, _, err := f.e.token(ctx, "", nil); !errors.Is(err, ErrLoginRequired) || access != "" {
			t.Fatal("ambiguous refresh authorized token use", err)
		}
	}
	if calls != 1 {
		t.Fatal("replayed single-use refresh", calls)
	}
}

func TestPublicBearerIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, user, team, bot string
		ok                    bool
	}{
		{name: "matching user", user: "U54321", team: "T12345", ok: true},
		{name: "different workspace", user: "U54321", team: "T99999"},
		{name: "bot", user: "U54321", team: "T12345", bot: "B12345"},
		{name: "incomplete identity", team: "T12345"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPublicFixture(t)
			f.reply["token_type"] = "Bearer"
			f.reply["scope"] = scopes
			delete(f.reply, "authed_user")
			base := f.e.client.Transport
			checks := 0
			f.e.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != identityEndpoint {
					return base.RoundTrip(r)
				}
				checks++
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer mock-access" {
					t.Fatal("unexpected identity request")
				}
				b, err := json.Marshal(map[string]any{"ok": true, "user_id": tc.user, "team_id": tc.team, "bot_id": tc.bot})
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}, err
			})
			err := f.login(t.Context())
			if checks != 1 || (err == nil) != tc.ok || (len(f.records) == 1) != tc.ok {
				t.Fatalf("identity checks=%d, stored=%d, login error=%v", checks, len(f.records), err)
			}
		})
	}
}
