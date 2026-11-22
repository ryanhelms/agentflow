package rovooauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (rt roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return rt(r) }

func jsonReply(v any) *http.Response {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(string(data)))}
}

// Exercise the real HTTP callback handler and PKCE exchange without opening a
// network socket or interacting with a browser profile.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipe() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}
func (p *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-p.conns:
		return c, nil
	case <-p.closed:
		return nil, net.ErrClosed
	}
}
func (p *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 49201} }
func (p *pipeListener) Close() error   { p.once.Do(func() { close(p.closed) }); return nil }
func (p *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	a, b := net.Pipe()
	select {
	case p.conns <- b:
		return a, nil
	case <-ctx.Done():
	case <-p.closed:
	}
	_ = a.Close()
	_ = b.Close()
	return nil, net.ErrClosed
}

const asMeta = "https://auth.atlassian.com/.well-known/oauth-authorization-server/VCeDsk8ZHncYF1g234fKtc4lNipbBhu3"

type fixture struct {
	e                                 *enrollment
	resource, as, registration, token map[string]any
	ln                                *pipeListener
	storeMu                           sync.Mutex
	records                           map[string]string
	opens, posts                      atomic.Int32
	redirect, challenge               string
	requests                          []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "user-lock")
	f := &fixture{
		records: make(map[string]string),
		resource: map[string]any{"resource": Resource, "authorization_servers": []string{issuer},
			"bearer_methods_supported": []string{"header"}, "scopes_supported": strings.Fields(scope + " manage:jira:agent-interface")},
		as: map[string]any{"issuer": issuer, "authorization_endpoint": authorizeEndpoint, "token_endpoint": tokenEndpoint,
			"registration_endpoint": registerEndpoint, "response_types_supported": []string{"code"},
			"code_challenge_methods_supported": []string{"S256"}, "grant_types_supported": []string{"authorization_code", "refresh_token"},
			"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post"}},
		registration: map[string]any{"client_id": "devflow-fixture-client", "client_secret": "discarded-secret", "token_endpoint_auth_method": "none"},
		token:        map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh", "expires_in": 3600, "token_type": "Bearer", "scope": scope},
	}
	f.e = &enrollment{client: newClient(),
		lockDir: func() (string, error) { return dir, nil },
		listen:  func(context.Context) (net.Listener, error) { f.ln = newPipe(); return f.ln, nil },
		get: func(s, a string) (string, error) {
			if s != service {
				t.Fatal("unexpected keyring service")
			}
			f.storeMu.Lock()
			defer f.storeMu.Unlock()
			raw, ok := f.records[a]
			if !ok {
				return "", keyring.ErrNotFound
			}
			return raw, nil
		},
		set: func(s, a, raw string) error {
			if s != service || len(raw) > chunkSize {
				t.Fatal("invalid keyring write")
			}
			f.storeMu.Lock()
			defer f.storeMu.Unlock()
			f.records[a] = raw
			return nil
		},
		remove: func(s, a string) error {
			if s != service || a == account {
				t.Fatal("invalid keyring deletion")
			}
			f.storeMu.Lock()
			defer f.storeMu.Unlock()
			if _, ok := f.records[a]; !ok {
				return keyring.ErrNotFound
			}
			delete(f.records, a)
			return nil
		},
	}
	f.e.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		address := r.URL.String()
		f.requests = append(f.requests, address)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected credential header")
		}
		switch address {
		case resourceMeta:
			return jsonReply(f.resource), nil
		case asMeta:
			return jsonReply(f.as), nil
		case registerEndpoint:
			var data map[string]any
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&data) != nil {
				t.Error("invalid DCR request")
			}
			uris := data["redirect_uris"].([]any)
			f.redirect = uris[0].(string)
			if data["client_name"] != "Devflow" || data["application_type"] != "native" || data["token_endpoint_auth_method"] != "none" ||
				data["scope"] != scope || !slices.Equal(data["grant_types"].([]any), []any{"authorization_code", "refresh_token"}) ||
				f.redirect != "http://"+f.ln.Addr().String()+callbackPath {
				t.Error("incorrect DCR metadata")
			}
			if _, ok := f.registration["redirect_uris"]; !ok {
				f.registration["redirect_uris"] = uris
			}
			return jsonReply(f.registration), nil
		case tokenEndpoint:
			f.posts.Add(1)
			if r.Method != "POST" || r.ParseForm() != nil || r.PostForm.Get("resource") != Resource ||
				r.PostForm.Get("client_id") != "devflow-fixture-client" || r.PostForm.Has("client_secret") {
				t.Error("invalid token request")
			}
			switch r.PostForm.Get("grant_type") {
			case "authorization_code":
				if r.PostForm.Get("redirect_uri") != f.redirect || r.PostForm.Get("code") != "fixture-code" ||
					oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier")) != f.challenge {
					t.Error("code, callback or S256 mismatch")
				}
			case "refresh_token":
				if !strings.HasPrefix(r.PostForm.Get("refresh_token"), "fixture-refresh") {
					t.Error("invalid refresh credential")
				}
			default:
				t.Error("unexpected grant")
			}
			return jsonReply(f.token), nil
		default:
			return nil, errors.New("unexpected destination")
		}
	})
	f.e.open = func(ctx context.Context, address string) error {
		f.opens.Add(1)
		u, err := url.Parse(address)
		if err != nil {
			return err
		}
		q := u.Query()
		if u.Scheme+"://"+u.Host+u.Path != authorizeEndpoint || q.Get("client_id") != "devflow-fixture-client" ||
			q.Get("redirect_uri") != f.redirect || q.Get("resource") != Resource || q.Get("scope") != scope ||
			q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" || len(q.Get("state")) < 26 {
			return errors.New("incorrect browser request")
		}
		f.challenge = q.Get("code_challenge")
		_, err = callbackRequest(ctx, f.ln, f.redirect, "", url.Values{"code": {"fixture-code"}, "state": {q.Get("state")}, "iss": {issuer}})
		return err
	}
	return f
}

