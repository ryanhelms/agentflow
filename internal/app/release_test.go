package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReleaseInitAndPlan(t *testing.T) {
	dir := newRepo(t) // on PLAT-1-quota with api/quota.go committed
	git := func(args ...string) {
		t.Helper()
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
	write("services/api/go.mod", "module x/api\n")
	write("services/api/main.go", "package main\n")
	write("services/api/Dockerfile", "FROM scratch\n")
	write("cli/go.mod", "module x/cli\n")
	write("cli/main.go", "package main\n")
	git("add", ".")
	git("commit", "-q", "-m", "feat(PLAT-1): add api and cli")
	git("tag", "api-v1.2.0")
	git("tag", "cli-v0.3.1")
	git("tag", "api-v1") // floating major tag: must never count as a release
	write("services/api/handler.go", "package main\n")
	git("add", ".")
	git("commit", "-q", "-m", "fix(PLAT-2): handle empty quota")
	git("switch", "-q", "-c", "develop")

	ctx := context.Background()
	a, err := Open(ctx, dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.ReleaseInit(ctx, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != "gitflow" || len(res.Units) != 2 {
		t.Fatalf("init = %+v", res)
	}
	var manifest map[string]string
	b, _ := os.ReadFile(filepath.Join(dir, ".release-please-manifest.json"))
	_ = json.Unmarshal(b, &manifest)
	if manifest["services/api"] != "1.2.0" || manifest["cli"] != "0.3.1" {
		t.Fatalf("manifest should be seeded from tags: %v", manifest)
	}

	plans, err := a.ReleasePlan(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]UnitPlan{}
	for _, p := range plans {
		got[p.Unit.Name] = p
	}
	api, cli := got["api"], got["cli"]
	if api.Bump != "patch" || api.Next != "1.2.1" || api.BranchKind != "develop" || api.Version[:14] != "1.2.1-alpha.1+" {
		t.Errorf("api = %+v", api)
	}
	if cli.Bump != "none" || cli.WillRelease || cli.Next != "0.3.1" {
		t.Errorf("cli changes must not leak from api: %+v", cli)
	}
}
