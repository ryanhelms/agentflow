package ops_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/ryanhelms/agentflow/internal/hooks"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// The delivery operations (lane 3.2): commit, push, pr, review, status and
// collisions. These tests drive them through the registry, as the CLI and
// the MCP server do, with a recording Emitter: each write publishes
// op.requested and then the right outcome for the confirm, apply, suggest and
// refusal paths.

const (
	deliveryTitle    = "feat: publish the branch"
	deliveryRepoConf = "commit: {require_key: false}\ntask_management: {provider: none}\npolicy:\n  max_autonomy: {remote: confirm}\n"
	deliveryConfirm  = "autonomy: {local: confirm, remote: confirm}\n"
)

// deliveryFixture is a repository on branch feature/pr whose GitHub remote is
// a local bare repository, and whose GitHub API answers come from handler.
type deliveryFixture struct {
	dir  string
	bare string
	app  *app.App
}

func deliveryGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func deliveryWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newDeliveryFixture builds the repository. user is the personal autonomy
// file. handler answers GitHub API requests; nil refuses every one.
func newDeliveryFixture(t *testing.T, user string, handler func(*http.Request) (*http.Response, error)) *deliveryFixture {
	t.Helper()
	goldentest.Isolate(t)
	home := t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "fixture")
	deliveryWrite(t, filepath.Join(home, "config.yml"), user)
	if handler == nil {
		handler = func(r *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("unexpected HTTP request %s %s", r.Method, r.URL)
		}
	}
	original := http.DefaultTransport
	http.DefaultTransport = roundTrip(handler)
	t.Cleanup(func() { http.DefaultTransport = original })

	f := &deliveryFixture{dir: t.TempDir(), bare: t.TempDir()}
	deliveryGit(t, f.bare, "init", "--quiet", "--template=", "--bare", "-b", "main")
	script := filepath.Join(t.TempDir(), "ssh.sh")
	deliveryWrite(t, script, `case "$*" in
  *git-receive-pack*) exec git-receive-pack "$AGENTFLOW_TEST_BARE" ;;
  *git-upload-pack*) exec git-upload-pack "$AGENTFLOW_TEST_BARE" ;;
  *) exit 1 ;;
esac
`)
	t.Setenv("AGENTFLOW_TEST_BARE", filepath.ToSlash(f.bare))
	t.Setenv("AGENTFLOW_TEST_SSH", filepath.ToSlash(script))
	t.Setenv("GIT_SSH_COMMAND", `sh "$AGENTFLOW_TEST_SSH"`)
	t.Setenv("GIT_SSH_VARIANT", "simple")
	deliveryGit(t, f.dir, "init", "--quiet", "--template=", "-b", "main")
	deliveryGit(t, f.dir, "config", "user.name", "Fixture")
	deliveryGit(t, f.dir, "config", "user.email", "test@example.org")
	deliveryGit(t, f.dir, "remote", "add", "origin", "git@github.com:team/app.git")
	deliveryWrite(t, filepath.Join(f.dir, ".agentflow", "config.yml"), deliveryRepoConf)
	deliveryGit(t, f.dir, "add", ".agentflow/config.yml")
	deliveryGit(t, f.dir, "commit", "--quiet", "-m", "chore: configure repository policy")
	deliveryGit(t, f.dir, "push", "--quiet", "-u", "origin", "main")
	deliveryGit(t, f.dir, "remote", "set-head", "origin", "main")
	deliveryGit(t, f.dir, "switch", "--quiet", "-c", "feature/pr")
	deliveryGit(t, f.dir, "commit", "--quiet", "--allow-empty", "-m", deliveryTitle)
	a, err := app.Open(t.Context(), f.dir, "cli")
	if err != nil {
		t.Fatal(err)
	}
	f.app = a
	return f
}

func (f *deliveryFixture) head(t *testing.T) string {
	return deliveryGit(t, f.dir, "rev-parse", "HEAD")
}

// run dispatches args as the CLI does and runs the command with a recording
// Emitter, the real audit log and a terminal that answers answer. Text the
// commands print to standard output is returned.
func (f *deliveryFixture) run(t *testing.T, answer bool, args ...string) (*commandtest.Emitter, *commandtest.Env, string, error) {
	t.Helper()
	dispatch, rest, ok := command.Default.Dispatch(args)
	if !ok {
		t.Fatalf("agentflow %v is not a registered command", args)
	}
	em, inv := recording()
	env := &commandtest.Env{App: f.app, Answer: answer}
	var err error
	out := captureStdout(t, func() { err = dispatch(t.Context(), env, inv, rest) })
	return em, env, out, err
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	previous := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = previous }()
	fn()
	os.Stdout = previous
	b, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// auditOf returns the lines of the App's audit.log.
