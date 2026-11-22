package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocsPublishAndSearchCLI(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	for _, sub := range []string{".devflow", "records"} {
		if err := os.Mkdir(sub, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(".devflow/config.yml", []byte("devflow: 1\nknowledge_base: {provider: filesystem, settings: {root: records}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "git", "init", "--quiet", "--template=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	output := filepath.Join(t.TempDir(), "cli-output")
	f, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stdout := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = stdout }()
	ref := "filesystem/engineering%2Fsystems/quota"
	if err := run(t.Context(), "docs", []string{"publish", ref, "--title", "Capacity", "--body", "Limit details", "--operation-id", "capacity-1", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), "docs", []string{"search", "capacity", "--space", "engineering/systems"}); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(out), `"status": "applied"`) || !strings.Contains(string(out), "Capacity ["+ref+"]\n  revision ") || !strings.Contains(string(out), "\n  Limit details\n") {
		t.Fatalf("CLI output %q, %v", out, err)
	}
}
