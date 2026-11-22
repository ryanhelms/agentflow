package setup

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const headlessConfig = `devflow: 1
code_hosting: {provider: none}
task_management: {provider: github-issues, settings: {repository: sample/project}}
knowledge_base: {provider: filesystem, settings: {root: records}}
devops:
  checks: {provider: none}
  deployments: {provider: none}
  releases: {provider: none}
secops: {provider: none}
notifications: {provider: none}
`

func setupFile(t *testing.T, root, name, content string, perm os.FileMode) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func planFixture(t *testing.T, dir, client string) *ProviderSetupPlan {
	t.Helper()
	plan, err := PlanProviderSetup(t.Context(), dir, client)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func planContains(t *testing.T, plan *ProviderSetupPlan, want string) {
	t.Helper()
	out, err := json.Marshal(plan)
	if err != nil || !strings.Contains(string(out), want) {
		t.Fatalf("want %q in %s (%v)", want, out, err)
	}
}

func TestHeadlessGitHubProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX gh executable")
	}
	root, bin := t.TempDir(), t.TempDir()
	t.Setenv("PATH", bin) // No desktop CLI or git. The repository is explicit.
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("DEVFLOW_HOME", filepath.Join(t.TempDir(), "unused"))
	setupFile(t, root, ".devflow/config.yml", headlessConfig, 0o644)
	setupFile(t, root, "records/one.md", "# local document", 0o644)
	plan := planFixture(t, root, "")
	if plan.Ready {
		t.Fatal("missing gh reported ready")
	}
	planContains(t, plan, "install gh on PATH")
	planContains(t, plan, "can list directory")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("SETUP_PROBE_LOG", log)
	setupFile(t, bin, "gh", `#!/bin/sh
test "$GH_PROMPT_DISABLED" = 1 || exit 5
printf '%s\n' "$*" >> "$SETUP_PROBE_LOG"
echo secret-probe-output >&2
case "$*" in
  "--version") exit 0 ;;
  "auth status --hostname github.com") exit "${SETUP_AUTH_EXIT:-0}" ;;
  "api --hostname github.com --method GET --silent repos/sample/project/issues?per_page=1") exit "${SETUP_API_EXIT:-0}" ;;
  *) exit 8 ;;
esac
`, 0o755)
	for _, tc := range []struct {
		phase, auth, api, want string
		ready                  bool
	}{
		{"signed out", "1", "0", "gh auth login --hostname github.com", false},
		{"missing read access", "0", "1", "can GET repos/sample/project/issues", false},
		{"readable", "0", "0", "read task_management on github.com/sample/project", true},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			t.Setenv("SETUP_AUTH_EXIT", tc.auth)
			t.Setenv("SETUP_API_EXIT", tc.api)
			plan := planFixture(t, root, "")
			if plan.Ready != tc.ready || plan.Registration != nil {
				t.Fatalf("unexpected plan %+v", plan)
			}
			planContains(t, plan, tc.want)
			b, _ := json.Marshal(plan)
			if strings.Contains(string(b), "secret-probe-output") {
				t.Fatal("probe output leaked")
			}
		})
	}
	if _, err := os.Stat(os.Getenv("DEVFLOW_HOME")); !os.IsNotExist(err) {
		t.Fatal("read-only planning touched user state")
	}
}

func TestRepositoryReadinessUsesSelectedTree(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, b, err)
		}
	}
	git("init", "-q", "-b", "main")
	setupFile(t, dir, "records/one.md", "# example", 0o644)
	git("add", "records")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
	git("switch", "-qc", "feature")
	if err := os.RemoveAll(filepath.Join(dir, "records")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		settings, want string
		ready          bool
	}{
		{"directory: records", "refs/heads/main:records", true},
		{"directory: records, repository: .", "refs/heads/main:records", true},
		{"directory: missing", "provide directory", false},
		{"branch: unseen", "make branch", false},
		{"directory: ../outside", "inside the Git tree", false},
		{"repository: another/destination", "checkout resolver", false},
	} {
		cfg := strings.ReplaceAll(headlessConfig, "github-issues, settings: {repository: sample/project}", "none")
		cfg = strings.Replace(cfg, "filesystem, settings: {root: records}", "repository, settings: {"+tc.settings+"}", 1)
		setupFile(t, dir, ".devflow/config.yml", cfg, 0o644)
		plan := planFixture(t, dir, "")
		if plan.Ready != tc.ready {
			t.Fatalf("%s: %+v", tc.settings, plan)
		}
		planContains(t, plan, tc.want)
	}
	if _, err := os.Stat(filepath.Join(dir, "records")); !os.IsNotExist(err) {
		t.Fatal("verification changed the working tree")
	}
}

