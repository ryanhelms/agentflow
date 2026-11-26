package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitMsg(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	f := filepath.Join(dir, "MSG")
	_ = os.WriteFile(f, []byte("feat(PLAT-1): add x\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n"), 0o644)
	var errBuf bytes.Buffer
	if err := CommitMsg(context.Background(), f, &errBuf); err != nil {
		t.Fatalf("valid message rejected: %v %s", err, errBuf.String())
	}
	if b, _ := os.ReadFile(f); strings.Contains(string(b), "anthropic") {
		t.Error("trailer not stripped")
	}
	_ = os.WriteFile(f, []byte("updated stuff\n"), 0o644)
	var ee *ExitError
	if err := CommitMsg(context.Background(), f, &errBuf); !errors.As(err, &ee) || ee.Code != 1 {
		t.Fatalf("invalid message accepted: %v", err)
	}
}

func TestPreBash(t *testing.T) {
	t.Chdir(t.TempDir())
	cases := map[string]int{
		`{"tool_input":{"command":"git push --force"}}`:                                                                                      2,
		`{"tool_input":{"command":"git commit -m \"updated stuff\""}}`:                                                                       2,
		`{"tool_input":{"command":"git commit -m \"feat(PLAT-1): add quota api\""}}`:                                                         0,
		`{"tool_input":{"command":"git commit -F msg.txt"}}`:                                                                                 0,
		`{"tool_input":{"command":"ls -la"}}`:                                                                                                0,
		`{"tool_input":{"command":"gh pr create --title \"docs(PLAT-1): keep README.md and its status area current everywhere\" --body x"}}`: 2,
		`{"tool_input":{"command":"gh pr create --title \"docs(PLAT-1): keep README.md and its status area current\" --body x"}}`:            0,
		`{"tool_input":{"command":"gh pr edit 6 -t 'updated stuff'"}}`:                                                                       2,
		"{\"tool_input\":{\"command\":\"git commit -m \\\"$(cat <<'EOF'\\nfix stuff\\nEOF\\n)\\\"\"}}":                                       2,
	}
	for in, want := range cases {
		var errBuf bytes.Buffer
		err := PreBash(context.Background(), strings.NewReader(in), &errBuf, false)
		got := 0
		var ee *ExitError
		if errors.As(err, &ee) {
			got = ee.Code
		}
		if got != want {
			t.Errorf("%s: exit %d want %d (%s)", in, got, want, errBuf.String())
		}
	}
}

func TestPreBashConventionOnly(t *testing.T) {
	t.Chdir(t.TempDir())
	cases := map[string]int{
		`{"tool_input":{"command":"gh pr merge 6 --squash --subject \"docs(PLAT-1): keep README.md current (#6)\""}}`: 0,
		`{"tool_input":{"command":"gh pr merge 6 --squash --subject \"keep README current (#6)\""}}`:                  2,
		`{"tool_input":{"command":"gh pr merge 6 --squash"}}`:                                                         0,
		`{"tool_input":{"command":"gh pr create --title \"fix(PLAT-1): x\" --body y"}}`:                               0,
		`{"tool_input":{"command":"git commit -m \"updated stuff\""}}`:                                                2,
	}
	for in, want := range cases {
		var errBuf bytes.Buffer
		got := 0
		var ee *ExitError
		if errors.As(PreBash(context.Background(), strings.NewReader(in), &errBuf, true), &ee) {
			got = ee.Code
		}
		if got != want {
			t.Errorf("%s: exit %d want %d (%s)", in, got, want, errBuf.String())
		}
	}
}

func TestSessionStartAutoInit(t *testing.T) {
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/nscaledev/demo.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	t.Chdir(dir)
	var out bytes.Buffer
	if err := SessionStart(context.Background(), "claude", "", &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".agentflow", "config.yml")); err != nil {
		t.Fatalf("auto init did not run: %s", out.String())
	}
	if !strings.Contains(out.String(), "set this repo up automatically") || !strings.Contains(out.String(), "/agentflow:remove") {
		t.Errorf("context = %s", out.String())
	}

	// after agentflow remove the clone is opted out and the hook stays silent
	if out, err := exec.Command("git", "-C", dir, "config", "agentflow.autoinit", "false").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	_ = os.RemoveAll(filepath.Join(dir, ".agentflow"))
	out.Reset()
	if err := SessionStart(context.Background(), "claude", "", &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("opted-out clone got context: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".agentflow", "config.yml")); err == nil {
		t.Error("auto init ran in an opted-out clone")
	}
}

func TestCoreHooksSurviveInvalidProviderDefinition(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	home := t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	if out, err := exec.Command("git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.Mkdir(".agentflow", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".agentflow/config.yml", []byte("task_management: {provider: asana}\ncommit: {types: [docs]}\npolicy: {protected_branches: [production]}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "providers"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "providers", "slack.yml"), []byte("providers: ["), 0600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := PreBash(t.Context(), strings.NewReader(`{"tool_input":{"command":"git push origin production"}}`), &buf, false); err == nil {
		t.Fatal("push to protected branch accepted")
	}
	for msg, valid := range map[string]bool{"feat(OPS-1): add quota": false, "docs: describe quota": true} {
		if err := os.WriteFile("MSG", []byte(msg), 0600); err != nil {
			t.Fatal(err)
		}
		if err := CommitMsg(t.Context(), "MSG", &buf); (err == nil) != valid {
			t.Errorf("commit %q: %v", msg, err)
		}
	}
}

