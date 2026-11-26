package ops_test

import (
	"context"
	"encoding/json"
	"io"
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
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/goldentest"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// taskRepo is a repository whose tasks are markdown files, so every task
// operation runs offline. Its user policy confirms every local and remote
// write, as the golden tests of internal/cli do.
type taskRepo struct {
	dir, home string
	app       *app.App
}

func newTaskRepo(t *testing.T, autonomy string) *taskRepo {
	t.Helper()
	goldentest.Isolate(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yml"), []byte(autonomy), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_HOME", home)
	// Worktrees go to ~/.worktrees: keep them out of the real home.
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "dev@example.com"},
		{"config", "user.name", "Dev"},
		{"commit", "-q", "--allow-empty", "-m", "chore: init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".agentflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "commit: {require_key: false}\ntask_management:\n  provider: filesystem\n  settings: {scope: 'eng/platform'}\n"
	if err := os.WriteFile(filepath.Join(dir, ".agentflow", "config.yml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &taskRepo{dir: dir, home: home}
	r.reopen(t)
	return r
}

// reopen loads the repository and the user policy again, as a new command
// does.
func (r *taskRepo) reopen(t *testing.T) {
	t.Helper()
	a, err := app.Open(context.Background(), r.dir, "cli")
	if err != nil {
		t.Fatal(err)
	}
	r.app = a
}

// setAutonomy replaces the user policy.
func (r *taskRepo) setAutonomy(t *testing.T, autonomy string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.home, "config.yml"), []byte(autonomy), 0o600); err != nil {
		t.Fatal(err)
	}
	r.reopen(t)
}

const confirmAll = "autonomy: {local: confirm, remote: confirm}\n"

// createTask makes a task through the app method, outside the registry, so
// the tests that follow start from a task and no recorded events.
func (r *taskRepo) createTask(t *testing.T, summary, description string) string {
	t.Helper()
	pending, err := r.app.CreateTicket(t.Context(), "", "", summary, description, "", "")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := pending.(*app.Pending)
	if !ok || p.ConfirmToken == "" {
		t.Fatalf("create did not ask for confirmation: %#v", pending)
	}
	created, err := r.app.CreateTicket(t.Context(), "", "", summary, description, "", p.ConfirmToken)
	if err != nil {
		t.Fatal(err)
	}
	return created.(map[string]string)["key"]
}

// taskStdout runs fn with standard output in a file and returns what fn
// printed there. The task commands that print text write to standard output
// as the legacy commands did.
func taskStdout(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = old }()
	fn()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// taskNoTerminal gives the test a standard input that is not a terminal.
func taskNoTerminal(t *testing.T) {
	t.Helper()
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	t.Cleanup(func() { os.Stdin = old; in.Close() })
}

// taskRun runs one CLI command by its words, as cli.run dispatches it: a
// Spec's own CLI or a verb.
func taskRun(t *testing.T, env command.CLIEnv, inv command.Invoker, args ...string) error {
	t.Helper()
	run, rest, ok := command.Default.Dispatch(args)
	if !ok {
		t.Fatalf("no registered command for %v", args)
	}
	return run(t.Context(), env, inv, rest)
}

func taskPayloads(t *testing.T, em *commandtest.Emitter) []bus.OpPayload {
	t.Helper()
	var out []bus.OpPayload
	for _, ev := range em.Events() {
		p, err := commandtest.Payload(ev)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

var confirmedTypes = []bus.Type{bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpApplied}

// checkConfirmed checks that the events are a confirmed write: a request and
// the wait for approval, then the resumed request and its applied outcome, all
// of one operation, and that no payload carries any of secrets.
func checkConfirmed(t *testing.T, em *commandtest.Emitter, operation string, secrets ...string) []bus.OpPayload {
	t.Helper()
	if !slices.Equal(em.Types(), confirmedTypes) {
		t.Fatalf("%s events %v, want %v", operation, em.Types(), confirmedTypes)
	}
	evs := em.Events()
	approval := evs[1].CorrelationID
	if evs[1].CausationID != evs[0].ID || evs[2].CorrelationID != approval || evs[3].CorrelationID != approval || evs[3].CausationID != evs[2].ID {
		t.Errorf("%s events are not correlated to the approval %s: %+v", operation, approval, evs)
	}
	for _, ev := range evs {
		for _, secret := range append(secrets, "cf_") {
			if strings.Contains(string(ev.Payload), secret) {
				t.Errorf("%s payload carries %q: %s", operation, secret, ev.Payload)
			}
		}
	}
	payloads := taskPayloads(t, em)
	for _, p := range payloads {
		if p.Operation != operation {
			t.Errorf("payload operation %q, want %q", p.Operation, operation)
		}
	}
	if !payloads[2].Resumed || payloads[0].Resumed {
		t.Errorf("resumed flags: first %v, second %v", payloads[0].Resumed, payloads[2].Resumed)
	}
	return payloads
}

func TestTaskCreatePublishesConfirmApplyPreviewAndRefusal(t *testing.T) {
	r := newTaskRepo(t, confirmAll)
	taskNoTerminal(t)
	em, inv := recording()
	env := &commandtest.Env{App: r.app}

	// A preview with --dry-run changes nothing and is not an operation.
	if err := taskRun(t, env, inv, "task", "create", "--summary", "Review quotas", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if got := em.Types(); len(got) != 0 {
		t.Fatalf("dry run published %v", got)
	}
	if !strings.Contains(env.Out.String(), "Review quotas") {
		t.Fatalf("dry run output %q", env.Out.String())
	}

	// --yes runs the confirmation flow: the preview waits for approval, the
	// token resumes it and the task is created.
	env = &commandtest.Env{App: r.app}
	if err := taskRun(t, env, inv, "task", "create", "--summary", "Review quotas", "--description", "- [ ] Run tests", "--yes"); err != nil {
		t.Fatal(err)
	}
	payloads := checkConfirmed(t, em, "task.create")
	if p := payloads[0]; p.Action != "ticket_create" || p.Class != "remote" || p.Subject != "Review quotas" || p.Client != "cli" {
		t.Errorf("requested payload %+v", p)
	}
	var created map[string]string
	if err := json.Unmarshal(env.Out.Bytes(), &created); err != nil || created["key"] == "" {
		t.Fatalf("output %q, %v", env.Out.String(), err)
	}
	if links := payloads[3].Links; len(links) != 1 || links[0] != (bus.OpLink{Kind: bus.OpLinkTask, ID: created["key"]}) {
		t.Errorf("applied links %+v, want the created task %s", links, created["key"])
	}

	// A noninteractive run without --yes prepares the candidate and stops
	// with an error: the request and op.prepared are published, nothing is created.
	em, inv = recording()
	env = &commandtest.Env{App: r.app}
	if err := taskRun(t, env, inv, "task", "create", "--summary", "Second task"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("noninteractive create without --yes: %v", err)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpPrepared}) {
		t.Fatalf("preview events %v", got)
	}
	r.app.Gate.PreviewOnly = false

	// A forged token is refused by the gate: op.failed with its code only.
	em, inv = recording()
	out, isErr := invokeTool(t, r.app, "task.create", map[string]any{"summary": "Third task", "confirm_token": "cf_forged"}, inv)
	if !isErr {
		t.Fatalf("forged token accepted: %s", out)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("refusal events %v", got)
	}
	if p := taskPayloads(t, em)[1]; p.Code == "" || p.Code == command.CodeUnexpectedResult {
		t.Errorf("refusal code %q", p.Code)
	}
}

func TestTaskCreateUnderASuggestPolicyPublishesTheSuggestion(t *testing.T) {
	r := newTaskRepo(t, "autonomy: {local: suggest, remote: suggest}\n")
	taskNoTerminal(t)
	em, inv := recording()
	err := taskRun(t, &commandtest.Env{App: r.app}, inv, "task", "create", "--summary", "Review quotas", "--yes")
	if err == nil || !strings.Contains(err.Error(), "suggest") {
		t.Fatalf("suggest policy: %v", err)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpSuggested}) {
		t.Fatalf("events %v", got)
	}
}

func TestStartPublishesConfirmApplyAndRefusal(t *testing.T) {
	r := newTaskRepo(t, confirmAll)
	taskNoTerminal(t)
	key := r.createTask(t, "Review quotas", "- [ ] Run tests")
	em, inv := recording()
	env := &commandtest.Env{App: r.app}
	if err := taskRun(t, env, inv, "start", key, "--worktree", "--yes"); err != nil {
		t.Fatal(err)
	}
	payloads := checkConfirmed(t, em, "start")
	if p := payloads[0]; p.Action != "start" || p.Class != "remote" || p.Subject != "start "+key || p.Task != key {
		t.Errorf("requested payload %+v", p)
	}
	var started app.StartResult
	if err := json.Unmarshal(env.Out.Bytes(), &started); err != nil || started.Branch == "" || started.Worktree == "" {
		t.Fatalf("output %q, %v", env.Out.String(), err)
	}
	if _, err := os.Stat(started.Worktree); err != nil {
		t.Errorf("start did not create the worktree: %v", err)
	}
	wantLinks := []bus.OpLink{{Kind: bus.OpLinkTask, ID: started.Key}, {Kind: bus.OpLinkBranch, ID: started.Branch}}
	if !slices.Equal(payloads[3].Links, wantLinks) {
		t.Errorf("applied links %+v, want %+v", payloads[3].Links, wantLinks)
	}

	// An unknown task is refused by the operation itself.
	em, inv = recording()
	if err := taskRun(t, &commandtest.Env{App: r.app}, inv, "start", "filesystem:eng/platform/missing", "--worktree", "--yes"); err == nil {
		t.Fatal("started a task that does not exist")
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("refusal events %v", got)
	}

	// A forged token is refused by the gate.
	em, inv = recording()
	other := r.createTask(t, "Second task", "")
	if out, isErr := invokeTool(t, r.app, "start", map[string]any{"key": other, "worktree": true, "confirm_token": "cf_forged"}, inv); !isErr {
		t.Fatalf("forged token accepted: %s", out)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("forged token events %v", got)
	}
}

func TestStartUnderASuggestPolicyPublishesTheSuggestion(t *testing.T) {
	r := newTaskRepo(t, confirmAll)
	taskNoTerminal(t)
	key := r.createTask(t, "Review quotas", "")
	r.setAutonomy(t, "autonomy: {local: suggest, remote: suggest}\n")
	em, inv := recording()
	if err := taskRun(t, &commandtest.Env{App: r.app}, inv, "start", key, "--worktree"); err != nil {
		t.Fatal(err)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpSuggested}) {
		t.Fatalf("events %v", got)
	}
}

// taskOperations lists the operations of this family with what each keeps
// from the legacy command and tool: the tool, the CLI command, the policy and
// the native write host.
var taskOperations = []struct {
	name, tool, cli string
	class           policy.Class
	action          string
	host            command.WriteHost
	nativeReads     bool
}{
	{"start", "agentflow_start", "start", policy.Remote, "start", command.WriteHostTasks, false},
	{"task.create", "agentflow_ticket_create", "task create", policy.Remote, "ticket_create", command.WriteHostTasks, false},
	{"task.link", "agentflow_jira_remote_link", "task link", policy.Remote, "jira_remote_link", command.WriteHostTasks, false},
	{"transition", "agentflow_transition", "transition", policy.Remote, "transition", command.WriteHostTasks, false},
	{"comment", "agentflow_comment", "comment", policy.Remote, "comment", command.WriteHostTasks, false},
	{"ac.update", "agentflow_ac_update", "ac done", policy.Remote, "comment", command.WriteHostNone, false},
	{"ac.checklist", "agentflow_ac_checklist", "ac", policy.Read, "", command.WriteHostNone, false},
	{"scope.check", "agentflow_scope_check", "scope", policy.Read, "", command.WriteHostNone, false},
	{"context", "agentflow_context", "context", policy.Read, "", command.WriteHostNone, true},
	{"work", "agentflow_my_work", "work", policy.Read, "", command.WriteHostNone, true},
	{"standup", "agentflow_standup", "standup", policy.Read, "", command.WriteHostNone, false},
}

func TestTaskOperationsKeepTheirContract(t *testing.T) {
	rows := command.Default.ParityRows()
	for _, op := range taskOperations {
		m := lookup(t, op.name).Meta()
		if m.Tool != op.tool || rows[op.tool] != op.cli || strings.Join(m.CLIPath, " ") != op.cli {
			t.Errorf("%s: tool %q on CLI %q (row %q), want %q on %q", op.name, m.Tool, strings.Join(m.CLIPath, " "), rows[op.tool], op.tool, op.cli)
		}
		if m.Class != op.class || m.Action != op.action || m.WriteHost != op.host || m.NativeReads != op.nativeReads {
			t.Errorf("%s: class %q action %q host %v native reads %v, want %q %q %v %v",
				op.name, m.Class, m.Action, m.WriteHost, m.NativeReads, op.class, op.action, op.host, op.nativeReads)
		}
		// Every method audits itself or writes nothing: the Invoker adds no line.
		if m.AuditByInvoker {
			t.Errorf("%s: the Invoker audits an operation whose method audits itself", op.name)
		}
		if op.class == policy.Read {
			if m.Events != command.ScopeNone {
				t.Errorf("%s: a read publishes events", op.name)
			}
			continue
		}
		if m.Events != command.ScopeWorktree || !config.ValidAutonomyAction(op.action) {
			t.Errorf("%s: events %v, action %q", op.name, m.Events, op.action)
		}
	}
}
