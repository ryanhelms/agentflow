package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/mcpserver"
	"github.com/nscaledev/devflow/internal/setup"
)

const fixtureAuth = "fixture-value-never-serialize"

func TestMain(m *testing.M) {
	// This test executable is also the raw pinned native MCP server, including
	// on Windows. It does not invoke a shell, a live desktop or a package manager.
	if slices.Equal(os.Args[1:], []string{"stdio"}) && os.Getenv("DEVFLOW_MCP_TEST_AUTH") == fixtureAuth {
		s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
		if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			os.Exit(7)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type companionTransport func(*http.Request) (*http.Response, error)

func (f companionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeclaredMCPSetup(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Chdir(root)
	t.Setenv("DEVFLOW_HOME", home)
	t.Setenv("DEVFLOW_NO_UPDATE", "1")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	const updateToken = "fixture-update-token"
	t.Setenv("GH_TOKEN", updateToken)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if b, err := exec.Command("git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v, %s", err, b)
	}
	write := func(name, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".devflow/config.yml", `code_hosting: {provider: none}
task_management: {provider: filesystem, settings: {root: .}}
knowledge_base: {provider: none}
devops:
  checks: {provider: none}
  deployments: {provider: none}
  releases: {provider: none}
secops: {provider: none}
notifications: {provider: none}
`)
	write(filepath.Join(home, "config.yml"), "autonomy: {local: auto}\n")
	const initial = `{"servers":{"devflow":{"command":"existing-launcher"},"another":{"command":"keep"},"devflow-legacy":{"command":"team-tool"}},"n":9007199254740993}`
	write(".vscode/mcp.json", initial)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.ToSlash(exe)
	if runtime.GOOS == "windows" {
		path = "/" + path
	}
	pin := fmt.Sprintf(`mcp:
- id: catalog
  providers: [filesystem]
  kind: stdio-mcp
  auth_env: DEVFLOW_MCP_TEST_AUTH
  args: [stdio]
  artifacts:
  - platform: {os: %s, arch: %s}
    version: "1.2.3"
    source: %s
    sha256: %x
- id: hosted
  providers: [filesystem]
  kind: remote-mcp
  endpoint: https://fixture.invalid/mcp
  auth_env: DEVFLOW_MCP_TEST_AUTH
- id: unused
  providers: [slack]
  kind: remote-mcp
  endpoint: https://inactive.invalid/mcp
`, runtime.GOOS, runtime.GOARCH, (&url.URL{Scheme: "file", Path: path}).String(), digest.Sum(nil))
	manifest := filepath.Join(home, "mcp-companions.yml")
	write(manifest, pin)
	t.Setenv("DEVFLOW_MCP_TEST_AUTH", fixtureAuth)
	server := mcp.NewServer(&mcp.Implementation{Name: "remote-fixture", Version: "1"}, nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	var methods []string
	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })
	status := 0
	failList := false
	http.DefaultTransport = companionTransport(func(req *http.Request) (*http.Response, error) {
		// Remove still checks the release policy when background updates are off.
		if req.Method == http.MethodGet && req.URL.Host == "api.github.com" &&
			req.URL.Path == "/repos/nscaledev/devflow/contents/policy/versions.json" &&
			req.Header.Get("Authorization") == "Bearer "+updateToken {
			rec := httptest.NewRecorder()
			rec.WriteHeader(http.StatusNotFound)
			return rec.Result(), nil
		}
		if req.URL.Host != "fixture.invalid" || req.Header.Get("Authorization") != "Bearer "+fixtureAuth {
			t.Errorf("unexpected credential destination %s %s", req.Method, req.URL.Host)
			return nil, fmt.Errorf("unexpected request")
		}
		rec := httptest.NewRecorder()
		if status != 0 {
			rec.Header().Set("Location", "https://redirect.invalid/")
			rec.WriteHeader(status)
			_, _ = rec.WriteString(fixtureAuth)
			return rec.Result(), nil
		}
		data, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		var rpc struct{ Method string }
		if err = json.Unmarshal(data, &rpc); err != nil {
			return nil, err
		}
		methods = append(methods, rpc.Method)
		if rpc.Method == "tools/list" && failList {
			rec.WriteHeader(http.StatusForbidden)
			_, _ = rec.WriteString(fixtureAuth)
			return rec.Result(), nil
		}
		if !slices.Contains([]string{"server/discover", "initialize", "notifications/initialized", "tools/list"}, rpc.Method) {
			t.Errorf("unexpected MCP method %s", rpc.Method)
		}
		req.Body = io.NopCloser(bytes.NewReader(data))
		handler.ServeHTTP(rec, req)
		return rec.Result(), nil
	})
	cli := func(want int, args ...string) *setup.ProviderSetupPlan {
		t.Helper()
		b := companionFixtureCLI(t, want, append([]string{"setup"}, args...)...)
		var result setup.ProviderSetupPlan
		if err := json.Unmarshal(b, &result); err != nil {
			t.Fatal(err)
		}
		return &result
	}
	cli(1, "verify", "--companion", "catalog", "--json")
	// Actual MCP apply, including issuing and consuming the existing gated token.
	ctx := t.Context()
	api, err := mcpserver.New(ctx, root, "vscode")
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = api.MCP().Run(ctx, st) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "headless", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(args map[string]any, result any, wantError bool) {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "devflow_setup_apply", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		out := res.Content[0].(*mcp.TextContent).Text
		if strings.Contains(out, fixtureAuth) || res.IsError != wantError {
			t.Fatalf("unexpected MCP result (error=%v)", res.IsError)
		}
		if !wantError && json.Unmarshal([]byte(out), result) != nil {
			t.Fatal("invalid MCP result")
		}
	}
	var pending app.Pending
	install := map[string]any{"companion": "catalog", "install": true}
	call(install, &pending, false)
	if pending.ConfirmToken == "" || pending.Status != "needs_confirmation" {
		t.Fatal("no install approval")
	}
	if _, err := os.Stat(filepath.Join(home, "mcp-companions")); !os.IsNotExist(err) {
		t.Fatal("preview wrote installation")
	}
	install["confirm_token"] = pending.ConfirmToken
	write(manifest, strings.Replace(pin, "1.2.3", "1.2.4", 1))
	call(install, nil, true)
	write(manifest, pin)
	var installed setup.ProviderSetupPlan
	call(install, &installed, false)
	if !installed.Ready || installed.Companions[0].Command == "" {
		t.Fatalf("bad installed companion: %+v", installed.Companions)
	}
	installedCommand := installed.Companions[0].Command
	if runtime.GOOS == "windows" {
		p := installedCommand
		data, err := os.ReadFile(filepath.Join(filepath.Dir(p), "current.txt"))
		if err != nil || string(data) != filepath.Base(p) || filepath.Ext(p) != ".exe" {
			t.Fatal("invalid Windows selection")
		}
	}
	checkConfig := func(wants ...string) string {
		t.Helper()
		data, err := os.ReadFile(".vscode/mcp.json")
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(fixtureAuth)) {
			t.Fatal("credential reached client configuration")
		}
		for _, want := range wants {
			if !strings.Contains(string(data), want) {
				t.Errorf("missing %q in client config", want)
			}
		}
		return string(data)
	}
	if checkConfig() != initial {
		t.Fatal("installation also registered a project client")
	}
	reg := map[string]any{"companion": "catalog", "client": "vscode"}
	pending = app.Pending{}
	call(reg, &pending, false)
	if pending.ConfirmToken == "" || checkConfig() != initial {
		t.Fatal("registration needs separate consent")
	}
	reg["confirm_token"] = pending.ConfirmToken
	reg["client"] = "cursor"
	call(reg, nil, true)
	reg["client"] = "vscode"
	call(reg, &installed, false)
	if !installed.Ready {
		t.Fatal("failed registration")
	}
	first := checkConfig("existing-launcher", "devflow-catalog", "${env:DEVFLOW_MCP_TEST_AUTH}", "9007199254740993")
	cli(0, "apply", "--client", "vscode", "--companion", "catalog", "--yes", "--json")
	if checkConfig() != first {
		t.Fatal("repeated registration changed bytes")
	}
	// A failed upgrade must leave both the previous selection and client entry usable.
	badPin := strings.Replace(pin, fmt.Sprintf("%x", digest.Sum(nil)), strings.Repeat("0", 64), 1)
	write(manifest, badPin)
	delete(install, "confirm_token")
	pending = app.Pending{}
	call(install, &pending, false)
	if pending.ConfirmToken == "" {
		t.Fatal("missing upgrade approval")
	}
	install["confirm_token"] = pending.ConfirmToken
	call(install, nil, true)
	write(manifest, pin)
	cli(0, "verify", "--client", "vscode", "--companion", "catalog", "--json")
	if checkConfig() != first {
		t.Fatal("failed upgrade modified registration")
	}
	cli(0, "apply", "--client", "vscode", "--companion", "hosted", "--yes", "--json")
	beforeRepair := checkConfig("devflow-hosted", "Bearer ${env:DEVFLOW_MCP_TEST_AUTH}", "existing-launcher", "devflow-catalog")
	// An approved endpoint change updates the reserved entry in place.
	write(manifest, strings.Replace(pin, "fixture.invalid/mcp", "fixture.invalid/v2", 1))
	cli(1, "verify", "--client", "vscode", "--companion", "hosted", "--json")
	cli(0, "apply", "--client", "vscode", "--companion", "hosted", "--yes", "--json")
	if got := checkConfig(); got != strings.Replace(beforeRepair, "fixture.invalid/mcp", "fixture.invalid/v2", 1) {
		t.Fatal("scoped repair changed more than the reviewed endpoint")
	}
	cli(0, "verify", "--companion", "hosted", "--client", "vscode", "--json")
	if !slices.Contains(methods, "server/discover") || !slices.Contains(methods, "tools/list") {
		t.Fatal("missing handshake")
	}
	failList = true
	failed := cli(1, "verify", "--companion", "hosted", "--json")
	if !strings.Contains(failed.Companions[0].Detail, "tools/list failed") {
		t.Fatal("missing list failure diagnostic")
	}
	failList = false
	for _, status = range []int{http.StatusUnauthorized, http.StatusTemporaryRedirect} {
		cli(1, "verify", "--companion", "hosted", "--json")
	}
	t.Setenv("DEVFLOW_MCP_TEST_AUTH", "")
	cli(1, "verify", "--companion", "hosted", "--json")
	// Remove must work offline after the declaration or credentials disappear.
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	preview := companionFixtureCLI(t, 0, "remove", "--dry-run")
	if !bytes.Contains(preview, []byte(`remove companion .vscode/mcp.json "devflow-hosted"`)) {
		t.Fatal("repair did not update the hosted entry's receipt")
	}
	const config = ".vscode/mcp.json"
	edited := strings.Replace(checkConfig(), "fixture.invalid/v2", "team.invalid/custom", 1)
	write(config, edited)
	const receipt = config + ".devflow-companions.json"
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	ownership := read(receipt)
	checkShared := func() {
		t.Helper()
		binary, err := os.Open(installedCommand)
		if err != nil {
			t.Fatal(err)
		}
		defer binary.Close()
		sum := sha256.New()
		if _, err := io.Copy(sum, binary); err != nil || !bytes.Equal(sum.Sum(nil), digest.Sum(nil)) {
			t.Fatal("shared companion executable changed")
		}
		dir := filepath.Dir(installedCommand)
		var selected string
		if runtime.GOOS == "windows" {
			selected = read(filepath.Join(dir, "current.txt"))
		} else if selected, err = os.Readlink(filepath.Join(dir, "current")); err != nil {
			t.Fatal(err)
		}
		if selected != filepath.Base(installedCommand) {
			t.Fatal("shared companion selection changed")
		}
	}
	report := string(companionFixtureCLI(t, 0, "remove", "--dry-run"))
	for _, want := range []string{`remove companion .vscode/mcp.json "devflow-catalog"`, `"devflow-hosted" (changed since registration`, `"devflow-legacy" (untracked`} {
		if !strings.Contains(report, want) {
			t.Errorf("dry run missing %q", want)
		}
	}
	if checkConfig() != edited || read(receipt) != ownership {
		t.Fatal("dry run changed project files")
	}
	checkShared()
	companionFixtureCLI(t, 0, "remove")
	remaining := checkConfig()
	decode := func(data string) map[string]any {
		t.Helper()
		var doc map[string]any
		decoder := json.NewDecoder(strings.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	expected := decode(edited)
	delete(expected["servers"].(map[string]any), "devflow-catalog")
	if !reflect.DeepEqual(decode(remaining), expected) {
		t.Fatal("remove changed entries outside the recorded native companion")
	}
	if _, err := os.Stat(receipt); !os.IsNotExist(err) {
		t.Fatalf("receipt after remove: %v", err)
	}
	checkShared()
	companionFixtureCLI(t, 0, "remove", "--force")
	if checkConfig() != remaining {
		t.Fatal("repeated removal changed the remaining project entries")
	}
}

func companionFixtureCLI(t *testing.T, want int, args ...string) []byte {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "result")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	prev := os.Stdout
	os.Stdout = out
	defer func() { os.Stdout = prev }()
	exit := Main(args)
	b, err := os.ReadFile(out.Name())
	if err != nil || exit != want || bytes.Contains(b, []byte(fixtureAuth)) {
		t.Fatalf("exit %d, want %d, output redacted, read error %v", exit, want, err)
	}
	return b
}
