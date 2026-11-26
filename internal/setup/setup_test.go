package setup

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/adapters"
	"github.com/nscaledev/agentflow/internal/gitx"
	"github.com/nscaledev/agentflow/internal/version"
)

func TestInitAndInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	_ = os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Project rules\n\nBe nice.\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".vscode"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".vscode", "mcp.json"), []byte(`{"servers":{"other":{"command":"x"}}}`), 0o644)

	res, err := Init(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) < 10 {
		t.Fatalf("written = %v", res.Written)
	}
	st, err := os.Stat(filepath.Join(dir, ".agentflow", "bin", "agentflow"))
	if err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("launcher not executable: %v", err)
	}
	agents, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if !strings.Contains(string(agents), "Be nice.") || strings.Count(string(agents), blockStart) != 1 {
		t.Fatalf("AGENTS.md = %s", agents)
	}
	// running init again must not duplicate the block
	if _, err := Init(context.Background(), dir, false); err != nil {
		t.Fatal(err)
	}
	agents, _ = os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if strings.Count(string(agents), blockStart) != 1 {
		t.Fatal("block duplicated")
	}
	hooks, _ := exec.Command("git", "-C", dir, "config", "core.hooksPath").Output()
	if strings.TrimSpace(string(hooks)) != ".githooks" {
		t.Errorf("hooksPath = %q", hooks)
	}

	if _, err := Install(dir, []string{"vscode", "codex", "claude"}); err != nil {
		t.Fatal(err)
	}
	var vs map[string]map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, ".vscode", "mcp.json"))
	_ = json.Unmarshal(b, &vs)
	if vs["servers"]["other"] == nil || vs["servers"]["agentflow"] == nil {
		t.Errorf("vscode merge lost keys: %s", b)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".codex", "prompts", "agentflow-commit.md")); err != nil {
		t.Error("codex prompts not installed")
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:nscaledev/demo.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	return dir
}