func callbackRequest(ctx context.Context, ln *pipeListener, address, host string, q url.Values) (int, error) {
	tr := &http.Transport{DialContext: ln.dial, DisableKeepAlives: true}
	req, err := http.NewRequestWithContext(ctx, "GET", address+"?"+q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	if host != "" {
		req.Host = host
	}
	c := http.Client{Transport: tr}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, err
}

func TestEnrollmentAndRotation(t *testing.T) {
	f := newFixture(t)
	// Exercise package entry points. This fixture never reads an OS store.
	old := desktop
	desktop = f.e
	t.Cleanup(func() { desktop = old })
	t.Setenv("ATLASSIAN_API_TOKEN", "synthetic-environment-value")
	if tok, err := Token(t.Context()); tok != "" || !errors.Is(err, ErrNotEnrolled) || len(f.requests) != 0 || f.opens.Load() != 0 {
		t.Fatal("unenrolled lookup did not fail locally")
	}
	if err := Login(t.Context()); err != nil {
		t.Fatal(err)
	}
	tok, gen, err := BoundToken(t.Context())
	if err != nil || tok != "fixture-access" || gen == "" {
		t.Fatal("missing enrolled grant", err)
	}
	if strings.Contains(f.records[account], tok) || strings.Contains(f.records[account], "discarded-secret") || !strings.Contains(f.records[account], "fixture-refresh") {
		t.Fatal("incorrect keyring record")
	}
	f.e.cached.Store(nil) // freshly launched Desktop process
	f.token["refresh_token"] = "fixture-refresh-rotated"
	f.token["access_token"] = "fixture-access-2"
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			a, g, err := BoundToken(t.Context())
			if err != nil || a != "fixture-access-2" || g != gen {
				t.Error("bad refreshed grant", err)
			}
		})
	}
	wg.Wait()
	if f.posts.Load() != 2 || f.opens.Load() != 1 || !strings.Contains(f.records[account], "fixture-refresh-rotated") {
		t.Fatal("refresh was repeated or not stored")
	}
	f.e.cached.Store(nil)
	delete(f.token, "refresh_token")
	if _, _, err := BoundToken(t.Context()); err != nil || !strings.Contains(f.records[account], "fixture-refresh-rotated") {
		t.Fatal("nonrotating refresh lost existing token", err)
	}
	f.token["refresh_token"] = "fixture-refresh-new-login"
	if err := Login(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, newGen, err := BoundToken(t.Context())
	if err != nil || newGen == gen {
		t.Fatal("reenrollment did not replace generation", err)
	}
}

