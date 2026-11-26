package cli

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nscaledev/agentflow/internal/mcpserver"
	"github.com/nscaledev/agentflow/internal/provider/native"
)

// mcpToCLI maps every statically registered MCP tool to the CLI command that
// runs the same internal/app operation. A new static tool must add a CLI
// command and a row here.
var mcpToCLI = map[string]string{
	"agentflow_ui_open":               "ui",
	"agentflow_config":                "config",
	"agentflow_config_apply":          "config publish",
	"agentflow_policy_explain":        "policy explain",
	"agentflow_approval_status":       "policy status",
	"agentflow_approval_approve":      "policy approve",
	"agentflow_commit_prepare":        "commit",
	"agentflow_pr_prepare":            "pr",
	"agentflow_push":                  "push",
	"agentflow_push_prepare":          "push",
	"agentflow_pr_mark_ready":         "pr mark-ready",
	"agentflow_pr_mark_ready_prepare": "pr mark-ready",
	"agentflow_context":               "context",
	"agentflow_my_work":               "work",
	"agentflow_start":                 "start",
	"agentflow_finish":                "finish",
	"agentflow_sweep":                 "sweep",
	"agentflow_worktree_go_work":      "worktree go-work",
	"agentflow_ac_checklist":          "ac",
	"agentflow_ac_update":             "ac done",
	"agentflow_scope_check":           "scope",
	"agentflow_ticket_create":         "task create",
	"agentflow_jira_remote_link":      "task link",
	"agentflow_notify":                "notify",
	"agentflow_transition":            "transition",
	"agentflow_comment":               "comment",
	"agentflow_commit_plan":           "commit",
	"agentflow_commit_context":        "commit",
	"agentflow_commit_validate":       "lint-msg",
	"agentflow_commit_preview":        "preview",
	"agentflow_commit":                "commit",
	"agentflow_pr_ready":              "pr",
	"agentflow_pr_open":               "pr",
	"agentflow_review_threads":        "review",
	"agentflow_review_reply":          "review reply",
	"agentflow_ship_status":           "status",
	"agentflow_standup":               "standup",
	"agentflow_eod_post":              "eod post",
	"agentflow_docs_get":              "docs get",
	"agentflow_docs_search":           "docs search",
	"agentflow_docs_publish":          "docs publish",
	"agentflow_adr_draft":             "adr new",
	"agentflow_collisions":            "collisions",
	"agentflow_release_plan":          "release plan",
	"agentflow_release_preview":       "release preview",
	"agentflow_setup_providers":       "setup providers",
	"agentflow_setup_plan":            "setup",
	"agentflow_setup_apply":           "setup apply",
}

// definitionTool reports whether a tool was generated at runtime from a
// provider definition rather than registered statically. Such tools get their
// CLI command from the same definition and are checked separately, not
// against mcpToCLI. None exist yet.
func definitionTool(string) bool { return false }

