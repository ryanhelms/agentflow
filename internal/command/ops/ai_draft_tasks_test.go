package ops_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
	"github.com/ryanhelms/agentflow/internal/command/ops"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// tasksAIRig is a worktree whose ticket and reply features are routed to a
// fake agent, run by a person at a terminal: no agent environment, the
// questions answered by the test, drafts hosted in this process.
type tasksAIRig struct {
	home, wt string
	a        *app.App
	env      *commandtest.Env
	asked    []string
	answer   string
	created  []ops.TaskCreateCall
	posted   []string
}

func newTasksAIRig(t *testing.T, script *acptest.SessionScript, draftLevel config.Level) *tasksAIRig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh wrapper")
	}
	resetFake(t) // clears the agent environment
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	args := acptest.Install(t, bin, "fake-draft", acptest.Script{Session: script})
	r := &tasksAIRig{answer: "y"}
	r.home = aisessiontest.Home(t, aisessiontest.Settings{AgentStart: true,
		Autonomy:  config.Autonomy{Local: config.Confirm, Remote: config.Confirm, Actions: map[string]config.Level{ops.ActionDraft: draftLevel}},
		Features:  config.UserAIFeatures{Ticket: config.UserAIFeature{Harness: "drafter"}, Reply: config.UserAIFeature{Harness: "drafter"}},
		Overrides: map[string]harnessdef.Definition{"drafter": {Name: "Drafter", Command: "fake-draft", Args: args}}}, bin)
	aisessiontest.Ready(t, r.home, "drafter")
	r.wt, _ = aisessiontest.Worktree(t)
	if err := os.Mkdir(filepath.Join(r.wt, ".agentflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.wt, ".agentflow", "config.yml"), []byte("task_management: {provider: filesystem}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", r.wt, "remote", "add", "origin", "git@github.com:team/app.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v %s", err, out)
	}
	oldOptions, oldManager, oldTerminal := ops.AIOptions, app.NewAIManager, ops.StdinIsTerminal
	detach, _ := aisessiontest.InProcessDetach(t)
	ops.AIOptions = func(a *app.App) aisession.Options {
		o := ops.AISessionOptions(a)
		o.Detach = detach
		return o
	}
	app.NewAIManager = aisession.NewManager
	ops.StdinIsTerminal = func() bool { return true }
	restoreAsk := ops.SetTasksDraftAsk(func(prompt string) string {
		r.asked = append(r.asked, prompt)
		return r.answer
	})
	restoreCreate := ops.SetTasksCreate(func(c ops.TaskCreateCall) error {
		r.created = append(r.created, c)
		return nil
	})
	t.Cleanup(func() {
		ops.AIOptions, app.NewAIManager, ops.StdinIsTerminal = oldOptions, oldManager, oldTerminal
		restoreAsk()
		restoreCreate()
	})
	t.Setenv("GH_TOKEN", "fixture")
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	http.DefaultTransport = roundTrip(func(req *http.Request) (*http.Response, error) { return r.github(t, req) })
	r.a, err = app.Open(t.Context(), r.wt, "cli")
	if err != nil {
		t.Fatal(err)
	}
	r.env = &commandtest.Env{App: r.a, Answer: true}
	return r
}

