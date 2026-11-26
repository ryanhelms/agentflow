package ops_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
	"github.com/ryanhelms/agentflow/internal/goldentest"
)

// opsRepo is a git repository named app on branch task-1, opened as the CLI
// opens it. Its personal configuration sets the autonomy of each class, so a
// test does not depend on the defaults.
type opsRepo struct {
	dir  string
	home string
	app  *app.App
}

const confirmEverything = "autonomy: {local: confirm, remote: confirm}\n"

// newOpsRepo makes the repository with repoConfig as .agentflow/config.yml
// and userConfig as the personal configuration.
func newOpsRepo(t *testing.T, repoConfig, userConfig string) *opsRepo {
	t.Helper()
	goldentest.Isolate(t)
	r := &opsRepo{home: t.TempDir(), dir: filepath.Join(t.TempDir(), "app")}
	if err := os.WriteFile(filepath.Join(r.home, "config.yml"), []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_HOME", r.home)
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(r.dir, ".agentflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "dev@example.org"},
		{"config", "user.name", "Dev"},
		{"commit", "-q", "--allow-empty", "-m", "chore: init"},
		{"switch", "-q", "-c", "task-1"},
	} {
		r.git(t, args...)
	}
	r.reopen(t, repoConfig)
	return r
}

// reopen replaces the repository configuration and opens the App again.
func (r *opsRepo) reopen(t *testing.T, repoConfig string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, ".agentflow", "config.yml"), []byte(repoConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(t.Context(), r.dir, "cli")
	if err != nil {
		t.Fatal(err)
	}
	r.app = a
}

func (r *opsRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	return repoGit(t, r.dir, args...)
}

func repoGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// cli runs a registered command line, as the CLI would: the longest
// registered path that prefixes args, a Spec's CLI or a verb. It returns what
// the Invoker published and what the command printed.
func (r *opsRepo) cli(t *testing.T, answer bool, args ...string) (*commandtest.Emitter, *commandtest.Env, error) {
	t.Helper()
	em, inv := recording()
	env := &commandtest.Env{App: r.app, Answer: answer}
	run, rest, ok := command.Default.Dispatch(args)
	if !ok {
		t.Fatalf("no registered command runs %v", args)
	}
	return em, env, run(t.Context(), env, inv, rest)
}

// tool calls the MCP tool of a registered command.
func (r *opsRepo) tool(t *testing.T, name string, in map[string]any) (*commandtest.Emitter, string, bool) {
	t.Helper()
	em, inv := recording()
	out, isErr := invokeTool(t, r.app, name, in, inv)
	return em, out, isErr
}

func (r *opsRepo) auditLines(t *testing.T) []map[string]any {
	t.Helper()
	file, err := os.Open(filepath.Join(r.app.Home, "audit.log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out []map[string]any
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("audit line %q: %v", sc.Text(), err)
		}
		out = append(out, line)
	}
	return out
}

// wantOpTypes fails unless em recorded exactly these event types.
func wantOpTypes(t *testing.T, em *commandtest.Emitter, want ...bus.Type) []bus.Event {
	t.Helper()
	if got := em.Types(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	return em.Events()
}

// wantOpPayload checks the operation, action and class of an event's payload and
// returns the payload.
func wantOpPayload(t *testing.T, ev bus.Event, operation, action, class string) bus.OpPayload {
	t.Helper()
	p, err := commandtest.Payload(ev)
	if err != nil {
		t.Fatal(err)
	}
	if p.Operation != operation || p.Action != action || p.Class != class {
		t.Fatalf("%s payload %+v, want operation %s, action %s, class %s", ev.Type, p, operation, action, class)
	}
	if strings.ContainsAny(p.Subject, "\r\n") {
		t.Errorf("subject holds a newline: %q", p.Subject)
	}
	return p
}

// wantOpConfirmFlow checks the four events of a confirmed operation: asked,
// then resumed with the approval's token and applied, all in one operation.
func wantOpConfirmFlow(t *testing.T, em *commandtest.Emitter, operation, action, class string) []bus.Event {
	t.Helper()
	evs := wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpApplied)
	for i, ev := range evs {
		wantOpPayload(t, ev, operation, action, class)
		if i > 0 && ev.CorrelationID != evs[1].CorrelationID {
			t.Errorf("event %d (%s) joins %s, want the approval %s", i, ev.Type, ev.CorrelationID, evs[1].CorrelationID)
		}
	}
	if evs[1].CausationID != evs[0].ID || evs[3].CausationID != evs[2].ID {
		t.Errorf("outcome events do not name their request as cause: %+v", evs)
	}
	if p, _ := commandtest.Payload(evs[2]); !p.Resumed {
		t.Errorf("the second request does not say it resumed: %+v", p)
	}
	return evs
}

// noTokensInEvents fails when an event carries a confirmation token.
func noTokensInEvents(t *testing.T, em *commandtest.Emitter) {
	t.Helper()
	for _, ev := range em.Events() {
		if strings.Contains(string(ev.Payload), "cf_") {
			t.Errorf("%s payload carries a token: %s", ev.Type, ev.Payload)
		}
	}
}

// quietStdout sends what commands print to standard output to a file for the
// rest of the test, and returns a function that reads it.
func quietStdout(t *testing.T) func() string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = f
	t.Cleanup(func() {
		os.Stdout = previous
		f.Close()
	})
	return func() string {
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}

// quietStderr sends what commands print to standard error to a file for the
// rest of the test.
func quietStderr(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = f
	t.Cleanup(func() {
		os.Stderr = previous
		f.Close()
	})
}

// wantWorktreeScope fails unless every event is tagged with the worktree
// (repoWide false) or none is (repoWide true).
func wantWorktreeScope(t *testing.T, evs []bus.Event, repoWide bool) {
	t.Helper()
	for _, ev := range evs {
		if repoWide && ev.Worktree != "" {
			t.Errorf("%s is tagged with worktree %q, want a repository-wide event", ev.Type, ev.Worktree)
		}
		if !repoWide && ev.Worktree != commandtest.Worktree {
			t.Errorf("%s has worktree %q, want %q", ev.Type, ev.Worktree, commandtest.Worktree)
		}
	}
}