func auditOf(t *testing.T, a *app.App) []map[string]any {
	t.Helper()
	file, err := os.Open(filepath.Join(a.Home, "audit.log"))
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

func wantTypes(t *testing.T, em *commandtest.Emitter, want ...bus.Type) []bus.Event {
	t.Helper()
	if got := em.Types(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	return em.Events()
}

func payloadOf(t *testing.T, ev bus.Event) bus.OpPayload {
	t.Helper()
	p, err := commandtest.Payload(ev)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// noLeak fails when an event payload holds any of the secrets.
func noLeak(t *testing.T, evs []bus.Event, secrets ...string) {
	t.Helper()
	for _, ev := range evs {
		for _, s := range secrets {
			if s != "" && strings.Contains(string(ev.Payload), s) {
				t.Errorf("%s payload carries %q: %s", ev.Type, s, ev.Payload)
			}
		}
	}
}

// operationAudit returns the audit lines the Invoker wrote for operation name.
func operationAudit(lines []map[string]any, name string) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["operation"] == name {
			out = append(out, l)
		}
	}
	return out
}

func TestDeliveryOperationsMatchTheLegacyCommands(t *testing.T) {
	// name: tool, CLI path (the legacy parity row), class, action.
	type row struct{ tool, cli, class, action string }
	want := map[string]row{
		"commit.plan":           {"agentflow_commit_plan", "commit", "read", ""},
		"commit.context":        {"agentflow_commit_context", "commit", "local", "commit_context"},
		"commit.validate":       {"agentflow_commit_validate", "lint-msg", "read", ""},
		"commit.preview":        {"agentflow_commit_preview", "preview", "read", ""},
		"commit.prepare":        {"agentflow_commit_prepare", "commit", "local", "commit"},
		"commit":                {"agentflow_commit", "commit", "local", "commit"},
		"push.prepare":          {"agentflow_push_prepare", "push", "remote", "push"},
		"push":                  {"agentflow_push", "push", "remote", "push"},
		"pr.ready":              {"agentflow_pr_ready", "pr", "read", ""},
		"pr.prepare":            {"agentflow_pr_prepare", "pr", "remote", "pr_open"},
		"pr.open":               {"agentflow_pr_open", "pr", "remote", "pr_open"},
		"pr.mark-ready.prepare": {"agentflow_pr_mark_ready_prepare", "pr mark-ready", "remote", "pr_ready_for_review"},
		"pr.mark-ready":         {"agentflow_pr_mark_ready", "pr mark-ready", "remote", "pr_ready_for_review"},
		"review.threads":        {"agentflow_review_threads", "review", "read", ""},
		"review.reply":          {"agentflow_review_reply", "review reply", "remote", "review_reply"},
		"ship.status":           {"agentflow_ship_status", "status", "read", ""},
		"collisions":            {"agentflow_collisions", "collisions", "read", ""},
	}
	rows := command.Default.ParityRows()
	for name, w := range want {
		c, ok := command.Default.Lookup(name)
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		m := c.Meta()
		if m.Tool != w.tool || strings.Join(m.CLIPath, " ") != w.cli || string(m.Class) != w.class || m.Action != w.action {
			t.Errorf("%s: tool %q, CLI %q, class %q, action %q; want %+v", name, m.Tool, strings.Join(m.CLIPath, " "), m.Class, m.Action, w)
		}
		if rows[w.tool] != w.cli {
			t.Errorf("parity row of %s is %q, want %q", w.tool, rows[w.tool], w.cli)
		}
		// An app method that writes its own audit line keeps it: only the
		// operations whose method writes none are audited by the Invoker.
		prepares := strings.HasSuffix(name, ".prepare") || name == "commit.context"
		if m.Class != policy.Read && m.AuditByInvoker != prepares {
			t.Errorf("%s: AuditByInvoker is %v, want %v", name, m.AuditByInvoker, prepares)
		}
		if m.PrepareOnly != strings.HasSuffix(name, ".prepare") {
			t.Errorf("%s: PrepareOnly is %v", name, m.PrepareOnly)
		}
		if m.NativeReads || m.WriteHost != command.WriteHostNone {
			t.Errorf("%s: native reads %v, write host %v", name, m.NativeReads, m.WriteHost)
		}
	}
	for _, path := range [][]string{{"commit"}, {"preview"}, {"push"}, {"pr"}, {"pr", "mark-ready"}, {"review"}, {"review", "reply"}, {"status"}, {"collisions"}} {
		if _, _, ok := command.Default.Dispatch(path); !ok {
			t.Errorf("agentflow %s is not dispatched by the registry", strings.Join(path, " "))
		}
	}
	// lint-msg stays a runtime command of the legacy switch.
	if _, _, ok := command.Default.Dispatch([]string{"lint-msg", "FILE"}); ok {
		t.Error("lint-msg is dispatched by the registry")
	}
}

func TestCommitPublishesConfirmApplyAndRefusal(t *testing.T) {
	const msg = "feat: add widget\n\nsecret body of the commit"
	f := newDeliveryFixture(t, deliveryConfirm, nil)
	deliveryWrite(t, filepath.Join(f.dir, "widget.txt"), "widget\n")
	before := len(auditOf(t, f.app))

	em, env, _, err := f.run(t, true, "commit", "-m", msg, "--path", "widget.txt")
	if err != nil {
		t.Fatal(err)
	}
	evs := wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpApplied)
	p := payloadOf(t, evs[0])
	if p.Operation != "commit" || p.Action != "commit" || p.Class != "local" || p.Subject != "feat: add widget" || p.Client != "cli" {
		t.Errorf("requested payload %+v", p)
	}
	applied := payloadOf(t, evs[3])
	// The app method reports an abbreviated object name.
	sha := f.head(t)
	if len(applied.Links) != 1 || applied.Links[0].Kind != bus.OpLinkCommit || applied.Links[0].ID == "" || !strings.HasPrefix(sha, applied.Links[0].ID) {
		t.Errorf("applied links %+v, want the commit %s", applied.Links, sha)
	}
	if !payloadOf(t, evs[2]).Resumed || evs[1].CorrelationID != evs[3].CorrelationID {
		t.Errorf("the resumed call does not join the approval: %+v", evs)
	}
	noLeak(t, evs, "secret body", "cf_")
	if !strings.Contains(env.Out.String(), `"status": "committed"`) {
		t.Errorf("output %q", env.Out.String())
	}
	// One audit line, written by the app method, as before the migration.
	lines := auditOf(t, f.app)[before:]
	if len(lines) != 1 || lines[0]["action"] != "commit" || lines[0]["operation"] != nil {
		t.Errorf("audit lines %v, want the app method's single line", lines)
	}

	// A message that breaks the convention is refused: op.failed with the status.
	deliveryWrite(t, filepath.Join(f.dir, "other.txt"), "other\n")
	em, env, _, err = f.run(t, true, "commit", "-m", "added a widget", "--path", "other.txt", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	evs = wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	if code := payloadOf(t, evs[1]).Code; code != "invalid" {
		t.Errorf("refusal code %q, want invalid", code)
	}
	if !strings.Contains(env.Out.String(), `"status": "invalid"`) {
		t.Errorf("output %q", env.Out.String())
	}

	// A forged token is refused by the gate.
	em, _, _, err = f.run(t, true, "commit", "-m", "feat: add other", "--path", "other.txt", "--confirm-token", "cf_forged")
	if err == nil {
		t.Fatal("a forged token was accepted")
	}
	evs = wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	if !payloadOf(t, evs[0]).Resumed || payloadOf(t, evs[1]).Code == "" || payloadOf(t, evs[1]).Code == command.CodeUnexpectedResult {
		t.Errorf("forged token events: %+v", evs)
	}
	noLeak(t, evs, "cf_forged")

	// Declining at the prompt leaves the confirmation pending and commits nothing.
	head := f.head(t)
	em, _, _, err = f.run(t, false, "commit", "-m", "feat: add other", "--path", "other.txt")
	if err == nil {
		t.Fatal("a declined commit succeeded")
	}
	wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval)
	if f.head(t) != head {
		t.Error("a declined commit changed HEAD")
	}
}