func TestRemoveUndoesInit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	dir := gitRepo(t)
	_ = os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Project rules\n\nBe nice.\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".vscode"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".vscode", "mcp.json"), []byte(`{"servers":{"other":{"command":"x"}}}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "release-please-config.json"), []byte("{}\n"), 0o644)
	if _, err := Init(ctx, dir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, []string{"codex", "claude"}); err != nil {
		t.Fatal(err)
	}
	// a team edit to a template file survives without --force
	_ = os.WriteFile(filepath.Join(dir, "docs", "confluence.yml"), []byte("space: OPS\n"), 0o644)

	dry, err := Remove(ctx, dir, RemoveOptions{DryRun: true})
	if err != nil || len(dry.Removed) == 0 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".agentflow")); err != nil {
		t.Fatal("dry run deleted files")
	}

	res, err := Remove(ctx, dir, RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "release-please-config.json")); err != nil || string(b) != "{}\n" {
		t.Fatal("pre-existing release configuration changed")
	}
	for _, gone := range []string{".agentflow", ".github", ".githooks", ".claude", ".codex", ".cursor", ".gemini", ".grok", ".jira"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s still exists (removed=%v)", gone, res.Removed)
		}
	}
	agents, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if strings.Contains(string(agents), blockStart) || !strings.Contains(string(agents), "Be nice.") {
		t.Errorf("AGENTS.md = %q", agents)
	}
	vs, _ := os.ReadFile(filepath.Join(dir, ".vscode", "mcp.json"))
	if strings.Contains(string(vs), "agentflow") || !strings.Contains(string(vs), "other") {
		t.Errorf(".vscode/mcp.json = %s", vs)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "docs", "confluence.yml")); string(b) != "space: OPS\n" {
		t.Error("changed template file was deleted without --force")
	}
	hp, _ := exec.Command("git", "-C", dir, "config", "core.hooksPath").Output()
	if len(strings.TrimSpace(string(hp))) != 0 {
		t.Errorf("hooksPath still %q", hp)
	}
	repo, _ := gitx.Open(ctx, dir)
	if got := AutoInitDecision(ctx, repo, AutoInit, []string{"nscaledev"}); got != AutoOff {
		t.Errorf("remove must opt the clone out of auto init, got %q", got)
	}
	// an explicit init opts back in
	if _, err := Init(ctx, dir, false); err != nil {
		t.Fatal(err)
	}
	if got := AutoInitDecision(ctx, repo, AutoInit, []string{"nscaledev"}); got != AutoInit {
		t.Errorf("init must clear the opt-out, got %q", got)
	}
}

func TestAutoInitDecision(t *testing.T) {
	ctx := context.Background()
	repo, err := gitx.Open(ctx, gitRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		mode string
		orgs []string
		want string
	}{
		{AutoInit, []string{"nscaledev"}, AutoInit},
		{AutoAsk, []string{"NscaleDev"}, AutoAsk},
		{AutoOff, []string{"nscaledev"}, AutoOff},
		{AutoInit, []string{"someone-else"}, AutoOff},
	}
	for _, c := range cases {
		if got := AutoInitDecision(ctx, repo, c.mode, c.orgs); got != c.want {
			t.Errorf("%s %v: got %q want %q", c.mode, c.orgs, got, c.want)
		}
	}
}

// The Claude Code plugin ships a copy of the project launcher; only the header differs.
func TestPluginLauncherMatchesTemplate(t *testing.T) {
	body := func(b []byte) string {
		_, after, _ := strings.Cut(string(b), "\nset -eu\n")
		return after
	}
	plugin, err := adapters.FS.ReadFile("claude-code/bin/agentflow")
	if err != nil {
		t.Fatal(err)
	}
	if body(plugin) != body(mustTemplate(".agentflow/bin/agentflow")) {
		t.Error("adapters/claude-code/bin/agentflow drifted from templates/repo/.agentflow/bin/agentflow")
	}
}

func TestPinWorkflow(t *testing.T) {
	sha := strings.Repeat("ab12", 10)
	src := []byte("    uses: ryanhelms/agentflow/.github/workflows/jira-key.yml@v0\n    uses: actions/checkout@v4\n")
	got := string(PinWorkflow(src, sha, "0.4.3"))
	if !strings.Contains(got, "jira-key.yml@"+sha+" # v0.4.3\n") || !strings.Contains(got, "actions/checkout@v4") {
		t.Errorf("pinned = %q", got)
	}
	for _, c := range []struct{ sha, ver string }{{"none", "0.4.3"}, {sha, "dev"}, {"abc1234", "0.4.3"}} {
		if string(PinWorkflow(src, c.sha, c.ver)) != string(src) {
			t.Errorf("%q %q must not pin", c.sha, c.ver)
		}
	}
}

func TestInitPinsCallerWorkflow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldC, oldV := version.Commit, version.Version
	version.Commit, version.Version = strings.Repeat("0123456789", 4), "0.4.3"
	t.Cleanup(func() { version.Commit, version.Version = oldC, oldV })
	dir := gitRepo(t)
	if _, err := Init(context.Background(), dir, false); err != nil {
		t.Fatal(err)
	}
	wf, _ := os.ReadFile(filepath.Join(dir, ".github", "workflows", "agentflow.yml"))
	if strings.Contains(string(wf), "@v0\n") || strings.Count(string(wf), "@"+version.Commit+" # v0.4.3") < 9 {
		t.Errorf("caller workflow not pinned:\n%s", wf)
	}
	if _, err := os.Stat(filepath.Join(dir, ".github", "dependabot.yml")); err == nil {
		t.Error("dependabot.yml must not be written: agentflow pin replaces it")
	}

	// A newer release re-pins every reference and reports where it came from.
	if got := PinBehind(dir); got != "" {
		t.Errorf("PinBehind at the same release = %q", got)
	}
	version.Commit, version.Version = strings.Repeat("abcdef1234", 4), "0.5.0"
	if got := PinBehind(dir); got != "v0.4.3" {
		t.Errorf("PinBehind = %q, want v0.4.3", got)
	}
	res, err := Pin(dir, true)
	if err != nil || !res.Changed || res.From != "v0.4.3" || res.To != "v0.5.0" {
		t.Fatalf("Pin --check = %+v, %v", res, err)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, ".github", "workflows", "agentflow.yml")); string(again) != string(wf) {
		t.Error("Pin with check wrote the file")
	}
	if _, err := Pin(dir, false); err != nil {
		t.Fatal(err)
	}
	wf, _ = os.ReadFile(filepath.Join(dir, ".github", "workflows", "agentflow.yml"))
	if strings.Contains(string(wf), "0123456789") || strings.Count(string(wf), "@"+version.Commit+" # v0.5.0") < 9 {
		t.Errorf("caller workflow not re-pinned:\n%s", wf)
	}
	if PinBehind(dir) != "" {
		t.Error("PinBehind after pin")
	}
}

func TestRepinWorkflow(t *testing.T) {
	sha := strings.Repeat("cd34", 10)
	src := []byte("uses: ryanhelms/agentflow/a.yml@v0\nuses: ryanhelms/agentflow/b.yml@" + strings.Repeat("1", 40) + " # v0.5.1\nuses: ryanhelms/agentflow/c.yml@main\n")
	got := string(RepinWorkflow(src, sha, "v0.6.0"))
	if strings.Count(got, "@"+sha+" # v0.6.0\n") != 3 {
		t.Errorf("repinned = %q", got)
	}
	if v := PinnedVersion(src); v != "v0.5.1" {
		t.Errorf("PinnedVersion = %q", v)
	}
	for _, c := range []struct {
		a, b string
		less bool
	}{{"v0.5.1", "v0.5.2", true}, {"v0.10.0", "v0.9.9", false}, {"v1.0.0", "v1.0.0", false}, {"v0.9.0", "v1.0.0", true}} {
		if VersionLess(c.a, c.b) != c.less {
			t.Errorf("VersionLess(%s, %s) != %v", c.a, c.b, c.less)
		}
	}
}

func TestRemovePreservesProviderRecords(t *testing.T) {
	dir := gitRepo(t)
	if _, err := Init(t.Context(), dir, false); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, ".agentflow", "tasks", "task.md")
	if err := os.MkdirAll(filepath.Dir(record), 0o755); err != nil {
		t.Fatal(err)
	}
	const content = "# An authored task"
	if err := os.WriteFile(record, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(t.Context(), dir, RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(record); err != nil || string(data) != content {
		t.Fatalf("task record changed: %q, %v", data, err)
	}
	if exists(dir, ".agentflow/config.yml") || exists(dir, ".agentflow/bin/agentflow") {
		t.Fatal("setup files were left behind")
	}
}
