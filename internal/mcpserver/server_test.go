package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/provider/native"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "dev@nscale.com")
	run("config", "user.name", "Dev")
	run("commit", "-q", "--allow-empty", "-m", "chore(PLAT-1): init")
	run("switch", "-q", "-c", "PLAT-123-gpu-quota")
	_ = os.MkdirAll(filepath.Join(dir, ".agentflow"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".agentflow", "config.yml"), []byte("agentflow: 1\njira: {site: http://127.0.0.1}\ntask_management: {provider: jira}\nflow_map: flow-map.yml\npolicy:\n  max_autonomy: {local: auto, remote: confirm}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "flow-map.yml"), []byte("defaults:\n  pr.opened: In Review\nprojects:\n  PLAT: {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "quota.go"), []byte("package quota\n"), 0o644)
	return dir
}

// jiraUnavailable keeps the context fixture on its offline, missing-ticket path.
type jiraUnavailable struct{}

func (jiraUnavailable) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func TestToolsOverInMemoryTransport(t *testing.T) {
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	// A local HTTP site bypasses Teamwork Graph CLI discovery. Fixture credentials
	// and an in-memory response keep the keychain and network out of the test.
	t.Setenv("JIRA_USER", "fixture")
	t.Setenv("JIRA_TOKEN", "fixture")
	previous := http.DefaultTransport
	http.DefaultTransport = jiraUnavailable{}
	t.Cleanup(func() { http.DefaultTransport = previous })
	t.Setenv("GH_TOKEN", "x") // never used: no GitHub remote in the test repo
	dir := gitRepo(t)
	// developer autonomy auto for local actions
	_ = os.WriteFile(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "config.yml"), []byte("autonomy: {local: auto, remote: confirm}\n"), 0o600)

	ctx := context.Background()
	s, err := New(ctx, dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = s.MCP().Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) < 20 {
		t.Fatalf("only %d tools", len(tools.Tools))
	}

	call := func(name string, args map[string]any) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}

	// The console resolves the worktree on the server. Invalid destinations must
	// fail before any process starts or any browser opens.
	registered := false
	for _, tool := range tools.Tools {
		if tool.Name == "agentflow_ui_open" {
			registered = true
			if tool.Annotations == nil || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
				t.Error("expected a local console tool")
			}
		}
	}
	if !registered {
		t.Fatal("missing agentflow_ui_open")
	}
	for _, args := range []map[string]any{
		{"screen": "https://example.invalid"},
		{"screen": "handoff", "role": "task_management"},
		{"role": "other"},
	} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agentflow_ui_open", Arguments: args})
		if err != nil || !res.IsError {
			t.Fatalf("invalid console destination %v: %v, %v", args, res, err)
		}
	}

	// Provider operations and explain must agree until this server restarts.
	defDir := filepath.Join(os.Getenv("AGENTFLOW_HOME"), "providers")
	if err := os.MkdirAll(defDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defDir, "later.yml"), []byte("version: 1\nproviders: {jira: {task_management: {settings: {site: https://later.example}}}}"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := call("agentflow_setup_providers", map[string]any{"explain": true}); !strings.Contains(out, s.app.Cfg.Jira.Site) || strings.Contains(out, "later.example") || strings.Contains(out, "later.yml") {
		t.Fatalf("explain differs from active snapshot: %s", out)
	}

	if out := call("agentflow_commit_validate", map[string]any{"message": "updated stuff"}); !strings.Contains(out, "PLAT-123") {
		t.Errorf("validate should suggest a fix with the branch key: %s", out)
	}
	if out := call("agentflow_commit_plan", nil); !strings.Contains(out, "quota.go") {
		t.Errorf("plan: %s", out)
	}
	if out := call("agentflow_commit_context", map[string]any{"paths": []string{"quota.go"}}); !strings.Contains(out, `"key": "PLAT-123"`) {
		t.Errorf("context: %s", out)
	}
	out := call("agentflow_commit", map[string]any{"message": "feat(PLAT-123): add quota package\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n"})
	var res map[string]any
	_ = json.Unmarshal([]byte(out), &res)
	if res["status"] != "committed" {
		t.Fatalf("commit: %s", out)
	}
	log, _ := exec.Command("git", "-C", dir, "log", "-1", "--format=%B").Output()
	if strings.Contains(string(log), "anthropic") {
		t.Errorf("AI trailer not stripped: %s", log)
	}
	if out := call("agentflow_context", nil); !strings.Contains(out, "agentflow · PLAT-123") {
		t.Errorf("context text: %s", out)
	}
}

func TestDesktopWriteEntryPoints(t *testing.T) {
	for _, client := range []string{"claude", "claude-desktop", "codex"} {
		s := &Server{app: &app.App{Client: client}, writer: &native.SessionWriter{}, slackWriter: &native.SessionWriter{}}
		wantNotify := s.slackWriter
		if client == "codex" {
			wantNotify = s.writer
		}
		if s.writerForTool("agentflow_notify") != wantNotify {
			t.Errorf("%s notification writer differs from its configured connection", client)
		}
		for name, desktop := range map[string]bool{
			"agentflow_comment": true, "agentflow_docs_publish": true, "agentflow_jira_remote_link": true,
			"agentflow_start": true, "agentflow_transition": true, "agentflow_commit": false,
			"agentflow_eod_post": false, "agentflow_ac_update": false, "agentflow_ticket_create": true,
		} {
			if got := s.writerForTool(name) != nil; got != (desktop || client == "codex") {
				t.Errorf("%s %s writer available = %t", client, name, got)
			}
		}
	}
}

func TestUIOnlyServerListsOnlyTheConsoleForAnUnconfiguredWorktree(t *testing.T) {
	ctx := t.Context()
	dir := gitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".agentflow", "config.yml"), []byte("agentflow: ["), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := newUIOnly(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = server.MCP().Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "agent-commander", Version: "test"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 1 || list.Tools[0].Name != "agentflow_ui_open" {
		t.Fatalf("launcher-only tool list: %v, %v", list, err)
	}
	invalid, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agentflow_ui_open", Arguments: map[string]any{"screen": "invalid"}})
	if err != nil || !invalid.IsError {
		t.Fatalf("UI-only companion must report a tool error for an invalid destination: %v, %v", invalid, err)
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agentflow_commit", Arguments: map[string]any{}})
	if err == nil && !result.IsError {
		t.Fatal("console-only server accepted a repository tool")
	}
}