func TestCommitSuggestAndPrepare(t *testing.T) {
	f := newDeliveryFixture(t, "autonomy: {local: suggest}\n", nil)
	deliveryWrite(t, filepath.Join(f.dir, "widget.txt"), "widget\n")
	em, _, _, err := f.run(t, true, "commit", "-m", "feat: add widget", "--path", "widget.txt")
	if err != nil {
		t.Fatal(err)
	}
	evs := wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpSuggested)
	if p := payloadOf(t, evs[1]); p.Status != "suggest" {
		t.Errorf("suggested payload %+v", p)
	}

	// commit --prepare prepares the exact commit and applies nothing. Its
	// app method writes no audit line, so the Invoker writes one.
	before := len(auditOf(t, f.app))
	em, env, _, err := f.run(t, true, "commit", "-m", "feat: add widget", "--path", "widget.txt", "--prepare")
	if err != nil {
		t.Fatal(err)
	}
	wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpSuggested)
	lines := operationAudit(auditOf(t, f.app)[before:], "commit.prepare")
	if len(lines) != 1 || lines[0]["result"] != "suggested" || lines[0]["action"] != "commit" {
		t.Errorf("prepare audit lines %v", lines)
	}
	if !strings.Contains(env.Out.String(), `"status": "suggest"`) {
		t.Errorf("output %q", env.Out.String())
	}

	g := newDeliveryFixture(t, "autonomy: {local: auto}\n", nil)
	deliveryWrite(t, filepath.Join(g.dir, "widget.txt"), "widget\n")
	em, _, _, err = g.run(t, true, "commit", "-m", "feat: add widget", "--path", "widget.txt", "--prepare")
	if err != nil {
		t.Fatal(err)
	}
	evs = wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpPrepared)
	if p := payloadOf(t, evs[1]); p.Status != "prepared" || p.ApprovalID == "" {
		t.Errorf("prepared payload %+v", p)
	}
	if g.head(t) == "" || deliveryGit(t, g.dir, "log", "-1", "--format=%s") != deliveryTitle {
		t.Error("prepare committed")
	}
}

