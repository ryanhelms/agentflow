package codexhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type exchange struct {
	Expect string
	Send   []string
}

var credentialEnv = []string{
	"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN",
	"ATLASSIAN_API_TOKEN", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "SERVICE_SECRET",
}

func TestCall(t *testing.T) {
	dir := t.TempDir()
	trace := setupFake(t, dir, append(handshake(t, dir),
		exchange{inventoryRequest, []string{
			`{"method":"mcpServer/startupStatus/updated","params":{"name":"ambient","status":"ready"}}`,
			`{"id":3,"result":{"data":[],"nextCursor":"page-two"}}`,
		}},
		exchange{
			`{"id":4,"method":"mcpServerStatus/list","params":{"threadId":"scratch","serverName":"fixture","detail":"toolsAndAuthOnly","cursor":"page-two"}}`,
			[]string{`{"id":4,"result":{"data":[{"name":"fixture","environmentId":"local-env","authStatus":"oAuth","runtimeStatus":"connected","tools":{"write_note":{},"other_tool":{}}}]}}`},
		},
		exchange{
			`{"id":5,"method":"mcpServer/tool/call","params":{"threadId":"scratch","server":"fixture","tool":"write_note","arguments":{"body":"line one\nline two","id":9007199254740993},"environmentId":"local-env"}}`,
			[]string{`{"method":"mcpServer/tool/call/progress","params":{"message":"working"}}`,
				`{"id":5,"result":{"content":[{"type":"text","text":"ok"}],"structuredContent":{"id":9007199254740993},"_meta":{"source":"fixture"}}}`},
		},
	))
	got, err := Call(context.Background(), dir, "fixture", "write_note",
		map[string]any{"body": "line one\nline two", "id": int64(9007199254740993)})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"content":[{"type":"text","text":"ok"}],"structuredContent":{"id":9007199254740993},"_meta":{"source":"fixture"}}`
	if string(got) != want {
		t.Fatalf("result = %s, want %s", got, want)
	}
	checkTrace(t, trace)
}

const inventoryRequest = `{"id":3,"method":"mcpServerStatus/list","params":{"threadId":"scratch","serverName":"fixture","detail":"toolsAndAuthOnly"}}`
const availableTool = `{"id":3,"result":{"data":[{"name":"fixture","authStatus":"unsupported","tools":{"write_note":{}}}]}}`
const callRequest = `{"id":4,"method":"mcpServer/tool/call","params":{"threadId":"scratch","server":"fixture","tool":"write_note","arguments":{}}}`

func TestCallDiscoveryErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		kind  Kind
	}{
		{"missing server", `{"id":3,"result":{"data":[]}}`, Unavailable},
		{"missing tool", `{"id":3,"result":{"data":[{"name":"fixture","authStatus":"unsupported","tools":{}}]}}`, Unavailable},
		{"authentication", `{"id":3,"result":{"data":[{"name":"fixture","authStatus":"notLoggedIn","runtimeStatus":"authenticationRequired","tools":{}}]}}`, Authentication},
		{"tool discovery permission", `{"id":3,"result":{"data":[{"name":"fixture","authStatus":"unknown","tools":{},"toolsError":"403 Forbidden"}]}}`, Permission},
		{"incompatible host", `{"id":3,"error":{"code":-32601,"message":"method not found"}}`, Unavailable},
		{"failed server", `{"id":3,"result":{"data":[{"name":"fixture","authStatus":"unknown","runtimeStatus":"failed","tools":{}}]}}`, Failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			trace := setupFake(t, dir, append(handshake(t, dir), exchange{inventoryRequest, []string{tc.reply}}))
			_, err := Call(context.Background(), dir, "fixture", "write_note", nil)
			checkError(t, err, tc.kind)
			checkTrace(t, trace)
		})
	}
}

func TestCallFailureAfterDispatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reply   string
		control string
		kind    Kind
	}{
		{"rpc permission", `{"id":4,"error":{"code":-32000,"message":"permission denied: fake-secret"}}`, "", Permission},
		{"tool failure", `{"id":4,"result":{"isError":true,"content":[{"type":"text","text":"failed: fake-secret"}]}}`, "", Failure},
		{"elicitation", `{"id":"control","method":"mcpServer/elicitation/request","params":{}}`, `{"id":"control","result":{"action":"cancel"}}`, Permission},
		{"command approval", `{"id":10,"method":"item/commandExecution/requestApproval","params":{}}`, `{"id":10,"result":{"decision":"cancel"}}`, Permission},
		{"permissions", `{"id":10,"method":"item/permissions/requestApproval","params":{}}`, `{"id":10,"result":{"permissions":{},"scope":"turn"}}`, Permission},
		{"ambient tool", `{"id":4,"method":"item/tool/call","params":{"tool":"ambient"}}`, `{"id":4,"result":{"contentItems":[],"success":false}}`, Permission},
		{"token refresh", `{"id":11,"method":"account/chatgptAuthTokens/refresh","params":{}}`, `{"id":11,"error":{"code":-32601,"message":"external authentication is unavailable"}}`, Authentication},
		{"unknown control", `{"id":12,"method":"future/request","params":{}}`, `{"id":12,"error":{"code":-32601,"message":"server request is unsupported"}}`, Permission},
		{"disconnected", "", "", Failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := append(handshake(t, dir),
				exchange{inventoryRequest, []string{availableTool}},
				exchange{callRequest, []string{tc.reply}},
			)
			if tc.control != "" {
				script = append(script, exchange{Expect: tc.control})
			}
			trace := setupFake(t, dir, script)
			_, err := Call(context.Background(), dir, "fixture", "write_note", nil)
			checkError(t, err, tc.kind)
			checkTrace(t, trace)
		})
	}
}

func TestCallUnavailableAndRecursiveTarget(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv(activeEnvKey, "")
	_, err := Call(context.Background(), ".", "fixture", "write_note", nil)
	checkError(t, err, Unavailable)
	_, err = Call(context.Background(), ".", "Devflow", "write_note", nil)
	checkError(t, err, Permission)
	_, err = Call(context.Background(), ".", "alias", "devflow_commit", nil)
	checkError(t, err, Permission)
	t.Setenv(activeEnvKey, "1")
	_, err = Call(context.Background(), ".", "fixture", "write_note", nil)
	checkError(t, err, Permission)
}

func TestCallRequiresEphemeralThread(t *testing.T) {
	dir := t.TempDir()
	script := handshake(t, dir)
	script[2].Send = []string{`{"id":2,"result":{"thread":{"id":"persistent","ephemeral":false}}}`}
	trace := setupFake(t, dir, script)
	_, err := Call(context.Background(), dir, "fixture", "write_note", nil)
	checkError(t, err, Permission)
	checkTrace(t, trace)
}

func TestCallCancellationAndOutputLimit(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		dir := t.TempDir()
		script := handshake(t, dir)
		script[0].Send = []string{"stall"}
		setupFake(t, dir, script[:1])
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()
		_, err := Call(ctx, dir, "fixture", "write_note", nil)
		checkError(t, err, Failure)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline error, got %v", err)
		}
	})
	t.Run("output limit", func(t *testing.T) {
		dir := t.TempDir()
		script := handshake(t, dir)
		script[0].Send = []string{"oversized"}
		setupFake(t, dir, script[:1])
		_, err := Call(context.Background(), dir, "fixture", "write_note", nil)
		checkError(t, err, Failure)
		if !strings.Contains(err.Error(), "oversized") {
			t.Fatalf("expected output limit error, got %v", err)
		}
	})
}

func handshake(t *testing.T, dir string) []exchange {
	t.Helper()
	cwd, err := json.Marshal(dir)
	if err != nil {
		t.Fatal(err)
	}
	return []exchange{
		{`{"method":"initialize","id":1,"params":{"clientInfo":{"name":"devflow","version":"0.1.0"},"capabilities":{"explicitGatewayOauth":true}}}`,
			[]string{`{"id":1,"result":{"userAgent":"fixture"}}`}},
		{`{"method":"initialized"}`, nil},
		{`{"id":2,"method":"thread/start","params":{"cwd":` + string(cwd) + `,"ephemeral":true,"approvalPolicy":"never","sandbox":"read-only"}}`,
			[]string{`{"id":2,"result":{"thread":{"id":"scratch","ephemeral":true}}}`}},
	}
}

func setupFake(t *testing.T, dir string, script []exchange) string {
	t.Helper()
	bin := t.TempDir()
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(bin, name)
	if err := os.Link(exe, fake); err != nil {
		binary, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fake, binary, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv(activeEnvKey, "")
	for _, name := range credentialEnv {
		t.Setenv(name, "fake-secret")
	}
	return filepath.Join(dir, "trace")
}

func checkError(t *testing.T, err error, kind Kind) {
	t.Helper()
	var classified *Error
	if !errors.As(err, &classified) || classified.Kind != kind {
		t.Fatalf("error = %v, want kind %s", err, kind)
	}
	if strings.Contains(err.Error(), "fake-secret") {
		t.Fatal("error exposed host diagnostics")
	}
}

func checkTrace(t *testing.T, path string) {
	t.Helper()
	trace, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(trace) != "started\ncomplete\n" {
		t.Fatalf("fake app-server trace = %q", trace)
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "app-server" {
		fakeAppServer()
		return
	}
	os.Exit(m.Run())
}

func fakeAppServer() {
	trace, err := os.OpenFile("trace", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer trace.Close()
	_, _ = fmt.Fprintln(trace, "started")
	want := []string{"app-server", "--listen", "stdio://",
		"-c", `mcp_servers.devflow={enabled=false,command="devflow"}`,
		"-c", "features.hooks=false", "-c", "analytics.enabled=false",
		"-c", "check_for_update_on_startup=false",
		"-c", `model_provider="devflow_codex_host"`,
		"-c", `model_providers.devflow_codex_host={name="Devflow MCP transport",base_url="http://127.0.0.1:0",supports_websockets=false}`}
	if !reflect.DeepEqual(os.Args[1:], want) || os.Getenv(activeEnvKey) != "1" || os.Getenv("CODEX_HOME") == "" {
		_, _ = fmt.Fprintln(trace, "invalid flags or host environment")
		return
	}
	for _, name := range credentialEnv {
		if _, found := os.LookupEnv(name); found {
			_, _ = fmt.Fprintf(trace, "received credential environment key %s\n", name)
			return
		}
	}
	raw, err := os.ReadFile("script")
	var script []exchange
	if err != nil || json.Unmarshal(raw, &script) != nil {
		_, _ = fmt.Fprintln(trace, "invalid fixture script")
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for _, step := range script {
		if !scanner.Scan() || !sameJSON(scanner.Text(), step.Expect) {
			_, _ = fmt.Fprintf(trace, "got %s, expected %s\n", scanner.Text(), step.Expect)
			return
		}
		for _, reply := range step.Send {
			switch reply {
			case "":
				_, _ = fmt.Fprintln(trace, "complete")
				return
			case "stall":
				time.Sleep(10 * time.Second)
				return
			case "oversized":
				_, _ = io.CopyN(os.Stdout, strings.NewReader(strings.Repeat("x", maxFrameBytes)), maxFrameBytes)
				return
			default:
				_, _ = fmt.Fprintln(os.Stdout, reply)
			}
		}
	}
	if scanner.Scan() {
		_, _ = fmt.Fprintf(trace, "unexpected request %s\n", scanner.Text())
		return
	}
	_, _ = fmt.Fprintln(trace, "complete")
}

func sameJSON(a, b string) bool {
	var left, right any
	got, want := json.NewDecoder(strings.NewReader(a)), json.NewDecoder(strings.NewReader(b))
	got.UseNumber()
	want.UseNumber()
	return got.Decode(&left) == nil && want.Decode(&right) == nil && reflect.DeepEqual(left, right)
}
