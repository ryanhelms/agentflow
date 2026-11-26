package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/watch"
)

func TestPrintDaemonHome(t *testing.T) {
	for name, c := range map[string]struct {
		check watch.HomeCheck
		want  string // "" means no line at all
	}{
		"no daemon":      {watch.HomeCheck{}, ""},
		"same home":      {watch.HomeCheck{Checked: true, Detail: "the console daemon watches /home/a"}, "✓ console daemon home        the console daemon watches /home/a\n"},
		"different home": {watch.HomeCheck{Checked: true, Warn: true, Detail: "the console daemon does not watch /home/a"}, "! console daemon home        the console daemon does not watch /home/a\n"},
	} {
		var out bytes.Buffer
		printDaemonHome(&out, c.check)
		if out.String() != c.want {
			t.Errorf("%s: %q, want %q", name, out.String(), c.want)
		}
	}
}

// A daemon that watches another AGENTFLOW_HOME is a warning: doctor still
// lists the line, and the line does not make doctor fail.
func TestDoctorWarnsWhenTheDaemonUsesAnotherHome(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	mine, daemons := t.TempDir(), t.TempDir()
	t.Setenv("AGENTFLOW_HOME", mine)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GH_TOKEN", "fixture")
	if out, err := exec.Command("git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.MkdirAll(".agentflow", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".agentflow/config.yml", []byte("agentflow: 1\nflow_map: flow-map.yml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("flow-map.yml", []byte("defaults:\n  pr.opened: In Review\nprojects: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, held, err := watch.HoldConsoleHome(daemons)
	if err != nil || !held {
		t.Fatalf("hold: %v, %t", err, held)
	}
	t.Cleanup(release)
	old := checkDaemonHome
	checkDaemonHome = func(context.Context) watch.HomeCheck { return watch.CheckHome(mine, true) }
	t.Cleanup(func() { checkDaemonHome = old })

	out, _ := captureCLI(t, "doctor")
	line := lineWith(out, "console daemon home")
	if !strings.HasPrefix(line, "! ") || !strings.Contains(line, "does not watch "+mine) {
		t.Fatalf("doctor output has no warning for another home:\n%s", out)
	}
	// Only a warning: it is not a failed check.
	if strings.Contains(line, "✖") {
		t.Fatalf("the warning is marked as a failed check: %q", line)
	}

	// The daemon holds this home too: a check mark.
	checkDaemonHome = func(context.Context) watch.HomeCheck { return watch.CheckHome(daemons, true) }
	out, _ = captureCLI(t, "doctor")
	if line := lineWith(out, "console daemon home"); !strings.HasPrefix(line, "✓ ") {
		t.Fatalf("doctor output for the daemon's own home:\n%s", out)
	}
	// No daemon, no line.
	checkDaemonHome = func(context.Context) watch.HomeCheck { return watch.CheckHome(mine, false) }
	if out, _ = captureCLI(t, "doctor"); strings.Contains(out, "console daemon home") {
		t.Fatalf("doctor printed the line without a daemon:\n%s", out)
	}
}

func lineWith(out, text string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, text) {
			return l
		}
	}
	return ""
}
