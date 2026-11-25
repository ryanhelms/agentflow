package asanaoauth

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nscaledev/devflow/internal/provider/nativeoauth"
	"github.com/zalando/go-keyring"
)

const (
	testWorkspace = "1201000000000042"
	testUser      = "1201000000000011"
	testCode      = "fixture-private-code"
	testSecret    = "fixture-private-client-secret"
)

var testApp = registration{"1201000000000090", testSecret}

type wire func(*http.Request) (*http.Response, error)

func (w wire) RoundTrip(r *http.Request) (*http.Response, error) { return w(r) }

// Exercise the real callback handler over an in-process HTTP connection.
type listener struct {
	in   chan net.Conn
	done chan struct{}
	once sync.Once
}

func (l *listener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53669} }
func (l *listener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.in:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *listener) callback(ctx context.Context, host, path string, q url.Values) (int, error) {
	a, b := net.Pipe()
	defer a.Close()
	if err := a.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return 0, err
	}
	select {
	case l.in <- b:
	case <-ctx.Done():
		b.Close()
		return 0, ctx.Err()
	case <-l.done:
		b.Close()
		return 0, net.ErrClosed
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+path+"?"+q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	if err = req.Write(a); err != nil {
		return 0, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(a), req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "no-store" {
		return 0, errors.New("cacheable callback")
	}
	return resp.StatusCode, nil
}

type fixture struct {
	e                           enrollment
	record                      string
	l                           *listener
	auth                        url.Values
	callbackErr                 string
	tokenStatus, resourceStatus int
	failURL                     string
	prm, as, code, refresh, me  map[string]any
	tokens, profiles            int
	lastRefresh                 string
}

func document(t *testing.T, file string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", file+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{tokenStatus: 200, resourceStatus: 200, prm: document(t, "resource"), as: document(t, "server"), code: document(t, "code"), refresh: document(t, "refresh"), me: document(t, "me")}
	dir := filepath.Join(t.TempDir(), "lock")
	f.e = enrollment{client: nativeoauth.NewClient(), lockDir: func() (string, error) { return dir, nil }}
	f.e.client.Transport = wire(f.request)
	check := func(s, a string) {
		if s != "devflow.asanaoauth.v2.1" || a != "https://app.asana.com https://mcp.asana.com/v2" {
			t.Fatal("foreign keychain entry")
		}
	}
	f.e.get = func(s, a string) (string, error) {
		check(s, a)
		if f.record == "" {
			return "", keyring.ErrNotFound
		}
		return f.record, nil
	}
	f.e.set = func(s, a, value string) error { check(s, a); f.record = value; return nil }
	f.e.remove = func(s, a string) error { check(s, a); f.record = ""; return nil }
	f.e.listen = func(context.Context) (net.Listener, error) {
		f.l = &listener{in: make(chan net.Conn), done: make(chan struct{})}
		return f.l, nil
	}
	f.e.open = f.browser
	return f
}

func (f *fixture) browser(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	q := u.Query()
	f.auth = q
	want := url.Values{"client_id": {testApp.ID}, "resource": {"https://mcp.asana.com/v2"}, "response_type": {"code"},
		"redirect_uri": {"http://127.0.0.1:53669/oauth/asana/callback"}, "state": {q.Get("state")}, "code_challenge_method": {"S256"}, "code_challenge": {q.Get("code_challenge")}}
	if u.Scheme+"://"+u.Host+u.Path != "https://app.asana.com/-/oauth_authorize" || !reflect.DeepEqual(want, q) ||
		len(q.Get("state")) < 24 || len(q.Get("code_challenge")) != 43 {
		return errors.New("incorrect browser URL")
	}
	// Invalid callbacks cannot consume the consent or reach token exchange.
	for _, bad := range []struct {
		host, path string
		state      []string
	}{
		{"localhost:53669", callbackPath, []string{q.Get("state")}},
		{callbackHost, callbackPath + "/", []string{q.Get("state")}},
		{callbackHost, callbackPath, []string{"other-state"}},
		{callbackHost, callbackPath, []string{q.Get("state"), q.Get("state")}},
	} {
		status, err := f.l.callback(ctx, bad.host, bad.path, url.Values{"code": {testCode}, "state": bad.state})
		if err != nil || status != 400 {
			return errors.New("invalid callback accepted")
		}
	}
	reply := url.Values{"code": {testCode}, "state": {q.Get("state")}}
	if f.callbackErr == "issuer" {
		reply.Set("iss", "https://different.invalid")
	} else if f.callbackErr != "" {
		reply.Del("code")
		reply.Set("error", f.callbackErr)
	}
	_, err = f.l.callback(ctx, callbackHost, callbackPath, reply)
	return err
}

func response(status int, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, err
}
func (f *fixture) request(r *http.Request) (*http.Response, error) {
	if r.URL.String() == f.failURL {
		return nil, errors.New(testSecret)
	}
	if r.URL.String() != Endpoint && r.Header.Get("Authorization") != "" {
		return nil, errors.New("bearer on wrong endpoint")
	}
	switch r.URL.String() {
	case resourceMeta, serverMeta:
		if r.Method != "GET" {
			return nil, errors.New("unexpected metadata method")
		}
		if r.URL.String() == resourceMeta {
			return response(200, f.prm)
		}
		return response(200, f.as)
	case tokenEndpoint:
		f.tokens++
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.ParseForm() != nil {
			return nil, errors.New("unexpected token request")
		}
		form := r.PostForm
		want := url.Values{"grant_type": {form.Get("grant_type")}, "client_id": {testApp.ID}, "client_secret": {testSecret}, "resource": {"https://mcp.asana.com/v2"}}
		reply := f.code
		switch form.Get("grant_type") {
		case "authorization_code":
			v := form.Get("code_verifier")
			sum := sha256.Sum256([]byte(v))
			if len(v) < 43 || base64.RawURLEncoding.EncodeToString(sum[:]) != f.auth.Get("code_challenge") {
				return nil, errors.New("incorrect PKCE verifier")
			}
			want.Set("code", testCode)
			want.Set("code_verifier", v)
			want.Set("redirect_uri", redirectURI)
		case "refresh_token":
			if f.record != refreshPending {
				return nil, errors.New("active grant during refresh")
			}
			f.lastRefresh = form.Get("refresh_token")
			want.Set("refresh_token", f.lastRefresh)
			reply = f.refresh
		default:
			return nil, errors.New("unexpected grant")
		}
		if !reflect.DeepEqual(form, want) {
			return nil, errors.New("unexpected token parameters")
		}
		return response(f.tokenStatus, reply)
	case Endpoint:
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+fmt.Sprint(f.code["access_token"]) && r.Header.Get("Authorization") != "Bearer "+fmt.Sprint(f.refresh["access_token"]) {
			return nil, errors.New("incorrect authenticated request")
		}
		if f.resourceStatus != 200 {
			return response(f.resourceStatus, map[string]any{"errors": []any{map[string]any{"message": testSecret}}})
		}
		var rpc struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			return nil, err
		}
		var result any
		switch rpc.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "serverInfo": map[string]any{"name": "Asana MCP", "version": "2"}, "capabilities": map[string]any{"tools": map[string]any{}}}
		case "notifications/initialized":
			return response(202, nil)
		case "tools/call":
			if rpc.Params.Name != "get_me" || len(rpc.Params.Arguments) != 0 {
				return nil, errors.New("unexpected Asana tool")
			}
			f.profiles++
			me, _ := json.Marshal(f.me)
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": string(me)}}}
		default:
			return nil, errors.New("unexpected MCP method")
		}
		return response(200, map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
	default:
		return nil, errors.New("request outside pinned endpoints")
	}
}