func TestCommitContextStagesPathsAndAuditsThem(t *testing.T) {
	f := newDeliveryFixture(t, deliveryConfirm, nil)
	deliveryWrite(t, filepath.Join(f.dir, "widget.txt"), "widget\n")
	before := len(auditOf(t, f.app))
	em, inv := recording()

	// Without paths it only reads the staged index: nothing to publish or audit.
	deliveryGit(t, f.dir, "add", "widget.txt")
	if _, isErr := invokeTool(t, f.app, "commit.context", map[string]any{}, inv); isErr {
		t.Fatal("commit context of the staged index failed")
	}
	if len(em.Events()) != 0 || len(auditOf(t, f.app)) != before {
		t.Errorf("a context without paths published %v and audited %d lines", em.Types(), len(auditOf(t, f.app))-before)
	}

	// With paths it stages them: a local write.
	deliveryGit(t, f.dir, "reset", "--quiet")
	if out, isErr := invokeTool(t, f.app, "commit.context", map[string]any{"paths": []string{"widget.txt"}}, inv); isErr {
		t.Fatalf("commit context with paths failed: %s", out)
	}
	evs := wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpApplied)
	if p := payloadOf(t, evs[0]); p.Operation != "commit.context" || p.Subject != "stage 1 paths" || p.Client != "test" {
		t.Errorf("payload %+v", p)
	}
	if staged := deliveryGit(t, f.dir, "diff", "--cached", "--name-only"); staged != "widget.txt" {
		t.Errorf("staged %q", staged)
	}
	lines := operationAudit(auditOf(t, f.app)[before:], "commit.context")
	if len(lines) != 1 || lines[0]["result"] != "applied" || lines[0]["operation_id"] != evs[1].CorrelationID {
		t.Errorf("audit lines %v", lines)
	}
}

func TestInteractiveCommitReadsTheContextThenValidates(t *testing.T) {
	f := newDeliveryFixture(t, deliveryConfirm, nil)
	// Nothing staged: the context fails before any question, and a context
	// without paths is a read, so nothing is published.
	em, _, _, err := f.run(t, true, "commit")
	if err == nil || !strings.Contains(err.Error(), "nothing staged") {
		t.Fatalf("commit with nothing staged: %v", err)
	}
	if len(em.Events()) != 0 {
		t.Errorf("published %v", em.Types())
	}

	// With a staged file and no answers the message is empty, so the
	// convention check refuses it and the command exits with code 1.
	deliveryWrite(t, filepath.Join(f.dir, "widget.txt"), "widget\n")
	deliveryGit(t, f.dir, "add", "widget.txt")
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	previous := os.Stdin
	os.Stdin = null
	defer func() { os.Stdin = previous }()
	em, _, out, err := f.run(t, true, "commit")
	var exit *hooks.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(out, "✖") {
		t.Fatalf("interactive commit: %v, output %q", err, out)
	}
	if len(em.Events()) != 0 {
		t.Errorf("an invalid message published %v", em.Types())
	}
}

