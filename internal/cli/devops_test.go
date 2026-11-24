package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/setup"
)

func TestDevOpsFixtureCommands(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) string {
		t.Helper()
		data, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "-q", "-b", "main")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "chore: create fixture", "--allow-empty")
	sha := git("rev-parse", "HEAD")
	if err := os.Mkdir(".agentflow", 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(".agentflow", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const config = `code_hosting: {provider: none}
task_management: {provider: none}
knowledge_base: {provider: none}
devops:
  checks: {provider: none}
  deployments: {provider: fixture, settings: {path: .agentflow/deployments.json}}
  releases: {provider: fixture, settings: {path: .agentflow/releases.json}}
secops: {provider: none}
notifications: {provider: none}
`
	write("config.yml", config)
	write("deployments.json", fmt.Sprintf(`[
  {"environment":"production","revision":"unrelated","state":"success"},
  {"environment":"production","revision":%q,"state":"failure","url":"https://example.test/deployment"},
  {"environment":"production","revision":%q,"state":"success"}
]`, sha, sha))
	write("releases.json", fmt.Sprintf(`[{"tag":"v1.2.3","revision":%q,"title":"Fixture release"}]`, sha))

	capture := func(out any, cmd string, args ...string) {
		t.Helper()
		f, err := os.CreateTemp(t.TempDir(), "stdout")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		stdout := os.Stdout
		os.Stdout = f
		defer func() { os.Stdout = stdout }()
		if err := run(t.Context(), cmd, args); err != nil {
			t.Fatalf("agentflow %s %v: %v", cmd, args, err)
		}
		data, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("invalid output %q: %v", data, err)
		}
	}
	var plan setup.ProviderSetupPlan
	capture(&plan, "setup", "verify", "--json")
	if !plan.Ready || len(plan.Requirements) != 2 {
		t.Fatalf("fixture readiness = %+v", plan)
	}
	var status struct {
		Deployments map[string]provider.Deployment `json:"deployments"`
	}
	capture(&status, "status")
	deployment := status.Deployments["production"]
	if len(status.Deployments) != 1 || deployment.Revision != sha || deployment.State != "failure" || deployment.URL == "" {
		t.Fatalf("fixture deployments = %+v", status.Deployments)
	}
	var release provider.Release
	capture(&release, "release", "get", "v1.2.3")
	if release.Tag != "v1.2.3" || release.Revision != sha || release.Title != "Fixture release" {
		t.Fatalf("fixture release = %+v", release)
	}
	if err := run(t.Context(), "release", []string{"get", "missing"}); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("release get missing = %v", err)
	}
	if err := run(t.Context(), "release", []string{"publish", "v1.2.3", "--revision", sha, "--yes"}); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("fixture publication = %v", err)
	}
	write("config.yml", strings.Replace(config, "provider: fixture, settings: {path: .agentflow/releases.json}", "provider: github, settings: {repository: example/releases}", 1))
	var preview struct {
		Repository string           `json:"repository"`
		Release    provider.Release `json:"release"`
	}
	capture(&preview, "release", "publish", "v1.2.4", "--revision", sha, "--title", "Next release", "--notes", "Notes", "--dry-run")
	want := provider.Release{Tag: "v1.2.4", Revision: sha, Title: "Next release", Notes: "Notes"}
	if preview.Repository != "github.com/example/releases" || !reflect.DeepEqual(preview.Release, want) {
		t.Fatalf("publication preview = %+v", preview)
	}
}
