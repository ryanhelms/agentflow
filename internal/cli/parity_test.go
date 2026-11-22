package cli

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nscaledev/devflow/internal/mcpserver"
)

// mcpToCLI maps every statically registered MCP tool to the CLI command that
// runs the same internal/app operation. A new static tool must add a CLI
// command and a row here.
var mcpToCLI = map[string]string{
	"devflow_context":         "context",
	"devflow_my_work":         "work",
	"devflow_start":           "start",
	"devflow_ac_checklist":    "ac",
	"devflow_ac_update":       "ac done",
	"devflow_scope_check":     "scope",
	"devflow_ticket_create":   "task create",
	"devflow_transition":      "transition",
	"devflow_comment":         "comment",
	"devflow_commit_plan":     "commit",
	"devflow_commit_context":  "commit",
	"devflow_commit_validate": "lint-msg",
	"devflow_commit_preview":  "preview",
	"devflow_commit":          "commit",
	"devflow_pr_ready":        "pr",
	"devflow_pr_open":         "pr",
	"devflow_review_threads":  "review",
	"devflow_review_reply":    "review reply",
	"devflow_ship_status":     "status",
	"devflow_standup":         "standup",
	"devflow_eod_post":        "eod post",
	"devflow_docs_search":     "docs search",
	"devflow_docs_publish":    "docs publish",
	"devflow_adr_draft":       "adr new",
	"devflow_collisions":      "collisions",
	"devflow_release_plan":    "release plan",
	"devflow_release_preview": "release preview",
	"devflow_setup_plan":      "setup",
	"devflow_setup_apply":     "setup apply",
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
	t.Setenv("DEVFLOW_HOME", t.TempDir())
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
			t.Errorf("%s: devflow %s is not a command: %v", tool, command, err)
		}
	}
}

func TestParityCommandsRunLocally(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("DEVFLOW_HOME", filepath.Join(dir, "home"))
	t.Setenv("JIRA_USER", "")
	t.Setenv("JIRA_TOKEN", "")
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
	for _, bad := range [][]string{
		{"eod", "post", "PLAT-7"},
		{"eod"},
		{"comment"},
		{"review", "reply", "only-thread"},
		{"collisions", "A", "B"},
	} {
		if _, code := capture(bad...); code == 0 {
			t.Errorf("devflow %v succeeded, want a usage error", bad)
		}
	}
}