func registeredTools(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	ctx := context.Background()
	s, err := mcpserver.New(ctx, dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = s.MCP().Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

func TestEveryMCPToolHasCLICommand(t *testing.T) {
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	registered := map[string]bool{}
	for _, name := range registeredTools(t) {
		registered[name] = true
		if _, ok := mcpToCLI[name]; !ok && !definitionTool(name) {
			t.Errorf("MCP tool %s has no CLI command: add one in internal/cli and a row in mcpToCLI", name)
		}
	}
	for name := range mcpToCLI {
		if !registered[name] {
			t.Errorf("mcpToCLI lists %s, which is not a registered MCP tool", name)
		}
	}

	// Each command must be in the usage text and known to the dispatcher.
	// Run outside a git repo so nothing past flag parsing can act.
	t.Chdir(t.TempDir())
	for tool, command := range mcpToCLI {
		if !strings.Contains(usage, "\n  "+command) {
			t.Errorf("%s: usage text does not list %q", tool, command)
		}
		words := strings.Fields(command)
		err := run(context.Background(), words[0], append(words[1:], "--help"))
		if err != nil && !errors.Is(err, flag.ErrHelp) && strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%s: agentflow %s is not a command: %v", tool, command, err)
		}
	}
}

func TestParityCommandsRunLocally(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("AGENTFLOW_HOME", filepath.Join(dir, "home"))
	t.Setenv("AGENTFLOW_NO_UPDATE", "1")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("GH_TOKEN", "fixture")
	t.Setenv("JIRA_USER", "dev@example.invalid")
	t.Setenv("JIRA_TOKEN", "fixture")
	posts := 0
	oldTransport := http.DefaultTransport
	http.DefaultTransport = taskTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && r.URL.String() == "https://fixture.invalid/rest/api/3/issue/PLAT-7/comment" {
			posts++
			return httptest.NewRecorder().Result(), nil
		}
		return nil, errors.New("offline fixture")
	})
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "dev@example.com"},
		{"config", "user.name", "Dev"},
		{"commit", "-q", "--allow-empty", "-m", "chore: init"},
		{"switch", "-q", "-c", "PLAT-7-quota"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.Mkdir(".agentflow", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".agentflow/config.yml", []byte("jira: {site: https://fixture.invalid}\ntask_management: {provider: jira}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("todo.go", []byte("package x\n// TODO: unrelated cleanup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture := func(args ...string) (string, int) {
		t.Helper()
		f, err := os.CreateTemp(t.TempDir(), "out")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		prev := os.Stdout
		os.Stdout = f
		code := Main(args)
		os.Stdout = prev
		b, _ := os.ReadFile(f.Name())
		return string(b), code
	}
	if out, code := capture("scope", "--json"); code != 0 || !strings.Contains(out, "unrelated cleanup") {
		t.Fatalf("scope = %d %q", code, out)
	}
	if out, code := capture("ac", "--json"); code != 0 || !strings.Contains(out, `"key": "PLAT-7"`) {
		t.Fatalf("ac = %d %q", code, out)
	}
	if _, code := capture("ac", "done", "missing item"); code == 0 {
		t.Fatal("ac done on an empty checklist succeeded")
	}
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	stdin := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = stdin }()
	if out, code := capture("comment", "--", "PLAT-7", "--yes"); code == 0 || posts != 0 || !strings.Contains(out, `"comment": "--yes"`) {
		t.Fatalf("literal comment body: exit %d, posts %d, output %q", code, posts, out)
	}
	if out, code := capture("comment", "PLAT-7", "fixture note", "--yes"); code != 0 || posts != 1 || !strings.Contains(out, `"commented": true`) {
		t.Fatalf("approved comment: exit %d, posts %d, output %q", code, posts, out)
	}
	for _, bad := range [][]string{
		{"eod", "post", "PLAT-7"},
		{"eod"},
		{"comment"},
		{"review", "reply", "only-thread"},
		{"collisions", "A", "B"},
	} {
		if _, code := capture(bad...); code == 0 {
			t.Errorf("agentflow %v succeeded, want a usage error", bad)
		}
	}
}

func TestDesktopNativeClientRoutesSupportedCommands(t *testing.T) {
	for command, provider := range map[string]string{
		"comment": "asana", "start": "asana", "transition": "asana", "task create": "asana", "notify": "slack",
	} {
		writer, err := nativeWriter("claude-desktop", command)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(writer.Close)
		// A recognized provider rejects this malformed call before enrollment.
		// A writer without that provider returns ErrUnavailable instead.
		call := native.Call{Provider: provider, Server: provider, Tool: "unsupported"}
		if _, err := writer.Prepare(t.Context(), call); !errors.Is(err, native.ErrDenied) {
			t.Errorf("%s did not select the writer for %s: %v", command, provider, err)
		}
	}
	for _, command := range []string{"task", "task list", "eod", "ac", "commit", "docs"} {
		if _, err := nativeWriter("claude-desktop", command); err == nil {
			t.Fatalf("enabled Desktop writer for %s", command)
		}
	}
}
