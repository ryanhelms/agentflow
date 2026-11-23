package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLegacyLintMessage(t *testing.T) {
	dir := t.TempDir()
	command := exec.Command("git", "init", "--quiet", "-b", "main", dir)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	t.Chdir(dir)
	t.Setenv("DEVFLOW_HOME", t.TempDir())

	if err := os.Mkdir(".devflow", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, config, message string
		exit                  int
	}{
		{"jira scope", "devflow: 1\n", "feat(PLAT-42): add fixture\n", 0},
		{"required jira scope", "task_management: {provider: jira}\n", "feat: add fixture (#57)\n", 1},
		{"unassigned task provider", "devflow: 1\n", "feat: add fixture (#57)\n", 0},
		{"optional jira scope", "devflow: 1\ncommit: {require_key: false}\n", "feat: add fixture (#57)\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(".devflow", "config.yml"), []byte(tc.config), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("message", []byte(tc.message), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := Main([]string{"lint-msg", "message"}); got != tc.exit {
				t.Errorf("exit = %d, want %d", got, tc.exit)
			}
		})
	}
}
