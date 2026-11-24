package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/agentflow/internal/app"
	"github.com/nscaledev/agentflow/internal/mcpserver"
	"github.com/nscaledev/agentflow/internal/policy"
	"github.com/nscaledev/agentflow/internal/provider/native"
	"github.com/zalando/go-keyring"
)

func TestJiraCreateCLIFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("twg fixture uses bash")
	}
	keyring.MockInit()
	fixtures := os.DirFS("testdata/jira-create")
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.CopyFS(bin, fixtures); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(bin, "twg"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for _, tool := range []string{"git", "bash"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	for _, name := range []string{"CI", "GITHUB_ACTIONS", "AGENTFLOW_OPERATION_ID", "JIRA_USER", "JIRA_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(name, "")
	}
	for _, name := range []string{"TWG_USER", "TWG_SITE", "TWG_PROFILE", "TWG_TOKEN"} {
		t.Setenv(name, "foreign-fixture")
	}
	ctx := t.Context()
	if out, err := exec.CommandContext(ctx, "git", "init", "-qb", "main").CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	if err := os.Mkdir(".agentflow", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".agentflow/config.yml", []byte("task_management: {provider: jira, settings: {site: https://fixture.atlassian.net, projects: [ENG]}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = taskTransport(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unexpected HTTP request %s %s", r.Method, r.URL)
	})
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	count := func(want int) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(bin, "writes"))
		if err != nil && !os.IsNotExist(err) || string(raw) != strings.Repeat("create\n", want) {
			t.Fatalf("write trace %q, want %d (%v)", raw, want, err)
		}
	}
	const title = "--fix 'kiosk'"
	const body = "First\n<literal> & $HOME"
	// The missing Codex executable is a pre-dispatch capability absence.
	if code := Main([]string{"--native-client", "codex", "task", "create", "--project", "ENG", "--summary", title, "--description", body, "--yes"}); code != 0 {
		t.Fatal(code)
	}
	count(1)
	t.Setenv("AGENTFLOW_HOME", t.TempDir())

	connect := func() *mcp.ClientSession {
		t.Helper()
		s, err := mcpserver.New(ctx, dir, "codex")
		if err != nil {
			t.Fatal(err)
		}
		st, ct := mcp.NewInMemoryTransports()
		go func() { _ = s.MCP().Run(ctx, st) }()
		session, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		return session
	}
	session := connect()
	args := map[string]any{"project": "ENG", "summary": title, "description": body}
	call := func(token string, wantErr string) string {
		t.Helper()
		args["confirm_token"] = token
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "agentflow_ticket_create", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		text := out.Content[0].(*mcp.TextContent).Text
		if out.IsError != (wantErr != "") || !strings.Contains(text, wantErr) {
			t.Fatalf("got %s, want error %q", text, wantErr)
		}
		return text
	}
	preview := func() string {
		t.Helper()
		var p app.Pending
		if err := json.Unmarshal([]byte(call("", "")), &p); err != nil {
			t.Fatal(err)
		}
		s := p.Summary.(map[string]any)
		if p.Status != "needs_confirmation" || p.ConfirmToken == "" || s["backend"] != "twg" || s["summary"] != title || s["description"] != body || s["type_id"] != "101" ||
			s["project_id"] != "100" || s["login"].(map[string]any)["account_id"] != "fixture-user" || s["labels"].([]any)[0] != "agentflow" {
			t.Fatal(p)
		}
		return p.ConfirmToken
	}
	edit := func(file, old, new string) func() {
		t.Helper()
		path := filepath.Join(bin, file+".json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), old) {
			t.Fatal("missing fixture value", old)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(raw), old, new, 1)), 0600); err != nil {
			t.Fatal(err)
		}
		return func() {
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if out, err := exec.CommandContext(ctx, "git", "switch", "-c", "ENG-7-topic").CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	var linked app.Pending
	if err := json.Unmarshal([]byte(call("", "")), &linked); err != nil {
		t.Fatal(err)
	}
	if fields := linked.Summary.(map[string]any); fields["link_to"] != "ENG-7" || fields["backend"] != nil {
		t.Fatal(fields)
	}
	if out, err := exec.CommandContext(ctx, "git", "switch", "-c", "unlinked").CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	token := preview()
	for field, value := range map[string]string{"summary": "Changed", "description": "Edited", "type": "Bug", "link_to": "ENG-1"} {
		prior, ok := args[field]
		args[field] = value
		call(token, policy.ErrToken.Error())
		if ok {
			args[field] = prior
		} else {
			delete(args, field)
		}
	}
	for _, change := range [][]string{{"profile", "jira-1", "jira-2"}, {"project", `"100"`, `"103"`}, {"project", `"101"`, `"104"`}} {
		restore := edit(change[0], change[1], change[2])
		call(token, policy.ErrToken.Error())
		restore()
	}
	exe := filepath.Join(bin, "twg")
	if err := os.Rename(exe, exe+".absent"); err != nil {
		t.Fatal(err)
	}
	call(token, policy.ErrToken.Error())
	if err := os.Rename(exe+".absent", exe); err != nil {
		t.Fatal(err)
	}
	args["project"] = "OTHER"
	call("", native.ErrDenied.Error())
	args["project"] = "ENG"
	restore := edit("profile", "oauth", "pat")
	call("", native.ErrSignIn.Error())
	restore()
	restore = edit("project", `"subtask":false`, `"subtask":true`)
	call("", native.ErrDenied.Error())
	restore()
	count(1)
	// Block durable receipt creation. A redeemed token alone cannot dispatch.
	ledger := filepath.Join(os.Getenv("AGENTFLOW_HOME"), "task-write-receipts")
	if err := os.WriteFile(ledger, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	call(token, "claim native task append")
	count(1)
	if err := os.Remove(ledger); err != nil {
		t.Fatal(err)
	}
	restoreRevision := edit("profile", `"revision":2`, `"revision":1`)
	token = preview()
	if result := call(token, ""); !strings.Contains(result, "ENG-42") {
		t.Fatal(result)
	}
	count(2)
	session = connect()
	call(token, policy.ErrToken.Error())
	restoreRevision()
	for i, change := range [][]string{
		{"receipt", `"success":true`, `"success":false`},
		{"receipt", `"ENG-42"`, `"OTHER-1"`},
		{"receipt", "https://fixture.atlassian.net", "https://other.example"},
		{"receipt", `"labels":["agentflow"]`, `"labels":["agentflow"],"parentKey":"ENG-1"`},
		{"readback", `"id":"1042"`, `"id":"1010"`},
		{"readback", "fixture-user", "other-user"},
		{"readback", "<literal>", "changed"},
		{"readback", `"agentflow"`, `"other"`},
	} {
		// Use a distinct approval for each independent receipt fixture.
		restoreProfile := edit("profile", `"revision":2`, fmt.Sprintf(`"revision":%d`, 3+i))
		token = preview()
		restore := edit(change[0], change[1], change[2])
		call(token, native.ErrWriteUncertain.Error())
		restore()
		if i == 0 {
			session = connect()
		}
		call(token, policy.ErrToken.Error())
		count(i + 3)
		restoreProfile()
	}
}