func TestDiscoveryFailsBeforeRegistration(t *testing.T) {
	for _, tc := range []struct {
		which, field string
		value        any
	}{
		{"resource", "resource", Resource + "?tools=all"}, {"resource", "authorization_servers", []string{"https://auth.atlassian.com"}},
		{"resource", "scopes_supported", []string{"read:me"}}, {"resource", "bearer_methods_supported", []string{"query"}},
		{"as", "issuer", issuer + "/"}, {"as", "registration_endpoint", "https://untrusted.invalid/register"},
		{"as", "authorization_endpoint", "https://untrusted.invalid/authorize"}, {"as", "token_endpoint", tokenEndpoint + "?next=x"},
		{"as", "code_challenge_methods_supported", []string{"plain"}}, {"as", "token_endpoint_auth_methods_supported", []string{"client_secret_post"}},
		{"as", "grant_types_supported", []string{"authorization_code"}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			f := newFixture(t)
			if tc.which == "as" {
				f.as[tc.field] = tc.value
			} else {
				f.resource[tc.field] = tc.value
			}
			if err := f.e.login(t.Context()); !errors.Is(err, errMetadata) || f.opens.Load() != 0 || len(f.requests) > 2 || f.records[account] != "" {
				t.Fatal("unsafe metadata accepted", err)
			}
		})
	}
}

func TestRegistrationAndTokenFailures(t *testing.T) {
	for _, tc := range []struct {
		field        string
		value        any
		registration bool
	}{
		{"redirect_uris", []string{"http://localhost/changed"}, true}, {"token_endpoint_auth_method", "client_secret_post", true},
		{"scope", scope + " email", true}, {"scope", "read:me", false}, {"scope", scope + " email", false},
		{"token_type", "DPoP", false}, {"access_token", "invalid\r\nheader", false}, {"access_token", "wrong,other", false}, {"expires_in", 0, false}, {"refresh_token", "", false},
	} {
		t.Run(tc.field, func(t *testing.T) {
			f := newFixture(t)
			if tc.registration {
				f.registration[tc.field] = tc.value
			} else {
				f.token[tc.field] = tc.value
			}
			err := f.e.login(t.Context())
			if err == nil || f.records[account] != "" || strings.Contains(err.Error(), "fixture") || (tc.registration && f.opens.Load() != 0) {
				t.Fatal("failure leaked or accepted")
			}
		})
	}
}

