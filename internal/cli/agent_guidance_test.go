package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/mcpserver"
)

func TestInitGuidanceFollowsConfiguredProviders(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	if err := os.Mkdir(".devflow", 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	const selected = "task_management: {provider: github-issues, settings: {repository: org/repo}}\nknowledge_base: {provider: repository}\n"
	const before = "# Human guidance\nKeep this.\n\n<!-- devflow:start -->\nOld Jira guidance\n<!-- devflow:end -->\n\n## More human guidance\nKeep this too.\n"
	write(".devflow/config.yml", selected)
	write("AGENTS.md", before)
	if _, err := captureCLI(t, "init", "--guidance-only"); err != nil {
		t.Fatal(err)
	}
	guide := read("AGENTS.md")
	if strings.Count(guide, "<!-- devflow:start -->") != 1 || strings.Contains(guide, "Old Jira guidance") ||
		!strings.Contains(guide, "devflow setup providers --explain") ||
		!strings.HasPrefix(guide, "# Human guidance\nKeep this.\n\n") ||
		!strings.HasSuffix(guide, "\n\n## More human guidance\nKeep this too.\n") {
		t.Fatalf("AGENTS.md = %s", guide)
	}
	if _, err := captureCLI(t, "init", "--guidance-only"); err != nil || read("AGENTS.md") != guide {
		t.Fatalf("refresh is not idempotent: %v", err)
	}
	if _, err := os.Stat(".github/workflows/devflow.yml"); !os.IsNotExist(err) || read(".devflow/config.yml") != selected {
		t.Fatalf("guidance refresh changed the project setup: %v", err)
	}
	checkSession := func(task, knowledge string) {
		t.Helper()
		s, err := mcpserver.New(t.Context(), dir, "cli")
		if err != nil {
			t.Fatal(err)
		}
		st, ct := mcp.NewInMemoryTransports()
		go func() { _ = s.MCP().Run(t.Context(), st) }()
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		connected, err := client.Connect(t.Context(), ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer connected.Close()
		instructions := connected.InitializeResult().Instructions
		if !strings.Contains(instructions, "task_management="+task+", knowledge_base="+knowledge) || !strings.Contains(instructions, "devflow setup providers --explain") {
			t.Fatalf("MCP instructions = %s", instructions)
		}
	}
	checkSession("github-issues", "repository")
	const ref = "repository/eng/guide"
	preview, err := taskCLI(t, "--summary", "Follow the guide", "--link-to", ref, "--dry-run")
	if err != nil || !strings.Contains(preview, "Related Knowledge") || !strings.Contains(preview, ref) {
		t.Fatalf("task preview = %s, %v", preview, err)
	}
	write(".devflow/config.yml", "task_management: {provider: none}\nknowledge_base: {provider: filesystem}\n")
	checkSession("none", "filesystem")
	if read("AGENTS.md") != guide {
		t.Fatal("changing providers rewrote guidance")
	}
}
