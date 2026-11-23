package hooks

import (
	"bytes"
	"context"
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
	t.Setenv("DEVFLOW_HOME", t.TempDir())
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
	if _, err := os.Stat(filepath.Join(dir, ".devflow", "config.yml")); err != nil {
		t.Fatalf("auto init did not run: %s", out.String())
	}
	if !strings.Contains(out.String(), "set this repo up automatically") || !strings.Contains(out.String(), "/devflow:remove") {
		t.Errorf("context = %s", out.String())
	}

	// after devflow remove the clone is opted out and the hook stays silent
	if out, err := exec.Command("git", "-C", dir, "config", "devflow.autoinit", "false").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	_ = os.RemoveAll(filepath.Join(dir, ".devflow"))
	out.Reset()
	if err := SessionStart(context.Background(), "claude", "", &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("opted-out clone got context: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".devflow", "config.yml")); err == nil {
		t.Error("auto init ran in an opted-out clone")
	}
}

func TestCoreHooksSurviveInvalidProviderDefinition(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	home := t.TempDir()
	t.Setenv("DEVFLOW_HOME", home)
	if out, err := exec.Command("git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.Mkdir(".devflow", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".devflow/config.yml", []byte("task_management: {provider: asana}\ncommit: {types: [docs]}\npolicy: {protected_branches: [production]}"), 0600); err != nil {
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
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", "-b", "PLAT-1-disabled").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s, %v", out, err)
	}
	if err := os.Mkdir(".devflow", 0700); err != nil {
		t.Fatal(err)
	}
	const config = "task_management: {provider: none}\nknowledge_base: {provider: repository}\n"
	if err := os.WriteFile(".devflow/config.yml", []byte(config), 0600); err != nil {
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
