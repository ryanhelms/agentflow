package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
)

// withStdin runs f with os.Stdin reading input.
func withStdin(t *testing.T, input string, f func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	old := os.Stdin
	os.Stdin = file
	defer func() { os.Stdin = old }()
	f()
}

// The hook commands, from the command line down, keep enforcing when a
// repository points AGENTFLOW_HOME inside itself, and never resolve the home:
// an early error there would let Claude Code run the command, since only
// exit code 2 blocks it.
func TestHookCommandsEnforceWithHomeInsideRepository(t *testing.T) {
	t.Cleanup(config.OverrideAccountHome(t.TempDir()))
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("AGENTFLOW_NO_UPDATE", "1")
	t.Setenv("AGENTFLOW_SESSION_REPO", "")
	home := filepath.Join(dir, "personal")
	t.Setenv("AGENTFLOW_HOME", home)
	if out, err := exec.Command("git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yml"), []byte("autonomy: {local: auto, remote: auto}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(".agentflow", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".agentflow/config.yml", []byte("commit: {types: [docs]}\npolicy: {protected_branches: [production]}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var code int
	withStdin(t, `{"tool_input":{"command":"git push origin production"}}`, func() {
		code = Main([]string{"hook", "pre-bash", "--client", "claude"})
	})
	if code != 2 {
		t.Errorf("hook pre-bash: exit %d, want 2", code)
	}
	withStdin(t, "refs/heads/production 1111111111111111111111111111111111111111 refs/heads/production 0000000000000000000000000000000000000000\n", func() {
		code = Main([]string{"hook", "pre-push"})
	})
	if code != 1 {
		t.Errorf("hook pre-push: exit %d, want 1", code)
	}
	if err := os.WriteFile("MSG", []byte("feat: add quota\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code = Main([]string{"hook", "commit-msg", "MSG"}); code != 1 {
		t.Errorf("hook commit-msg: exit %d, want 1", code)
	}
	for _, n := range config.HomeNotices() {
		if strings.Contains(n, home) {
			t.Errorf("a hook resolved the agentflow home: %s", n)
		}
	}
}