// github answers the reply draft's reads and review reply's write: one PR
// with one open thread.
func (r *tasksAIRig) github(t *testing.T, req *http.Request) (*http.Response, error) {
	reply := func(body string) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	node := `{"id":%q,"isResolved":false,"isOutdated":false,"path":"a.go","line":3,"comments":{"nodes":[{"body":"Why?","author":{"login":"alice"}}]}}`
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	switch {
	case req.URL.Path == "/repos/team/app/pulls":
		return reply(`[{"number":7,"html_url":"https://github.com/team/app/pull/7","head":{"ref":"task","sha":"abc"},"base":{"ref":"main"}}]`)
	case strings.Contains(string(body), "addPullRequestReviewThreadReply"):
		r.posted = append(r.posted, string(body))
		return reply(`{"data":{"addPullRequestReviewThreadReply":{"comment":{"id":"C1"}}}}`)
	case strings.Contains(string(body), "node(id:$id)"):
		return reply(`{"data":{"node":{"pullRequest":{"number":7,"repository":{"nameWithOwner":"team/app"}}}}}`)
	case strings.Contains(string(body), "reviewThreads"):
		return reply(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[` + fmt.Sprintf(node, "PRRT_one") + `]}}}}}`)
	}
	t.Errorf("unexpected request %s %s", req.Method, req.URL)
	return nil, errors.New("unexpected request")
}

// run dispatches args as the command line does.
func (r *tasksAIRig) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	dispatch, rest, ok := command.Default.Dispatch(args)
	if !ok {
		t.Fatalf("agentflow %v is not a registered command", args)
	}
	_, inv := recording()
	var err error
	out := captureStdout(t, func() { err = dispatch(t.Context(), r.env, inv, rest) })
	return out, err
}

func (r *tasksAIRig) sessions() int {
	entries, err := os.ReadDir(filepath.Join(r.home, "ai", "sessions"))
	if err != nil {
		return 0
	}
	return len(entries)
}

func tasksAICode(t *testing.T, err error, code string) {
	t.Helper()
	var refusal *app.AIRefusal
	if !errors.As(err, &refusal) || refusal.Refusal.Code != code {
		t.Errorf("error %v, want the refusal %s", err, code)
	}
}

// --ai is for a person at a terminal, takes no --yes and none of the text
// it drafts: each is refused before anything is read or sent.
func TestTasksAIRefusals(t *testing.T) {
	r := newTasksAIRig(t, acptest.Draft(), config.Auto)
	usage := [][]string{
		{"task", "create", "--ai", "--description", "x", "--yes"},
		{"task", "create", "--ai", "--description", "x", "--summary", "s"},
		{"review", "reply", "--ai", "--yes"},
		{"review", "reply", "--ai", "PRRT_one", "the body"},
		{"review", "reply", "PRRT_one", "--ai", "--yes"},
	}
	for _, args := range usage {
		_, err := r.run(t, args...)
		tasksAICode(t, err, "usage")
	}
	needsPerson := [][]string{
		{"task", "create", "--ai", "--description", "x"},
		{"review", "reply", "--ai"},
		{"review", "reply", "--ai", "PRRT_one"},
	}
	for name, as := range map[string]func(*testing.T){
		"an agent":      func(t *testing.T) { t.Setenv("CLAUDECODE", "1") },
		"a session":     func(t *testing.T) { t.Setenv(app.SessionEnv, "ais_0123456789abcdef0123456789abcdef") },
		"no terminal":   func(t *testing.T) { ops.StdinIsTerminal = func() bool { return false } },
		"agent and yes": func(t *testing.T) { t.Setenv("CLAUDECODE", "1") },
	} {
		t.Run(name, func(t *testing.T) {
			old := ops.StdinIsTerminal
			t.Cleanup(func() { ops.StdinIsTerminal = old })
			as(t)
			for _, args := range needsPerson {
				if name == "agent and yes" && args[0] == "task" {
					args = append(args, "--yes")
				}
				_, err := r.run(t, args...)
				tasksAICode(t, err, "needs_person")
			}
		})
	}
	if r.sessions() != 0 || len(r.asked) != 0 || len(r.created) != 0 || len(r.posted) != 0 {
		t.Errorf("sessions %d, asked %v, created %v, posted %v", r.sessions(), r.asked, r.created, r.posted)
	}
}

// task create --ai: the draft is shown, "Use this draft?" asked, and only
// the drafted summary and description join the person's own flags.
func TestTaskCreateAI(t *testing.T) {
	r := newTasksAIRig(t, acptest.Draft(), config.Auto)
	args := []string{"task", "create", "--ai", "--description", "Add a greeting to the demo", "--project", "eng", "--type", "Bug", "--link-to", "PLAT-1", "--dry-run"}

	r.answer = "n"
	out, err := r.run(t, args...)
	if err == nil || !strings.Contains(err.Error(), "cancelled") || len(r.created) != 0 {
		t.Fatalf("declined: %v, created %v\n%s", err, r.created, out)
	}
	if len(r.asked) != 1 || r.asked[0] != "Use this draft? [y/N]: " || !strings.Contains(out, "  | Add a greeting line to the demo output.") {
		t.Errorf("asked %v:\n%s", r.asked, out)
	}

	r.answer = "y"
	if out, err = r.run(t, args...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := ops.TaskCreateCall{Summary: "Add a greeting to the demo", Description: "Add a greeting line to the demo output.",
		Project: "eng", Type: "Bug", LinkTo: "PLAT-1", DryRun: true}
	if len(r.created) != 1 || r.created[0] != want {
		t.Errorf("created %+v, want %+v", r.created, want)
	}
}

// A draft whose answer fails the checks is shown, and nothing is asked or
// created.
func TestTaskCreateAIInvalidDraft(t *testing.T) {
	r := newTasksAIRig(t, acptest.DraftInvalidAnswer(), config.Auto)
	out, err := r.run(t, "task", "create", "--ai", "--description", "x")
	if err == nil || len(r.asked) != 0 || len(r.created) != 0 {
		t.Fatalf("%v, asked %v, created %v", err, r.asked, r.created)
	}
	if !strings.Contains(out, "invalid") || !strings.Contains(out, "issue missing_field") || !strings.Contains(out, "The answer as it came back") {
		t.Errorf("output:\n%s", out)
	}
}

// The draft's own confirmation still holds: declined, nothing is sent.
func TestTaskCreateAIDraftDeclined(t *testing.T) {
	r := newTasksAIRig(t, acptest.Draft(), config.Confirm)
	in, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.WriteString("n\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	t.Cleanup(func() { os.Stdin = old; _ = in.Close() })
	_, err = r.run(t, "task", "create", "--ai", "--description", "x")
	if err == nil || !strings.Contains(err.Error(), "nothing was sent") || r.sessions() != 0 || len(r.created) != 0 {
		t.Errorf("%v, sessions %d", err, r.sessions())
	}
}

// review reply --ai: one question per reply; only a yes reaches review
// reply, which keeps its own confirmation.
func TestReviewReplyAI(t *testing.T) {
	r := newTasksAIRig(t, acptest.Draft(), config.Auto)

	r.answer = "n"
	out, err := r.run(t, "review", "reply", "--ai")
	if err != nil || len(r.posted) != 0 {
		t.Fatalf("declined: %v, posted %v\n%s", err, r.posted, out)
	}
	if len(r.asked) != 1 || r.asked[0] != "Send this reply? [y/N]: " || !strings.Contains(out, "Reply to a.go:3, alice") ||
		!strings.Contains(out, "  | Fixed in the latest commit.") || !strings.Contains(out, "Not sent.") {
		t.Errorf("asked %v:\n%s", r.asked, out)
	}

	// A yes to the draft, a no at the command's own confirmation.
	r.answer, r.env.Answer = "y", false
	if _, err = r.run(t, "review", "reply", "--ai", "PRRT_one"); err == nil || len(r.posted) != 0 {
		t.Fatalf("the gate was skipped: %v, posted %v", err, r.posted)
	}

	r.env.Answer = true
	if out, err = r.run(t, "review", "reply", "--ai", "PRRT_one"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(r.posted) != 1 || !strings.Contains(r.posted[0], `"id":"PRRT_one"`) || !strings.Contains(r.posted[0], `"body":"Fixed in the latest commit."`) {
		t.Errorf("posted %v", r.posted)
	}

	_, err = r.run(t, "review", "reply", "--ai", "PRRT_nope")
	tasksAICode(t, err, "unknown_thread")
}

// Without --ai, review reply behaves as before: confirm, then post.
func TestReviewReplyWithoutAI(t *testing.T) {
	r := newTasksAIRig(t, acptest.Draft(), config.Auto)
	r.env.Answer = false
	if _, err := r.run(t, "review", "reply", "PRRT_one", "Done."); err == nil || len(r.posted) != 0 {
		t.Fatalf("%v, posted %v", err, r.posted)
	}
	if _, err := r.run(t, "review", "reply", "PRRT_one", "Done.", "--yes"); err != nil || len(r.posted) != 1 {
		t.Fatalf("--yes: %v, posted %v", err, r.posted)
	}
	if _, err := r.run(t, "review", "reply", "PRRT_one"); err == nil || !strings.Contains(err.Error(), "usage: agentflow review reply") {
		t.Errorf("one argument: %v", err)
	}
	if len(r.asked) != 0 || r.sessions() != 0 {
		t.Errorf("asked %v, sessions %d", r.asked, r.sessions())
	}
}
