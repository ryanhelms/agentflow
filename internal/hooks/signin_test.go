package hooks

import (
	"strings"
	"testing"
)

// The console's sign-in commands and the session host are never run by an
// agent: the hook blocks them in both modes, through the command parsing
// the merge gate uses too (wrappers, shells, launchers).
func TestPreBashBlocksSignInAndSessionHost(t *testing.T) {
	t.Chdir(t.TempDir())
	blocked := []string{
		"agentflow ui --no-open",
		"agentflow ui --no-open=true .",
		"agentflow ui --foreground --no-open",
		"agentflow ui --serve-child overview '' link 0",
		".agentflow/bin/agentflow ui --serve-child overview '' auto 0",
		`sh -c "agentflow ui --no-open"`,
		"env -u CLAUDECODE agentflow ui --no-open",
		"cd /tmp && agentflow ui --no-open",
		"agentflow ai host --session ais_0123456789abcdef0123456789abcdef",
		"script -q /dev/null agentflow ai host --session ais_0123456789abcdef0123456789abcdef",
	}
	allowed := []string{"agentflow ui", "agentflow ui --status", "agentflow ui --no-open=false", "agentflow ai status", `echo "agentflow ui --no-open"`}
	for mode, conventionOnly := range preBashModes {
		for _, command := range blocked {
			code, out := preBashExit(t, command, conventionOnly)
			if code != 2 || !(strings.Contains(out, "sign-in link") || strings.Contains(out, "ai host")) {
				t.Errorf("%s: %q = exit %d %q, want blocked", mode, command, code, out)
			}
		}
		for _, command := range allowed {
			if code, out := preBashExit(t, command, conventionOnly); code != 0 {
				t.Errorf("%s: %q = exit %d %q, want allowed", mode, command, code, out)
			}
		}
	}
}