func TestCallbackChecks(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		q                          url.Values
		requireIssuer, wantSuccess bool
	}{
		{"matching issuer", url.Values{"code": {"ok"}, "iss": {issuer}}, true, true},
		{"issuer optional", url.Values{"code": {"ok"}}, false, true},
		{"issuer required", url.Values{"code": {"ok"}}, true, false},
		{"wrong issuer", url.Values{"code": {"ok"}, "iss": {"https://other.invalid"}}, false, false},
		{"duplicated code", url.Values{"code": {"ok", "also"}}, false, false},
		{"denied", url.Values{"error": {"denied"}, "error_description": {"private-description"}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			p := newPipe()
			endpoint := "http://" + p.Addr().String() + callbackPath
			tc.q.Set("state", "expected")
			code, err := waitCallback(ctx, p, "expected", tc.requireIssuer, func() error {
				for _, bad := range []struct {
					addr, host string
					state      []string
				}{
					{endpoint, "foreign.invalid", []string{"expected"}}, {endpoint + "/wrong", "", []string{"expected"}},
					{endpoint, "", []string{"wrong"}}, {endpoint, "", []string{"expected", "wrong"}},
				} {
					status, err := callbackRequest(ctx, p, bad.addr, bad.host, url.Values{"state": bad.state, "code": {"premature"}})
					if err != nil || status != 400 {
						return fmt.Errorf("invalid callback status %d", status)
					}
				}
				_, err := callbackRequest(ctx, p, endpoint, "", tc.q)
				return err
			})
			if (err == nil) != tc.wantSuccess || (err == nil && code != "ok") || (err != nil && !strings.Contains(err.Error(), "consent denied")) {
				t.Fatalf("callback result %q, %v", code, err)
			}
		})
	}
}

func TestRedirectsNeverFollowed(t *testing.T) {
	for _, where := range []string{resourceMeta, asMeta, registerEndpoint, tokenEndpoint} {
		t.Run(where, func(t *testing.T) {
			f := newFixture(t)
			base := f.e.client.Transport
			f.e.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "elsewhere.invalid" {
					t.Error("redirect reached untrusted host")
					return nil, errors.New("redirect")
				}
				if r.URL.String() != where {
					return base.RoundTrip(r)
				}
				resp := jsonReply(map[string]string{"error": "synthetic-secret"})
				resp.StatusCode = http.StatusTemporaryRedirect
				resp.Header.Set("Location", "https://elsewhere.invalid/receive")
				return resp, nil
			})
			if err := f.e.login(t.Context()); err == nil || strings.Contains(err.Error(), "synthetic-secret") || f.records[account] != "" {
				t.Fatal("redirect accepted or response exposed")
			}
		})
	}
}

func TestStoreAndRefreshFailures(t *testing.T) {
	f := newFixture(t)
	get := f.e.get
	f.e.get = func(string, string) (string, error) { return "", errors.New("secret keyring output") }
	if a, g, err := f.e.boundToken(t.Context()); a != "" || g != "" || !errors.Is(err, errStorage) || strings.Contains(err.Error(), "secret") || f.opens.Load() != 0 {
		t.Fatal("unsafe store failure")
	}
	f.e.get = get
	if err := f.e.login(t.Context()); err != nil {
		t.Fatal(err)
	}

	f.e.cached.Store(nil)
	f.token["error"] = "secret provider output"
	if a, g, err := f.e.boundToken(t.Context()); a != "" || g != "" || !errors.Is(err, errToken) {
		t.Fatal("unsafe exchange failure", err)
	}
	if _, _, err := f.e.boundToken(t.Context()); !errors.Is(err, ErrLoginRequired) || f.posts.Load() != 2 {
		t.Fatal("spent refresh token replayed", err)
	}
	delete(f.token, "error")
	f.token["refresh_token"] = "fixture-refresh"
	f.e.set = func(string, string, string) error { return errors.New("secret store output") }
	if err := f.e.login(t.Context()); !errors.Is(err, errStorage) || strings.Contains(err.Error(), "secret") {
		t.Fatal("unsafe persistence failure")
	}
}

func TestNetworkPolicy(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::ffff:127.0.0.1", "10.0.1.2", "100.64.0.2", "169.254.169.254", "::1", "::", "fe80::1", "fc00::1", "2002:7f00:1::", "224.0.0.1"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Errorf("accepted %s", ip)
		}
	}
	if !publicIP(netip.MustParseAddr("1.1.1.1")) || !publicIP(netip.MustParseAddr("2606:4700:4700::1111")) {
		t.Fatal("rejected public IP")
	}
	if newClient().Transport.(*http.Transport).Proxy != nil {
		t.Fatal("ambient proxy enabled")
	}
}