func (f *fixture) login(ctx context.Context) error { return f.e.login(ctx, testApp, testWorkspace) }

func TestEnrollmentLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	t.Setenv("ASANA_ACCESS_TOKEN", "unrelated-fixture")
	if _, err := f.e.bound(ctx, "", nil); !errors.Is(err, ErrNotEnrolled) {
		t.Fatal(err)
	}
	if err := Login(ctx, strings.NewReader(testSecret), io.Discard); err == nil {
		t.Fatal("non-terminal login")
	}
	if err := f.login(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := f.e.bound(ctx, "", nil)
	if err != nil || b.User != testUser || b.Workspace != testWorkspace || b.Generation == "" || f.profiles != 1 {
		t.Fatal("invalid binding", err)
	}
	for _, v := range []string{testCode, "fixture-access", "fixture@example.invalid", f.auth.Get("state"), f.auth.Get("code_challenge")} {
		if strings.Contains(f.record, v) {
			t.Fatal("ephemeral material in keychain")
		}
	}
	state, challenge := f.auth.Get("state"), f.auth.Get("code_challenge")
	dispatchErr := errors.New("dispatch outcome")
	calls := 0
	_, err = f.e.bound(ctx, b.Generation, func(access string) error {
		calls++
		if access != f.code["access_token"] {
			t.Fatal("wrong access token")
		}
		// The child has its own process, lock descriptor and heap.
		dir, _ := f.e.lockDir()
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLeaseChild$")
		child.Env = []string{"DEVFLOW_TEST_ASANA_LOCK=" + dir}
		if err := child.Run(); err != nil {
			t.Fatal("dispatch lease did not exclude another process", err)
		}
		return dispatchErr
	})
	if !errors.Is(err, dispatchErr) || calls != 1 {
		t.Fatal("dispatch replay or lost error")
	}
	if err := f.login(ctx); err != nil {
		t.Fatal(err)
	}
	next, err := f.e.bound(ctx, "", nil)
	if err != nil || next.Generation == b.Generation || f.auth.Get("state") == state || f.auth.Get("code_challenge") == challenge {
		t.Fatal("reused consent material", err)
	}
	never := func(string) error { t.Fatal("stale dispatch"); return nil }
	if _, err := f.e.bound(ctx, b.Generation, never); !errors.Is(err, ErrGrantChanged) {
		t.Fatal(err)
	}
	if err := f.e.disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.bound(ctx, next.Generation, never); !errors.Is(err, ErrGrantChanged) {
		t.Fatal(err)
	}
	if _, err := f.e.bound(ctx, "", nil); !errors.Is(err, ErrNotEnrolled) {
		t.Fatal(err)
	}
}

