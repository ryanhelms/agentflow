package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/worktree"
)

func TestWorktreeCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("AGENTFLOW_ALLOW_PROMOTE", "")
	dir := filepath.Join(home, "app")
	for _, args := range [][]string{{"init", "-q", "-b", "develop", dir}, {"-C", dir, "-c", "user.name=D", "-c", "user.email=d@example.com", "commit", "-q", "--allow-empty", "-m", "chore: init"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	t.Chdir(dir)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	sweepErr := run(t.Context(), "sweep", []string{"--dry-run", "--stale-days", "3"})
	os.Stdout = stdout
	_ = w.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(r)
	if sweepErr != nil || !strings.Contains(out.String(), "No linked worktrees.") {
		t.Fatalf("sweep: %v %q", sweepErr, out.String())
	}
	if err := run(t.Context(), "promote", []string{"--dry-run"}); err == nil || !strings.Contains(err.Error(), "CLAUDECODE") {
		t.Fatalf("promote inside an agent: %v", err)
	}
	if err := run(t.Context(), "finish", nil); err == nil || !strings.Contains(err.Error(), "primary checkout") {
		t.Fatalf("finish in the primary checkout: %v", err)
	}
	if err := run(t.Context(), "worktree", []string{"go-work"}); err == nil || !strings.Contains(err.Error(), "worktree.go_work") {
		t.Fatalf("go-work without config: %v", err)
	}
	if err := run(t.Context(), "worktree", nil); err == nil {
		t.Fatal("worktree without a subcommand should fail")
	}
	var buf bytes.Buffer
	printSweep(&buf, &worktree.SweepResult{Worktrees: []worktree.Outcome{{Repo: "app", Branch: "b", State: worktree.StateStale, Detail: "no commits for 9 days", Path: "/x"}}})
	if !strings.Contains(buf.String(), "REPO") || !strings.Contains(buf.String(), "stale") {
		t.Fatalf("table %q", buf.String())
	}
}
