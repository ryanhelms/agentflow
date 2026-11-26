package ops_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
)

func TestCommentPublishesConfirmApplyAndRefusal(t *testing.T) {
	f := newFixture(t)
	taskNoTerminal(t)
	em, inv := recording()
	env := &commandtest.Env{App: f.app}
	if err := taskRun(t, env, inv, "comment", "PLAT-7", "fixed the quota", "--yes"); err != nil {
		t.Fatal(err)
	}
	if f.posts != 1 || !strings.Contains(env.Out.String(), `"commented": true`) {
		t.Fatalf("posts %d, output %q", f.posts, env.Out.String())
	}
	// The comment is the message body: it is never in an event.
	payloads := checkConfirmed(t, em, "comment", "fixed the quota")
	if p := payloads[0]; p.Action != "comment" || p.Class != "remote" || p.Subject != "comment on a task" || p.Task != "PLAT-7" || p.Client != "cli" {
		t.Errorf("requested payload %+v", p)
	}

	// A forged token is refused by the gate: op.failed with its code only.
	em, inv = recording()
	in := map[string]any{"key": "PLAT-7", "text": "x", "confirm_token": "cf_forged"}
	if out, isErr := invokeTool(t, f.app, "comment", in, inv); !isErr {
		t.Fatalf("forged token accepted: %s", out)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("refusal events %v", got)
	}
	if p := taskPayloads(t, em)[1]; p.Code == "" || p.Code == "unexpected_result" {
		t.Errorf("refusal code %q", p.Code)
	}
	if f.posts != 1 {
		t.Errorf("a refused comment was posted: %d posts", f.posts)
	}

	// A "no" at the prompt shows only through approval.resolved: the CLI
	// publishes no op.failed for it.
	em, inv = recording()
	if err := taskRun(t, &commandtest.Env{App: f.app}, inv, "comment", "PLAT-7", "later"); err == nil {
		t.Fatal("declined prompt succeeded")
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpAwaitingApproval}) {
		t.Fatalf("declined events %v", got)
	}
}

func TestCommentRefusedByTheProvider(t *testing.T) {
	r := newTaskRepo(t, confirmAll)
	taskNoTerminal(t)
	key := r.createTask(t, "Review quotas", "")
	em, inv := recording()
	if err := taskRun(t, &commandtest.Env{App: r.app}, inv, "comment", key, "hello", "--yes"); err == nil {
		t.Fatal("a file task took a comment")
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("events %v", got)
	}
}

func TestCommentUnderASuggestPolicyPublishesTheSuggestion(t *testing.T) {
	f := newFixture(t)
	taskNoTerminal(t)
	f.app.Gate.Autonomy.Remote = "suggest"
	em, inv := recording()
	if err := taskRun(t, &commandtest.Env{App: f.app}, inv, "comment", "PLAT-7", "hello"); err != nil {
		t.Fatal(err)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpSuggested}) {
		t.Fatalf("events %v", got)
	}
	if f.posts != 0 {
		t.Errorf("a suggested comment was posted")
	}
}

func TestCommentAuditLineCountUnchanged(t *testing.T) {
	// Before: the app method alone, through the confirmation flow.
	f := newFixture(t)
	taskNoTerminal(t)
	pending, err := f.app.Comment(t.Context(), "PLAT-7", "note", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Comment(t.Context(), "PLAT-7", "note", pending.(*app.Pending).ConfirmToken); err != nil {
		t.Fatal(err)
	}
	direct := len(f.auditLines(t))
	// After: the same operation through the registry and the real Auditor.
	g := newFixture(t)
	_, inv := recording()
	if err := taskRun(t, &commandtest.Env{App: g.app}, inv, "comment", "PLAT-7", "note", "--yes"); err != nil {
		t.Fatal(err)
	}
	if got := len(g.auditLines(t)); got != direct || direct != 1 {
		t.Fatalf("audit lines: %d through the Invoker, %d direct; want 1 each", got, direct)
	}
}

func TestCommentCommandLineErrors(t *testing.T) {
	_, inv := recording()
	for _, args := range [][]string{{"comment"}, {"comment", "a", "b", "c"}} {
		err := taskRun(t, &commandtest.Env{}, inv, args...)
		if err == nil || !strings.Contains(err.Error(), "usage: agentflow comment [KEY] TEXT [--yes]") {
			t.Errorf("agentflow %v: %v", args, err)
		}
	}
}

func TestTaskLinkRefusedWithoutAProviderConnection(t *testing.T) {
	f := newFixture(t)
	taskNoTerminal(t)
	em, inv := recording()
	env := &commandtest.Env{App: f.app}
	err := taskRun(t, env, inv, "task", "link", "PLAT-7", "--url", "https://example.invalid/runbook?token=secret", "--yes")
	if err == nil || !strings.Contains(err.Error(), "provider MCP capability unavailable") {
		t.Fatalf("link without a connection: %v", err)
	}
	if got := em.Types(); !slices.Equal(got, []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}) {
		t.Fatalf("events %v", got)
	}
	payloads := taskPayloads(t, em)
	if p := payloads[0]; p.Operation != "task.link" || p.Action != "jira_remote_link" || p.Class != "remote" || p.Subject != "add a web link to PLAT-7" || p.Task != "PLAT-7" {
		t.Errorf("requested payload %+v", p)
	}
	// The URL can carry a secret: it is in no event.
	for _, ev := range em.Events() {
		if strings.Contains(string(ev.Payload), "secret") || strings.Contains(string(ev.Payload), "example.invalid") {
			t.Errorf("payload carries the URL: %s", ev.Payload)
		}
	}
}

func TestTaskLinkCommandLineErrors(t *testing.T) {
	_, inv := recording()
	for _, args := range [][]string{
		{"task", "link"},
		{"task", "link", "PLAT-7"},
		{"task", "link", "--url", "https://example.invalid"},
		{"task", "link", "A", "B", "--url", "https://example.invalid"},
	} {
		err := taskRun(t, &commandtest.Env{}, inv, args...)
		if err == nil || !strings.Contains(err.Error(), "usage: agentflow task link KEY --url URL [--title T] [--yes]") {
			t.Errorf("agentflow %v: %v", args, err)
		}
	}
}