func TestLeaseChild(t *testing.T) {
	dir := os.Getenv("DEVFLOW_TEST_ASANA_LOCK")
	if dir == "" {
		t.Skip("subprocess fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	lock, err := nativeoauth.Lock(ctx, func() (string, error) { return dir, nil })
	if lock != nil {
		lock.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("competing process acquired enrollment lock")
	}
}

func TestRefreshKeepsBinding(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if err := f.login(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := f.e.bound(ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, rotate := range []bool{false, true, false} {
		// A new process starts with an empty in-memory access cache.
		f.e.cached.Store(nil)
		wantRefresh := f.code["refresh_token"]
		if i == 2 {
			wantRefresh = "1/fixture-refresh:rotated"
		}
		delete(f.refresh, "data") // The MCP self lookup must still match.
		if rotate {
			f.refresh["refresh_token"] = "1/fixture-refresh:rotated"
		} else {
			delete(f.refresh, "refresh_token")
		}
		b, err := f.e.bound(ctx, "", nil)
		if err != nil || b != first || f.lastRefresh != wantRefresh || f.profiles != i+2 {
			t.Fatal("incorrect refresh binding", err)
		}
		_, err = f.e.bound(ctx, b.Generation, func(access string) error {
			if access != f.refresh["access_token"] {
				t.Fatal("stale access token")
			}
			return nil
		})
		if err != nil || f.tokens != i+2 {
			t.Fatal("lease replayed refresh", err)
		}
	}
}

func TestFailuresStayDistinct(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture)
		want   error
	}{
		{"expired", func(f *fixture) {
			f.tokenStatus = 400
			f.refresh = map[string]any{"error": "invalid_grant", "error_description": testSecret}
		}, ErrLoginRequired},
		{"workspace-disabled-401", func(f *fixture) { f.resourceStatus = 401 }, ErrDenied},
		{"workspace-policy-403", func(f *fixture) { f.resourceStatus = 403 }, ErrDenied},
		{"client-denied", func(f *fixture) {
			f.tokenStatus = 400
			f.refresh = map[string]any{"error": "unauthorized_client", "error_description": testSecret}
		}, ErrDenied},
		{"token-network", func(f *fixture) { f.failURL = tokenEndpoint }, ErrTransport},
		{"mcp-network", func(f *fixture) { f.failURL = Endpoint }, ErrTransport},
		{"rate-limited", func(f *fixture) { f.tokenStatus = 429 }, ErrTransport},
		{"resource-mismatch", func(f *fixture) { f.refresh["resource"] = Origin + "/sse" }, ErrGrantChanged},
		{"api-scope", func(f *fixture) { f.refresh["scope"] = "tasks:read" }, ErrGrantChanged},
		{"changed-user", func(f *fixture) { f.refresh["data"] = map[string]any{"gid": "1201000000000099"} }, ErrGrantChanged},
		{"conflicting-legacy-id", func(f *fixture) { f.refresh["data"] = map[string]any{"gid": testUser, "id": 1234} }, ErrGrantChanged},
		{"changed-mcp-user", func(f *fixture) { delete(f.refresh, "data"); f.me["gid"] = "1201000000000099" }, ErrGrantChanged},
		{"changed-workspace", func(f *fixture) { f.me["workspaces"] = []any{map[string]string{"gid": "1201000000000080"}} }, ErrGrantChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := t.Context()
			if err := f.login(ctx); err != nil {
				t.Fatal(err)
			}
			b := f.e.cached.Load().binding
			f.e.cached.Store(nil)
			tc.change(f)
			for range 2 {
				_, err := f.e.bound(ctx, b.Generation, func(string) error { t.Fatal("dispatched after failed refresh"); return nil })
				if !errors.Is(err, tc.want) || errors.Is(err, ErrNotEnrolled) || strings.Contains(err.Error(), testSecret) {
					t.Fatal("wrong error class", err)
				}
			}
			if f.tokens > 2 || strings.Contains(f.record, "refresh:original") {
				t.Fatal("retried uncertain refresh")
			}
		})
	}
}

func TestMetadataAndConsent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture)
		want   error
	}{
		{"api-resource", func(f *fixture) { f.prm["resource"] = "https://app.asana.com/api/1.0" }, errMetadata},
		{"sse-resource", func(f *fixture) { f.prm["resource"] = Origin + "/sse" }, errMetadata},
		{"foreign-issuer", func(f *fixture) { f.prm["authorization_servers"] = []string{"https://other.invalid"} }, errMetadata},
		{"wrong-token-endpoint", func(f *fixture) { f.as["token_endpoint"] = "https://other.invalid/token" }, errMetadata},
		{"public-client", func(f *fixture) { f.as["token_endpoint_auth_methods_supported"] = []string{"none"} }, errMetadata},
		{"no-pkce", func(f *fixture) { f.as["code_challenge_methods_supported"] = []string{"plain"} }, errMetadata},
		{"discovery-network", func(f *fixture) { f.failURL = resourceMeta }, ErrTransport},
		{"consent-denied", func(f *fixture) { f.callbackErr = "access_denied" }, ErrDenied},
		{"wrong-issuer", func(f *fixture) { f.callbackErr = "issuer" }, ErrDenied},
		{"required-issuer-omitted", func(f *fixture) { f.as["authorization_response_iss_parameter_supported"] = true }, ErrDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.change(f)
			if err := f.login(t.Context()); !errors.Is(err, tc.want) || f.tokens != 0 || f.record != "" {
				t.Fatal("unsafe enrollment", err)
			}
			if !errors.Is(tc.want, ErrDenied) && f.auth != nil {
				t.Fatal("opened browser after invalid discovery")
			}
		})
	}
}

