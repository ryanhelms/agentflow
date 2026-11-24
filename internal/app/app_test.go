package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nscaledev/agentflow/internal/config"
)

// newRepo makes a repo with no origin: main has api/, the branch PLAT-1-x adds to it.
func newRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
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

func TestPRReadinessWithoutTaskProvider(t *testing.T) {
	ctx := t.Context()
	dir := newRepo(t)
	if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := "commit: {require_key: true}\n"
	if err := os.WriteFile(filepath.Join(dir, ".agentflow/config.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(ctx, dir, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, a.Repo, "branch", "-m", "add-quota")
	mustGit(t, a.Repo, "add", ".agentflow/config.yml")
	mustGit(t, a.Repo, "commit", "--amend", "--no-edit")
	mustGit(t, a.Repo, "remote", "add", "origin", "https://github.com/example/quota.git")

	ready, err := a.PRReady(ctx, false)
	if err != nil || !ready.Ready || ready.SuggestTitle != "feat(PLAT-1): add quota" || len(ready.Effects) != 0 {
		t.Fatalf("readiness without a task provider: %+v, %v", ready, err)
	}
	for _, check := range ready.Checks {
		if check.Name == "branch has Jira key" || check.Name == "branch has task reference" {
			t.Fatalf("task prerequisite on an unassigned role: %+v", check)
		}
	}
	if issues := a.Validate(ctx, "feat: add quota"); len(issues) == 0 {
		t.Fatal("the explicitly configured commit key requirement was skipped")
	}
}

func TestJiraPRReadinessWithoutBranchKey(t *testing.T) {
	for _, tt := range []struct {
		name  string
		rules string
		ready bool
	}{
		{name: "required by default"},
		{name: "optional", rules: "commit: {require_key: false}\n", ready: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			dir := newRepo(t)
			if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0700); err != nil {
				t.Fatal(err)
			}
			cfg := "task_management: {provider: jira}\n" + tt.rules
			if err := os.WriteFile(filepath.Join(dir, ".agentflow/config.yml"), []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			a, err := Open(ctx, dir, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			mustGit(t, a.Repo, "branch", "-m", "add-quota")
			mustGit(t, a.Repo, "add", ".agentflow/config.yml")
			// Keep a valid Jira key in the commit so commit lint passes in both modes.
			mustGit(t, a.Repo, "commit", "--amend", "--no-edit")
			mustGit(t, a.Repo, "remote", "add", "origin", "https://github.com/example/quota.git")
			a.Gate.Autonomy.Remote = config.Confirm

			r, err := a.PRReady(ctx, false)
			if err != nil {
				t.Fatal(err)
			}
			if r.Ready != tt.ready ||
				!slices.Contains(r.Checks, Check{Name: "branch has Jira key", OK: tt.ready}) ||
				!slices.Contains(r.Checks, Check{Name: "commit messages", OK: true}) ||
				len(r.Effects) != 0 || r.SuggestTitle != "" || r.Draft {
				t.Fatalf("unexpected readiness: %+v", r)
			}

			v, err := a.PROpen(ctx, PROpenInput{Title: "feat: add quota"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if tt.ready {
				confirmation(t, v)
			} else if result, ok := v.(map[string]any); !ok || result["status"] != "not_ready" {
				t.Fatalf("expected not_ready, got %+v", v)
			}
		})
	}
}