func TestCommitFlagRulesAreTheLegacyOnes(t *testing.T) {
	f := newDeliveryFixture(t, deliveryConfirm, nil)
	const exact = "use -m MSG for an exact commit and select one of --prepare or --confirm-token"
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"commit", "--prepare"}, exact},
		{[]string{"commit", "--confirm-token", "t"}, exact},
		{[]string{"commit", "-m", "feat: x", "--prepare", "--yes"}, exact},
		{[]string{"commit", "-m", "feat: x", "--path", "a", "--split"}, exact},
		{[]string{"commit", "--path", ""}, "--path requires a path"},
		{[]string{"push", "extra"}, "unexpected arguments to push"},
		{[]string{"push", "--prepare", "--yes"}, "--prepare cannot be combined with --yes or --confirm-token"},
		{[]string{"pr", "mark-ready", "--prepare", "--confirm-token", "t"}, "--prepare cannot be combined with --yes or --confirm-token"},
		{[]string{"pr", "--prepare", "--check"}, "use one of --prepare, --confirm-token or --check"},
		{[]string{"review", "extra"}, "usage: agentflow review [threads] [--json] | agentflow review reply THREAD_ID BODY [--yes]"},
		{[]string{"review", "reply", "only-thread"}, "usage: agentflow review reply THREAD_ID BODY [--yes]"},
		{[]string{"collisions", "A", "B"}, "usage: agentflow collisions [KEY] [--json]"},
	} {
		em, _, _, err := f.run(t, true, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("agentflow %v: %v, want %q", tc.args, err, tc.want)
		}
		if len(em.Events()) != 0 {
			t.Errorf("agentflow %v published %v for a request that never became an operation", tc.args, em.Types())
		}
	}
}

func TestPushPublishesPrepareApplyAndRefusal(t *testing.T) {
	f := newDeliveryFixture(t, deliveryConfirm, nil)
	before := len(auditOf(t, f.app))

	em, _, _, err := f.run(t, true, "push", "--prepare")
	if err != nil {
		t.Fatal(err)
	}
	evs := wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval)
	if p := payloadOf(t, evs[0]); p.Operation != "push.prepare" || p.Action != "push" || p.Class != "remote" {
		t.Errorf("payload %+v", p)
	}
	if lines := operationAudit(auditOf(t, f.app)[before:], "push.prepare"); len(lines) != 1 || lines[0]["result"] != "awaiting_approval" {
		t.Errorf("prepare audit lines %v", lines)
	}
	if refs := deliveryGit(t, f.bare, "for-each-ref", "--format=%(refname)"); refs != "refs/heads/main" {
		t.Fatalf("prepare published %q", refs)
	}

	before = len(auditOf(t, f.app))
	em, env, _, err := f.run(t, true, "push", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	evs = wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpApplied)
	links := payloadOf(t, evs[3]).Links
	if len(links) != 2 || links[0] != (bus.OpLink{Kind: bus.OpLinkBranch, ID: "feature/pr"}) || links[1] != (bus.OpLink{Kind: bus.OpLinkCommit, ID: f.head(t)}) {
		t.Errorf("applied links %+v", links)
	}
	noLeak(t, evs, "cf_")
	if refs := deliveryGit(t, f.bare, "for-each-ref", "--format=%(refname)"); !strings.Contains(refs, "refs/heads/feature/pr") {
		t.Errorf("push published %q", refs)
	}
	// The map is printed as before: the named type marshals like the map.
	var printed map[string]any
	if err := json.Unmarshal(env.Out.Bytes(), &printed); err != nil || printed["status"] != "pushed" || printed["branch"] != "feature/pr" {
		t.Errorf("output %q: %v", env.Out.String(), err)
	}
	if lines := auditOf(t, f.app)[before:]; len(lines) != 1 || lines[0]["action"] != "push" || lines[0]["operation"] != nil {
		t.Errorf("audit lines %v, want the app method's single line", lines)
	}

	// A branch that fails its readiness checks is refused, not applied.
	g := newDeliveryFixture(t, deliveryConfirm, nil)
	deliveryGit(t, g.dir, "commit", "--quiet", "--allow-empty", "-m", "added a thing")
	em, env, _, err = g.run(t, true, "push", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	evs = wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	if code := payloadOf(t, evs[1]).Code; code != "not_ready" {
		t.Errorf("refusal code %q, want not_ready", code)
	}
	if !strings.Contains(env.Out.String(), `"status": "not_ready"`) {
		t.Errorf("output %q", env.Out.String())
	}
}