func TestFailClosedKeychainAndGenerations(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if err := WithBoundToken(ctx, "", func(string) error { return nil }); !errors.Is(err, ErrGrantChanged) {
		t.Fatal(err)
	}
	if err := WithBoundToken(ctx, "anything", nil); !errors.Is(err, ErrGrantChanged) {
		t.Fatal(err)
	}
	if err := f.login(ctx); err != nil {
		t.Fatal(err)
	}
	generation := f.e.cached.Load().binding.Generation
	// No other client's record, bearer, or record for the retired SSE resource
	// can become a Devflow enrollment even when a bearer remains in memory.
	for _, bad := range []string{`{"access_token":"foreign-client"}`,
		strings.Replace(f.record, Resource, Origin+"/sse", 1),
		strings.Replace(f.record, testWorkspace, "1201000000000080", 1),
		strings.Replace(f.record, testApp.ID, "1201000000000091", 1),
		strings.Replace(f.record, testUser, "1201000000000050", 1),
		"truncated",
	} {
		f.record = bad
		if _, err := f.e.bound(ctx, generation, func(string) error { t.Fatal("released bearer for foreign record"); return nil }); !errors.Is(err, ErrGrantChanged) {
			t.Fatal(err)
		}
	}
	f.e.get = func(string, string) (string, error) { return "", errors.New(testSecret) }
	if _, err := f.e.bound(ctx, "", nil); !errors.Is(err, errStorage) || strings.Contains(err.Error(), testSecret) {
		t.Fatal(err)
	}
	if err := f.e.disconnect(ctx); !errors.Is(err, errStorage) {
		t.Fatal("unverified deletion", err)
	}
}

