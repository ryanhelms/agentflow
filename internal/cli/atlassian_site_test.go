package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// No Atlassian site is built in, so signing in or out needs one from the
// repository or --site. Neither command reaches the keychain or the network here.
func TestLoginAndLogoutNeedASite(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if out, err := exec.Command("git", "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	for cmd, args := range map[string][]string{"login": {"--email", "dev@example.com", "--token-stdin"}, "logout": nil} {
		if err := run(t.Context(), cmd, args); err == nil || !strings.Contains(err.Error(), "--site") {
			t.Errorf("%s: %v", cmd, err)
		}
	}
}