// stubGitHub answers the GitHub API calls of a test. Each route is
// "METHOD path", or "POST graphql NAME" for a GraphQL operation whose query
// contains NAME, and gives the JSON body to return. A request without a
// route fails the call. The requests answered are appended to seen, if set.
// The routes may change while the test runs.
func stubGitHub(t *testing.T, routes map[string]string, seen *[]string) {
	t.Helper()
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTrip(func(r *http.Request) (*http.Response, error) {
		key := r.Method + " " + r.URL.Path
		if r.URL.Path == "/graphql" {
			raw, _ := io.ReadAll(r.Body)
			var op struct{ Query string }
			_ = json.Unmarshal(raw, &op)
			key = ""
			for route := range routes {
				if name, ok := strings.CutPrefix(route, "POST graphql "); ok && strings.Contains(op.Query, name) {
					key = route
				}
			}
		}
		body, ok := routes[key]
		if !ok {
			return nil, fmt.Errorf("unexpected GitHub request %s %s", r.Method, r.URL)
		}
		if seen != nil {
			*seen = append(*seen, key)
		}
		rec := httptest.NewRecorder()
		_, _ = rec.WriteString(body)
		return rec.Result(), nil
	})
}

func TestPROpenPublishesConfirmApplyAndRefusal(t *testing.T) {
	var seen []string
	f := newDeliveryFixture(t, deliveryConfirm, nil)
	sha := f.head(t)
	opened := fmt.Sprintf(`{"number":7,"html_url":"https://github.com/team/app/pull/7","draft":false,"head":{"sha":%q}}`, sha)
	stubGitHub(t, map[string]string{"POST /repos/team/app/pulls": opened}, &seen)

	// A title that breaks the convention is refused before anything is published.
	em, _, _, err := f.run(t, true, "pr", "--prepare", "--title", "Publish the branch", "--body", "body")
	if err != nil {
		t.Fatal(err)
	}
	evs := wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	if code := payloadOf(t, evs[1]).Code; code != "invalid_title" {
		t.Errorf("refusal code %q, want invalid_title", code)
	}

	before := len(auditOf(t, f.app))
	em, _, _, err = f.run(t, true, "pr", "--prepare", "--title", deliveryTitle, "--body", "the body of the PR")
	if err != nil {
		t.Fatal(err)
	}
	evs = wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval)
	if p := payloadOf(t, evs[0]); p.Operation != "pr.prepare" || p.Subject != deliveryTitle {
		t.Errorf("payload %+v", p)
	}
	noLeak(t, evs, "the body of the PR")
	if lines := operationAudit(auditOf(t, f.app)[before:], "pr.prepare"); len(lines) != 1 {
		t.Errorf("prepare audit lines %v", lines)
	}
	if len(seen) != 0 {
		t.Fatalf("prepare made GitHub requests %v", seen)
	}

	before = len(auditOf(t, f.app))
	em, env, _, err := f.run(t, true, "pr", "--title", deliveryTitle, "--body", "the body of the PR", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	evs = wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpApplied)
	links := payloadOf(t, evs[3]).Links
	if len(links) != 1 || links[0] != (bus.OpLink{Kind: bus.OpLinkPull, ID: "7", URL: "https://github.com/team/app/pull/7"}) {
		t.Errorf("applied links %+v", links)
	}
	noLeak(t, evs, "the body of the PR", "cf_")
	var printed map[string]any
	if err := json.Unmarshal(env.Out.Bytes(), &printed); err != nil || printed["status"] != "opened" || printed["number"] != float64(7) {
		t.Errorf("output %q: %v", env.Out.String(), err)
	}
	if lines := auditOf(t, f.app)[before:]; len(lines) != 1 || lines[0]["action"] != "pr_open" || lines[0]["operation"] != nil {
		t.Errorf("audit lines %v, want the app method's single line", lines)
	}
	if !slices.Contains(seen, "POST /repos/team/app/pulls") {
		t.Errorf("no pull request was created: %v", seen)
	}
}