func TestLoginRequiresUsableV2Grant(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture)
		want   error
	}{
		{"expired-code", func(f *fixture) { f.tokenStatus = 400; f.code = map[string]any{"error": "invalid_grant"} }, ErrLoginRequired},
		{"rest-token", func(f *fixture) { f.code["access_token"] = "fixture-standard-api-token"; f.resourceStatus = 401 }, ErrDenied},
		{"sse-token", func(f *fixture) { f.code["resource"] = Origin + "/sse" }, ErrGrantChanged},
		{"missing-refresh", func(f *fixture) { delete(f.code, "refresh_token") }, errResponse},
		{"token-control-character", func(f *fixture) { f.code["access_token"] = "fixture\r\ninvalid" }, errResponse},
		{"wrong-workspace", func(f *fixture) { f.me["workspaces"] = []map[string]string{{"gid": "1201000000000001"}} }, ErrGrantChanged},
		{"mcp-account-mismatch", func(f *fixture) { f.me["gid"] = "1201000000000050" }, ErrGrantChanged},
		{"keychain-failure", func(f *fixture) { f.e.set = func(string, string, string) error { return errors.New(testSecret) } }, errStorage},
		{"silent-keychain-failure", func(f *fixture) { f.e.set = func(string, string, string) error { return nil } }, errStorage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.change(f)
			if err := f.login(t.Context()); !errors.Is(err, tc.want) || strings.Contains(err.Error(), testSecret) {
				t.Fatal("wrong enrollment error", err)
			}
			if f.record != "" || f.e.cached.Load() != nil {
				t.Fatal("published unsuccessful enrollment")
			}
		})
	}
}
