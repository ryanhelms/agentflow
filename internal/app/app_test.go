package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newRepo makes a repo with no origin: main has api/, the branch PLAT-1-x adds to it.
func newRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	t.Setenv("JIRA_USER", "")
	t.Setenv("JIRA_TOKEN", "")
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		_ = os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "dev@nscale.com")
	run("config", "user.name", "Dev")
	run("commit", "-q", "--allow-empty", "-m", "chore(PLAT-1): init")
	run("switch", "-q", "-c", "PLAT-1-quota")
	write("api/quota.go", "package api\n")
	run("add", ".")
	run("commit", "-q", "-m", "feat(PLAT-1): add quota")
	return dir
}

func TestScopeCheckWithoutOrigin(t *testing.T) {
	dir := newRepo(t)
	_ = os.WriteFile(filepath.Join(dir, "web.go"), []byte("package web\n// TODO: wire quota into UI\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "api", "limits.go"), []byte("package api\n"), 0o644)
	a, err := Open(context.Background(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.ScopeCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 1 || d.Files[0] != "web.go" || len(d.TODOs) != 1 {
		t.Fatalf("drift = %+v", d)
	}
}

func TestLintBranchWithoutOrigin(t *testing.T) {
	dir := newRepo(t)
	a, err := Open(context.Background(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	bad, err := a.LintBranch(context.Background())
	if err != nil || len(bad) != 0 {
		t.Fatalf("bad=%v err=%v", bad, err)
	}
	if keys, _ := a.Keys(context.Background(), a.Repo.BaseRef(context.Background())+"..HEAD"); len(keys) != 1 || keys[0] != "PLAT-1" {
		t.Fatalf("keys = %v", keys)
	}
}