func TestPRReadinessCheckIsARead(t *testing.T) {
	f := newDeliveryFixture(t, deliveryConfirm, nil)
	em, _, out, err := f.run(t, true, "pr", "--check")
	if err != nil {
		t.Fatalf("readiness check: %v", err)
	}
	if len(em.Events()) != 0 || !strings.Contains(out, "✓") {
		t.Errorf("events %v, output %q", em.Types(), out)
	}
	// A not ready branch exits with code 1, as before.
	deliveryWrite(t, filepath.Join(f.dir, "scratch.txt"), "x\n")
	deliveryGit(t, f.dir, "commit", "--quiet", "--allow-empty", "-m", "added a thing")
	if _, _, out, err = f.run(t, true, "pr", "--check"); err == nil || !strings.Contains(out, "✖") || err.Error() != "not ready" {
		t.Errorf("not ready branch: %v, output %q", err, out)
	}
}

func TestMarkReadyPublishesPrepareAndApply(t *testing.T) {
	var seen []string
	f := newDeliveryFixture(t, deliveryConfirm, nil)
	sha := f.head(t)
	deliveryGit(t, f.dir, "push", "--quiet", "origin", "feature/pr")
	pull := func(draft bool) string {
		return fmt.Sprintf(`[{"number":7,"node_id":"PR_7","state":"open","draft":%v,"title":"t","html_url":"https://github.com/team/app/pull/7","base":{"ref":"main"},"head":{"ref":"feature/pr","sha":%q}}]`, draft, sha)
	}
	routes := map[string]string{
		"GET /repos/team/app/pulls":                  pull(true),
		"POST graphql markPullRequestReadyForReview": fmt.Sprintf(`{"data":{"markPullRequestReadyForReview":{"pullRequest":{"id":"PR_7","isDraft":false,"headRefOid":%q}}}}`, sha),
	}
	stubGitHub(t, routes, &seen)

	before := len(auditOf(t, f.app))
	em, _, _, err := f.run(t, true, "pr", "mark-ready", "--prepare")
	if err != nil {
		t.Fatal(err)
	}
	evs := wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval)
	if p := payloadOf(t, evs[0]); p.Operation != "pr.mark-ready.prepare" || p.Action != "pr_ready_for_review" {
		t.Errorf("payload %+v", p)
	}
	if lines := operationAudit(auditOf(t, f.app)[before:], "pr.mark-ready.prepare"); len(lines) != 1 {
		t.Errorf("prepare audit lines %v", lines)
	}

	before = len(auditOf(t, f.app))
	em, _, _, err = f.run(t, true, "pr", "mark-ready", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	evs = wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpApplied)
	if links := payloadOf(t, evs[3]).Links; len(links) != 1 || links[0].Kind != bus.OpLinkPull || links[0].ID != "7" {
		t.Errorf("applied links %+v", links)
	}
	if lines := auditOf(t, f.app)[before:]; len(lines) != 1 || lines[0]["action"] != "pr_ready_for_review" || lines[0]["operation"] != nil {
		t.Errorf("audit lines %v, want the app method's single line", lines)
	}

	// A pull request that is already ready needs nothing: applied, not failed.
	routes["GET /repos/team/app/pulls"] = pull(false)
	em, env, _, err := f.run(t, true, "pr", "mark-ready", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpApplied)
	if !strings.Contains(env.Out.String(), `"status": "already_ready"`) {
		t.Errorf("output %q", env.Out.String())
	}
}

func TestReviewReplyPublishesConfirmAndApply(t *testing.T) {
	routes := map[string]string{
		"POST graphql PullRequestReviewThread":         `{"data":{"node":{"pullRequest":{"number":7,"repository":{"nameWithOwner":"team/app"}}}}}`,
		"POST graphql addPullRequestReviewThreadReply": `{"data":{"addPullRequestReviewThreadReply":{"comment":{"id":"C1"}}}}`,
	}
	f := newDeliveryFixture(t, deliveryConfirm, nil)
	stubGitHub(t, routes, nil)
	before := len(auditOf(t, f.app))

	em, env, _, err := f.run(t, true, "review", "reply", "T1", "Fixed in the last commit.", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	evs := wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpApplied)
	if p := payloadOf(t, evs[0]); p.Operation != "review.reply" || p.Action != "review_reply" || p.Subject != "reply on a review thread" {
		t.Errorf("payload %+v", p)
	}
	noLeak(t, evs, "Fixed in the last commit.", "cf_")
	if !strings.Contains(env.Out.String(), `"replied": true`) {
		t.Errorf("output %q", env.Out.String())
	}
	if lines := auditOf(t, f.app)[before:]; len(lines) != 1 || lines[0]["action"] != "review_reply" || lines[0]["operation"] != nil {
		t.Errorf("audit lines %v, want the app method's single line", lines)
	}

	// The MCP tool runs the same Spec; a forged token is refused by the gate.
	em, inv := recording()
	in := map[string]any{"thread_id": "T1", "body": "x", "confirm_token": "cf_forged"}
	if out, isErr := invokeTool(t, f.app, "review.reply", in, inv); !isErr {
		t.Fatalf("forged token accepted: %s", out)
	}
	evs = wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	if code := payloadOf(t, evs[1]).Code; code == "" || code == command.CodeUnexpectedResult {
		t.Errorf("refusal code %q", code)
	}
}

