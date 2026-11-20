package setup

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitAndInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	_ = os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Project rules\n\nBe nice.\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".vscode"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".vscode", "mcp.json"), []byte(`{"servers":{"other":{"command":"x"}}}`), 0o644)

	res, err := Init(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) < 10 {
		t.Fatalf("written = %v", res.Written)
	}
	st, err := os.Stat(filepath.Join(dir, ".devflow", "bin", "devflow"))
	if err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("launcher not executable: %v", err)
	}
	agents, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if !strings.Contains(string(agents), "Be nice.") || strings.Count(string(agents), blockStart) != 1 {
		t.Fatalf("AGENTS.md = %s", agents)
	}
	// running init again must not duplicate the block
	if _, err := Init(context.Background(), dir, false); err != nil {
		t.Fatal(err)
	}
	agents, _ = os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if strings.Count(string(agents), blockStart) != 1 {
		t.Fatal("block duplicated")
	}
	hooks, _ := exec.Command("git", "-C", dir, "config", "core.hooksPath").Output()
	if strings.TrimSpace(string(hooks)) != ".githooks" {
		t.Errorf("hooksPath = %q", hooks)
	}

	if _, err := Install(dir, []string{"vscode", "codex", "claude"}); err != nil {
		t.Fatal(err)
	}
	var vs map[string]map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, ".vscode", "mcp.json"))
	_ = json.Unmarshal(b, &vs)
	if vs["servers"]["other"] == nil || vs["servers"]["devflow"] == nil {
		t.Errorf("vscode merge lost keys: %s", b)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".codex", "prompts", "devflow-commit.md")); err != nil {
		t.Error("codex prompts not installed")
	}
}
