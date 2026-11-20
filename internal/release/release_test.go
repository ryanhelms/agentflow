package release

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBumpAndNext(t *testing.T) {
	cases := []struct {
		msgs []string
		bump string
	}{
		{[]string{"chore(PLAT-1): tidy", "docs(PLAT-1): readme"}, BumpNone},
		{[]string{"fix(PLAT-1): handle nil"}, BumpPatch},
		{[]string{"fix(PLAT-1): x", "feat(PLAT-2,PLAT-3): add api"}, BumpMinor},
		{[]string{"feat(PLAT-1)!: drop v1"}, BumpMajor},
		{[]string{"refactor(PLAT-1): move\n\nBREAKING CHANGE: config renamed"}, BumpMajor},
	}
	for _, c := range cases {
		if got := BumpFor(c.msgs); got != c.bump {
			t.Errorf("%v: %s want %s", c.msgs, got, c.bump)
		}
	}
	if v := Next(Version{1, 4, 2}, BumpMajor); v.String() != "2.0.0" {
		t.Error(v)
	}
	if v := Next(Version{0, 4, 2}, BumpMajor); v.String() != "0.5.0" {
		t.Errorf("pre-1.0 breaking should bump minor: %s", v)
	}
	if v := Next(Version{1, 4, 2}, BumpPatch); v.String() != "1.4.3" {
		t.Error(v)
	}
}

func TestPrerelease(t *testing.T) {
	next := Version{1, 5, 0}
	cases := map[string]string{
		BranchKind(GitFlow, "develop", "main"):     "1.5.0-alpha.3+abc1234",
		BranchKind(GitFlow, "release/1.5", "main"): "1.5.0-rc.3+abc1234",
		BranchKind(Trunk, "hotfix/PLAT-9", "main"): "1.5.0-hotfix.3+abc1234",
		BranchKind(Trunk, "PLAT-12-quota", "main"): "1.5.0-pr.3+abc1234",
		BranchKind(Trunk, "main", "main"):          "1.5.0",
		BranchKind(Trunk, "develop", "main"):       "1.5.0-pr.3+abc1234", // develop is just a branch in trunk mode
	}
	for kind, want := range cases {
		if got := Prerelease(next, kind, 3, "abc1234"); got != want {
			t.Errorf("%s: %s want %s", kind, got, want)
		}
	}
}

func write(t *testing.T, root, p, s string) {
	t.Helper()
	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectMonorepo(t *testing.T) {
	root := t.TempDir()
	write(t, root, "services/api/go.mod", "module x/api\n")
	write(t, root, "services/api/main.go", "package main\n")
	write(t, root, "services/api/Dockerfile", "FROM scratch\n")
	write(t, root, "services/api/chart/Chart.yaml", "name: api\nversion: 0.1.0\n")
	write(t, root, "cli/go.mod", "module x/cli\n")
	write(t, root, "cli/cmd/main.go", "package main\n")
	write(t, root, "cli/.goreleaser.yaml", "version: 2\n")
	write(t, root, "libs/quota/go.mod", "module x/quota\n")
	write(t, root, "libs/quota/quota.go", "package quota\n")
	write(t, root, "web/package.json", "{}\n")
	write(t, root, "web/node_modules/x/package.json", "{}\n")
	write(t, root, "charts/platform/Chart.yaml", "name: platform\n")
	write(t, root, "modules/network/versions.tf", "terraform {}\n")

	units := Detect(root, "nscaledev")
	got := map[string]Unit{}
	for _, u := range units {
		got[u.Name] = u
	}
	if len(units) != 6 {
		t.Fatalf("units = %+v", units)
	}
	api := got["api"]
	if api.Path != "services/api" || api.ReleaseType != "go" || api.Chart != "chart" || api.Image != "ghcr.io/nscaledev/api" ||
		!containsStr(api.Builds, Helm) || !containsStr(api.Builds, Image) || containsStr(api.Builds, Library) {
		t.Errorf("api = %+v", api)
	}
	if cli := got["cli"]; !containsStr(cli.Builds, Binary) || cli.ReleaseType != "go" {
		t.Errorf("cli = %+v", cli)
	}
	if q := got["quota"]; q.Publish != "go" || q.Builds[0] != Library {
		t.Errorf("quota = %+v", q)
	}
	if p := got["platform"]; p.ReleaseType != "helm" || p.Path != "charts/platform" {
		t.Errorf("platform = %+v", p)
	}
	if n := got["network"]; n.ReleaseType != "terraform-module" {
		t.Errorf("network = %+v", n)
	}
	cfg := &Config{Strategy: Trunk, Units: units}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.TagPrefix(api) != "api-v" {
		t.Error("monorepo tags must carry the component")
	}
	pc := cfg.PleaseConfig()
	pkg := pc["packages"].(map[string]any)["services/api"].(map[string]any)
	if _, ok := pkg["extra-files"]; !ok {
		t.Errorf("api ships a chart; Chart.yaml must be bumped: %+v", pkg)
	}
	if pkg["component"] != "api" || pkg["include-component-in-tag"] != true {
		t.Errorf("please config = %+v", pkg)
	}
	m := cfg.Manifest(map[string]string{"cli": "1.2.0"})
	if m["cli"] != "1.2.0" || m["services/api"] != "0.0.0" {
		t.Errorf("manifest = %v", m)
	}
}

func TestValidate(t *testing.T) {
	bad := []Config{
		{Strategy: "yolo", Units: []Unit{{Name: "a", Path: ".", ReleaseType: "go"}}},
		{Strategy: Trunk},
		{Strategy: Trunk, Units: []Unit{{Name: "A B", Path: ".", ReleaseType: "go"}}},
		{Strategy: Trunk, Units: []Unit{{Name: "a", Path: "../x", ReleaseType: "go"}}},
		{Strategy: Trunk, Units: []Unit{{Name: "a", Path: ".", ReleaseType: "go", Builds: []string{"rpm"}}}},
	}
	for i, c := range bad {
		if c.Validate() == nil {
			t.Errorf("case %d should fail", i)
		}
	}
}