func TestDeliveryReadsPublishNothing(t *testing.T) {
	routes := map[string]string{"GET /repos/team/app/pulls": `[]`}
	f := newDeliveryFixture(t, deliveryConfirm, nil)
	stubGitHub(t, routes, nil)
	deliveryWrite(t, filepath.Join(f.dir, "widget.txt"), "widget\n")
	before := len(auditOf(t, f.app))
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"commit", "--split"}, "Stage one group"},
		{[]string{"commit", "-m", "feat: add widget", "--dry-run"}, "✓ valid"},
		{[]string{"preview", "feat: add widget"}, "feat: add widget"},
		{[]string{"review", "--json"}, `"threads"`},
		{[]string{"review", "threads"}, "no open PR for this branch"},
		{[]string{"status", "ignored", "--arguments"}, ""},
		{[]string{"collisions", "--json"}, ""},
		{[]string{"collisions"}, "No other open PRs"},
	} {
		em, env, out, err := f.run(t, true, tc.args...)
		if err != nil {
			t.Errorf("agentflow %v: %v", tc.args, err)
			continue
		}
		if len(em.Events()) != 0 {
			t.Errorf("agentflow %v published %v", tc.args, em.Types())
		}
		if got := out + env.Out.String(); !strings.Contains(got, tc.want) {
			t.Errorf("agentflow %v printed %q, want %q", tc.args, got, tc.want)
		}
	}
	if got := len(auditOf(t, f.app)); got != before {
		t.Errorf("reads wrote %d audit lines", got-before)
	}
	// The status line of the branch is JSON.
	_, env, _, err := f.run(t, true, "status")
	if err != nil || !strings.Contains(env.Out.String(), `"branch": "feature/pr"`) {
		t.Errorf("status: %q, %v", env.Out.String(), err)
	}
}

func TestDeliveryToolsRunThroughTheRegistry(t *testing.T) {
	f := newDeliveryFixture(t, deliveryConfirm, nil)
	deliveryWrite(t, filepath.Join(f.dir, "widget.txt"), "widget\n")
	em, inv := recording()

	if out, isErr := invokeTool(t, f.app, "commit.validate", map[string]any{"message": "added a widget"}, inv); isErr || !strings.Contains(out, `"valid":false`) {
		t.Errorf("commit.validate: %s", out)
	}
	if out, isErr := invokeTool(t, f.app, "commit.validate", map[string]any{"message": "feat: add widget"}, inv); isErr || !strings.Contains(out, `"valid":true`) {
		t.Errorf("commit.validate: %s", out)
	}
	if out, isErr := invokeTool(t, f.app, "commit.preview", map[string]any{"message": "feat: add widget"}, inv); isErr || !strings.Contains(out, `"effects"`) {
		t.Errorf("commit.preview: %s", out)
	}
	if out, isErr := invokeTool(t, f.app, "commit.plan", map[string]any{}, inv); isErr || !strings.Contains(out, "widget.txt") {
		t.Errorf("commit.plan: %s", out)
	}
	if len(em.Events()) != 0 {
		t.Errorf("read tools published %v", em.Types())
	}

	// commit.prepare refuses a message that breaks the convention: op.failed.
	out, isErr := invokeTool(t, f.app, "commit.prepare", map[string]any{"message": "added a widget", "paths": []string{"widget.txt"}}, inv)
	if isErr || !strings.Contains(out, `"status":"invalid"`) {
		t.Fatalf("commit.prepare: %s", out)
	}
	evs := wantTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	if p := payloadOf(t, evs[1]); p.Code != "invalid" || p.Client != "test" {
		t.Errorf("payload %+v", p)
	}
}