func TestClientMergePreservesUnrelatedSettings(t *testing.T) {
	dir := t.TempDir()
	const path = ".vscode/mcp.json"
	const unrelated = `{"servers":{"other":{"command":"elsewhere"}},"large":9007199254740993}`
	setupFile(t, dir, path, unrelated, 0o600)
	for i := 0; i < 2; i++ {
		if err := RegisterProviderClient(dir, "vscode"); err != nil {
			t.Fatal(err)
		}
		r, err := inspectRegistration(dir, "vscode")
		if err != nil || r.State != "configured" {
			t.Fatalf("registration: %+v, %v", r, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, path))
	for _, part := range []string{"9007199254740993", "elsewhere", "mcp --client vscode"} {
		if !strings.Contains(string(data), part) {
			t.Fatalf("lost %q in %s", part, data)
		}
	}
	for _, conflict := range []string{`null`, `{"servers": false}`, `{"servers":{"devflow":{"url":"https://example.invalid"}}}`, "broken"} {
		setupFile(t, dir, path, conflict, 0o600)
		err := RegisterProviderClient(dir, "vscode")
		if err == nil {
			t.Fatalf("accepted %q", conflict)
		}
		if strings.Contains(conflict, "url") && !strings.Contains(err.Error(), "read-only MCP handshake") {
			t.Fatalf("missing remote endpoint requirement: %v", err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, path))
		if string(data) != conflict {
			t.Fatal("overwrote a conflicting client configuration")
		}
	}
}

func TestWikiReadinessChecksInitializedGitRemote(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX Git executable")
	}
	root, bin := t.TempDir(), t.TempDir()
	t.Setenv("PATH", bin)
	cfg := strings.Replace(headlessConfig, "task_management: {provider: github-issues, settings: {repository: sample/project}}", "task_management: {provider: none}", 1)
	cfg = strings.Replace(cfg, "knowledge_base: {provider: filesystem, settings: {root: records}}", "knowledge_base: {provider: github-wiki, settings: {repository: sample/project}}", 1)
	setupFile(t, root, ".devflow/config.yml", cfg, 0o644)
	setupFile(t, bin, "git", `#!/bin/sh
case "$*" in
  "--version") exit 0 ;;
  "-c credential.interactive=never ls-remote --exit-code https://github.com/sample/project.wiki.git HEAD")
    test "$GIT_TERMINAL_PROMPT" = 0 || exit 8
    exit "${SETUP_WIKI_EXIT:-0}" ;;
  *) exit 8 ;;
esac
`, 0o755)
	t.Setenv("SETUP_WIKI_EXIT", "1")
	planContains(t, planFixture(t, root, ""), "enable the GitHub wiki")
	t.Setenv("SETUP_WIKI_EXIT", "0")
	plan := planFixture(t, root, "")
	if !plan.Ready {
		t.Fatalf("initialized wiki reported unready: %+v", plan)
	}
	planContains(t, plan, "can read the wiki default branch")
}

func TestFileTaskReadinessUsesSelectedDirectory(t *testing.T) {
	dir := t.TempDir()
	cfg := strings.Replace(headlessConfig, "task_management: {provider: github-issues, settings: {repository: sample/project}}", "task_management: {provider: filesystem, settings: {root: tasks, scope: engineering}}", 1)
	cfg = strings.Replace(cfg, "knowledge_base: {provider: filesystem, settings: {root: records}}", "knowledge_base: {provider: none}", 1)
	setupFile(t, dir, ".devflow/config.yml", cfg, 0o644)
	setupFile(t, dir, "tasks/one.md", "# fixture", 0o644)
	plan := planFixture(t, dir, "")
	if !plan.Ready {
		t.Fatalf("configured task directory was not ready: %+v", plan)
	}
	planContains(t, plan, "can list directory")
}
