package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	_ = os.MkdirAll(filepath.Join(dir, ".devflow"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".devflow", "config.yml"), []byte("devflow: 1\nflow_map: flow-map.yml\npolicy:\n  max_autonomy: {local: auto, remote: confirm}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "flow-map.yml"), []byte("defaults:\n  pr.opened: In Review\nprojects:\n  PLAT: {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "quota.go"), []byte("package quota\n"), 0o644)
	return dir
}

func TestToolsOverInMemoryTransport(t *testing.T) {
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	t.Setenv("JIRA_USER", "")
	t.Setenv("JIRA_TOKEN", "")
	t.Setenv("GH_TOKEN", "x") // never used: no GitHub remote in the test repo
	dir := gitRepo(t)
	// developer autonomy auto for local actions
	_ = os.WriteFile(filepath.Join(os.Getenv("DEVFLOW_HOME"), "config.yml"), []byte("autonomy: {local: auto, remote: confirm}\n"), 0o600)

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

	if out := call("devflow_commit_validate", map[string]any{"message": "updated stuff"}); !strings.Contains(out, "PLAT-123") {
		t.Errorf("validate should suggest a fix with the branch key: %s", out)
	}
	if out := call("devflow_commit_plan", nil); !strings.Contains(out, "quota.go") {
		t.Errorf("plan: %s", out)
	}
	if out := call("devflow_commit_context", map[string]any{"paths": []string{"quota.go"}}); !strings.Contains(out, `"key": "PLAT-123"`) {
		t.Errorf("context: %s", out)
	}
	out := call("devflow_commit", map[string]any{"message": "feat(PLAT-123): add quota package\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n"})
	var res map[string]any
	_ = json.Unmarshal([]byte(out), &res)
	if res["status"] != "committed" {
		t.Fatalf("commit: %s", out)
	}
	log, _ := exec.Command("git", "-C", dir, "log", "-1", "--format=%B").Output()
	if strings.Contains(string(log), "anthropic") {
		t.Errorf("AI trailer not stripped: %s", log)
	}
	if out := call("devflow_context", nil); !strings.Contains(out, "devflow · PLAT-123") {
		t.Errorf("context text: %s", out)
	}
}