func TestNativeLoopback(t *testing.T) {
	// Some build sandboxes disallow socket binding. CI on an ordinary host
	// exercises the native listener as well as the in-memory protocol fixture.
	var lc net.ListenConfig
	probe, err := lc.Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) {
		t.Skip("loopback bind forbidden in this environment")
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = probe.Close()
	ln, err := listenCallback(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if addr := ln.Addr().(*net.TCPAddr); addr.Port == 0 || !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatal("listener is not literal IPv4 loopback")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	code, err := waitCallback(ctx, ln, "native-state", false, func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", "http://"+ln.Addr().String()+callbackPath+"?state=native-state&code=native-fixture", nil)
		if err != nil {
			return err
		}
		c := http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := c.Do(req)
		if err != nil {
			return err
		}
		return resp.Body.Close()
	})
	if err != nil || code != "native-fixture" {
		t.Fatal("native callback failed", err)
	}

}

func TestGrantLockAcrossProcesses(t *testing.T) {
	const key = "DEVFLOW_TEST_ROVO_LOCK_DIR"
	dir := os.Getenv(key)
	if dir != "" {
		e := enrollment{lockDir: func() (string, error) { return dir, nil }}
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		f, err := e.lock(ctx)
		if os.Getenv("DEVFLOW_TEST_ROVO_EXPECT_BUSY") == "yes" {
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("another process acquired the held grant lock")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		return
	}
	dir = filepath.Join(t.TempDir(), "lock")
	e := enrollment{lockDir: func() (string, error) { return dir, nil }}
	f, err := e.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	check := func(expect string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestGrantLockAcrossProcesses$")
		cmd.Env = append(os.Environ(), key+"="+dir, "DEVFLOW_TEST_ROVO_EXPECT_BUSY="+expect)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child lock test failed: %v %s", err, out)
		}
	}
	check("yes")
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	check("no")
}

func TestQuietToken(t *testing.T) {
	const child = "DEVFLOW_TEST_QUIET_ROVO"
	const canary = "synthetic-private-keyring-canary"
	if os.Getenv(child) == "yes" {
		f := newFixture(t)
		f.token["refresh_token"] = "fixture-refresh-" + strings.Repeat(canary, 100)
		f.token["access_token"] = "access-" + canary
		desktop = f.e
		if err := Login(t.Context()); err != nil {
			t.Fatal(err)
		}
		if token, err := Token(t.Context()); err != nil || token != "access-"+canary {
			t.Fatal("missing access token")
		}
		f.e.cached.Store(nil)
		f.e.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
			reply := jsonReply(map[string]string{"error_description": canary})
			reply.StatusCode = http.StatusBadRequest
			return reply, nil
		})
		token, err := Token(t.Context())
		if token != "" || !errors.Is(err, ErrLoginRequired) || strings.Contains(err.Error(), canary) {
			t.Fatal("unsafe refresh failure")
		}
		f.e.get = func(string, string) (string, error) { return "", errors.New(canary) }
		if _, err := Token(t.Context()); err == nil || strings.Contains(err.Error(), canary) {
			t.Fatal("unsafe keyring failure")
		}
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestQuietToken$")
	cmd.Env = append(os.Environ(), child+"=yes")
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), canary) {
		t.Fatal("synthetic secret appeared in stdout or stderr")
	}
	if err != nil {
		t.Fatalf("redaction fixture failed: %v %s", err, out)
	}
}

func TestSilentStorageFailure(t *testing.T) {
	f := newFixture(t)
	if err := f.e.login(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.e.cached.Store(nil)
	f.e.set = func(string, string, string) error { return nil } // a backend acknowledged but left the old record
	if tok, g, err := f.e.boundToken(t.Context()); tok != "" || g != "" || !errors.Is(err, errStorage) || f.posts.Load() != 1 {
		t.Fatal("unverified keyring replacement allowed a refresh")
	}
}
