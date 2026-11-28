package acptest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/acp/acptest"
)

// TestInstallLargeScript: a script over 256 KiB installs as a wrapper whose
// every argument stays under Linux's 128 KiB limit on one argument, and
// the wrapper still starts the fake.
func TestInstallLargeScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reads a sidecar file")
	}
	var steps []acptest.Step
	for range 5 {
		steps = append(steps, acptest.Step{Update: acptest.JSON(map[string]any{"text": strings.Repeat("x", 64<<10)})})
	}
	dir := t.TempDir()
	acptest.Install(t, dir, "big", acptest.Script{Version: "9.9.9", Session: &acptest.SessionScript{Steps: steps}})
	body, err := os.ReadFile(filepath.Join(dir, "big"))
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range strings.Fields(string(body)) {
		if len(arg) > 128<<10 {
			t.Fatalf("an argument of %d bytes: exec fails on Linux", len(arg))
		}
	}
	out, err := exec.Command(filepath.Join(dir, "big"), "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "big 9.9.9" {
		t.Fatalf("wrapper: %q %v", out, err)
	}
}