func TestSessionStartReportsConfiguredProviders(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", "-b", "PLAT-1-disabled").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s, %v", out, err)
	}
	if err := os.Mkdir(".agentflow", 0700); err != nil {
		t.Fatal(err)
	}
	const config = "task_management: {provider: none}\nknowledge_base: {provider: repository}\n"
	if err := os.WriteFile(".agentflow/config.yml", []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := SessionStart(t.Context(), "test", "", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "task_management=none, knowledge_base=repository") || strings.Contains(out.String(), "Jira") {
		t.Fatalf("session context = %s", out.String())
	}
}

// preBashModes are the two modes of the pre-bash hook. The plugin rule holds in
// both.
var preBashModes = map[string]bool{"never list": false, "convention only": true}

// preBashExit runs the pre-bash hook on command and returns its exit code and
// what it printed.
func preBashExit(t *testing.T, command string, conventionOnly bool) (int, string) {
	t.Helper()
	in, err := json.Marshal(map[string]any{"tool_input": map[string]any{"command": command}})
	if err != nil {
		t.Fatal(err)
	}
	var errBuf bytes.Buffer
	var ee *ExitError
	if errors.As(PreBash(context.Background(), bytes.NewReader(in), &errBuf, conventionOnly), &ee) {
		return ee.Code, errBuf.String()
	}
	return 0, errBuf.String()
}

// Installing, removing and approving plugin writes are the person's
// decisions: the hook blocks each of these ways of invoking the binary, in both
// modes, because plugins belong to the user and not to a repository.
func TestPreBashBlocksPluginDecisions(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, command := range []string{
		"agentflow plugin add ./x",
		"agentflow plugin add ./x --yes --json",
		"agentflow plugin remove ci-notify",
		"agentflow plugin approve op_00112233445566778899aabbccddeeff",
		".agentflow/bin/agentflow plugin approve op_1",
		"./agentflow plugin add x",
		`"$HOME/.cache/agentflow/current/agentflow" plugin remove x`,
		"~/.cache/agentflow/0.58.0/agentflow plugin add x",
		"C:/Users/me/AppData/Local/agentflow/0.58.0/agentflow.exe plugin add x",
		`cmd /c "C:\repo\.agentflow\bin\agentflow.cmd plugin add x"`,
		"pwsh -File .agentflow/bin/agentflow.ps1 plugin add x",
		"bash .agentflow/bin/agentflow plugin add x",
		"/home/me/.claude/plugins/cache/nscale/agentflow/0.58.0/bin/launch plugin add x",
		`"${CLAUDE_PLUGIN_ROOT}/bin/launch" plugin add x`,
		"go run ./cmd/agentflow plugin add x",
		"agentflow --native-client codex plugin add x",
		"agentflow plugin 'add' x",
		"script -q /dev/null agentflow plugin add x",
		"script -q /dev/null .agentflow/bin/agentflow plugin approve op_1",
		`script -qc "agentflow plugin add x" /dev/null`,
		`script --command="agentflow plugin remove x" /dev/null`,
		"script -q /dev/null sh -c 'agentflow plugin add x'",
		`sh -c "agentflow plugin add x"`,
		"bash -lc 'cd /tmp && agentflow plugin add x'",
		`zsh -c "echo y | agentflow plugin add x"`,
		`eval "agentflow plugin add x"`,
		`expect -c 'spawn agentflow plugin add x; send "y\r"'`,
		"echo y | agentflow plugin add x",
		"cd /tmp/p && agentflow plugin add .",
		"CLAUDECODE= agentflow plugin add x",
		"unset CLAUDECODE; agentflow plugin add x",
		"env -u CLAUDECODE -u CODEX_THREAD_ID agentflow plugin add x",
		"env -i PATH=/usr/bin script -q /dev/null agentflow plugin add x",
		"setsid nohup agentflow plugin add x &",
		"timeout 30 agentflow plugin add x",
		"sudo agentflow plugin add x",
		"if true; then agentflow plugin add x; fi",
		"(agentflow plugin add x)",
		"out=$(agentflow plugin add x --json)",
		"agentflow plugin list && agentflow plugin add x",
	} {
		for mode, conventionOnly := range preBashModes {
			if code, shown := preBashExit(t, command, conventionOnly); code != 2 || !strings.Contains(shown, "in their own terminal") {
				t.Errorf("%s, %q: exit %d, want it blocked (%s)", mode, command, code, shown)
			}
		}
	}
}

// Commands that read plugins, belong to another program, or only mention the
// words in an argument run as usual.
func TestPreBashAllowsMentionsOfPluginCommands(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, command := range []string{
		"agentflow plugin list",
		"agentflow plugin list --json",
		".agentflow/bin/agentflow plugin --help",
		"agentflow config get plugins",
		`echo "agentflow plugin add PATH"`,
		"echo agentflow plugin remove x",
		`grep -rn "agentflow plugin add" docs`,
		"rg 'agentflow plugin (add|remove|approve)' README.md",
		"git log --grep='agentflow plugin remove'",
		`git commit -m "docs(PLAT-1): say agentflow plugin add needs a terminal"`,
		"gh issue comment 80 --body \"$(cat <<'EOF'\nThe plugin's rule is off.\nRun agentflow plugin add yourself.\nEOF\n)\"",
		"cat > notes.txt <<'EOF'\nagentflow plugin add x\nEOF",
		"cat docs/plugins.md # agentflow plugin add",
		`printf '%s\n' 'sh -c "agentflow plugin add x"'`,
		`gh issue comment 80 --body "run agentflow plugin approve op_1 yourself"`,
		`bash -c 'echo "agentflow plugin add x"'`,
		"bash scripts/check.sh agentflow plugin add",
		"codex plugin add agentflow@nscale",
		"claude plugin remove agentflow",
	} {
		for mode, conventionOnly := range preBashModes {
			if code, shown := preBashExit(t, command, conventionOnly); code != 0 {
				t.Errorf("%s, %q: exit %d, want it allowed (%s)", mode, command, code, shown)
			}
		}
	}
}
