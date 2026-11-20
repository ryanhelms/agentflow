package setup

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/adapters"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/version"
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
	st, err := os.Stat(filepath.Join(dir, ".devflow", "bin", "devflow"))
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
	if vs["servers"]["other"] == nil || vs["servers"]["devflow"] == nil {
		t.Errorf("vscode merge lost keys: %s", b)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".codex", "prompts", "devflow-commit.md")); err != nil {
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
	if _, err := os.Stat(filepath.Join(dir, ".devflow")); err != nil {
		t.Fatal("dry run deleted files")
	}

	res, err := Remove(ctx, dir, RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{".devflow", ".github", ".githooks", ".claude", ".codex", ".cursor", ".gemini", ".grok", ".jira", "release-please-config.json"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s still exists (removed=%v)", gone, res.Removed)
		}
	}
	agents, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if strings.Contains(string(agents), blockStart) || !strings.Contains(string(agents), "Be nice.") {
		t.Errorf("AGENTS.md = %q", agents)
	}
	vs, _ := os.ReadFile(filepath.Join(dir, ".vscode", "mcp.json"))
	if strings.Contains(string(vs), "devflow") || !strings.Contains(string(vs), "other") {
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
	plugin, err := adapters.FS.ReadFile("claude-code/bin/devflow")
	if err != nil {
		t.Fatal(err)
	}
	if body(plugin) != body(mustTemplate(".devflow/bin/devflow")) {
		t.Error("adapters/claude-code/bin/devflow drifted from templates/repo/.devflow/bin/devflow")
	}
}

func TestPinWorkflow(t *testing.T) {
	sha := strings.Repeat("ab12", 10)
	src := []byte("    uses: nscaledev/devflow/.github/workflows/jira-key.yml@v0\n    uses: actions/checkout@v4\n")
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
	wf, _ := os.ReadFile(filepath.Join(dir, ".github", "workflows", "devflow.yml"))
	if strings.Contains(string(wf), "@v0\n") || strings.Count(string(wf), "@"+version.Commit+" # v0.4.3") < 9 {
		t.Errorf("caller workflow not pinned:\n%s", wf)
	}
	if _, err := os.Stat(filepath.Join(dir, ".github", "dependabot.yml")); err != nil {
		t.Error("dependabot.yml not written")
	}
}
