package ops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
)

func TestADRDraftPublishesConfirmApplyAndRefusal(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", confirmEverything)
	quietStdout(t)

	// Confirm, then apply: the prompt asks, the second run writes the file.
	em, env, err := r.cli(t, true, "adr", "new", "Use", "queues")
	if err != nil {
		t.Fatal(err)
	}
	evs := wantOpConfirmFlow(t, em, "adr.draft", "adr_draft", "local")
	wantWorktreeScope(t, evs, false)
	if p, _ := commandtest.Payload(evs[0]); p.Subject != "draft ADR Use queues" {
		t.Errorf("subject %q", p.Subject)
	}
	noTokensInEvents(t, em)
	if _, err := os.Stat(filepath.Join(r.dir, "docs", "adr", "0001-use-queues.md")); err != nil {
		t.Fatalf("the ADR was not written: %v (%s)", err, env.Out.String())
	}
	// The method keeps its own audit line; the Invoker adds none.
	if lines := r.auditLines(t); len(lines) != 1 || lines[0]["action"] != "adr_draft" || lines[0]["operation"] != nil {
		t.Errorf("audit lines %v, want the method's one line", lines)
	}

	// A "no" at the prompt leaves it asking: the CLI publishes no op.failed.
	em, _, err = r.cli(t, false, "adr", "new", "Second")
	if err == nil {
		t.Fatal("declined prompt succeeded")
	}
	wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpAwaitingApproval)

	// A forged token is refused by the gate: op.failed carries a code only.
	em, out, isErr := r.tool(t, "adr.draft", map[string]any{"title": "Third", "confirm_token": "cf_forged"})
	if !isErr {
		t.Fatalf("forged token accepted: %s", out)
	}
	evs = wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	if p, _ := commandtest.Payload(evs[1]); p.Code == "" || strings.Contains(string(evs[1].Payload), "forged") {
		t.Errorf("refusal payload %s", evs[1].Payload)
	}
	if p, _ := commandtest.Payload(evs[0]); !p.Resumed {
		t.Errorf("a call with a token does not say it resumed: %+v", p)
	}
}

func TestADRDraftSuggestPublishesSuggested(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", "autonomy: {local: suggest}\n")
	quietStdout(t)
	em, _, err := r.cli(t, true, "adr", "new", "Only suggested")
	if err != nil {
		t.Fatal(err)
	}
	wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpSuggested)
	if _, err := os.Stat(filepath.Join(r.dir, "docs")); !os.IsNotExist(err) {
		t.Fatalf("a suggested ADR was written: %v", err)
	}
}

func TestDocsPublishConfirmsAndReadsPublishNothing(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\nknowledge_base: {provider: filesystem, settings: {root: records}}\n", confirmEverything)
	if err := os.Mkdir(filepath.Join(r.dir, "records"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout := quietStdout(t)
	const ref = "filesystem/engineering/quota"

	em, _, err := r.cli(t, false, "docs", "publish", ref, "--title", "Quota limits", "--body", "Details", "--operation-id", "doc-1", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	evs := wantOpConfirmFlow(t, em, "docs.publish", "docs_publish", "remote")
	wantWorktreeScope(t, evs, false)
	if p, _ := commandtest.Payload(evs[0]); p.Subject != "publish Quota limits" {
		t.Errorf("subject %q", p.Subject)
	}
	noTokensInEvents(t, em)
	for _, ev := range em.Events() {
		if strings.Contains(string(ev.Payload), "Details") {
			t.Errorf("payload carries the document body: %s", ev.Payload)
		}
	}
	if lines := r.auditLines(t); len(lines) != 1 || lines[0]["action"] != "docs_publish" {
		t.Errorf("audit lines %v, want the method's one line", lines)
	}

	// A refusal: a forged token through the tool.
	em, out, isErr := r.tool(t, "docs.publish", map[string]any{"ref": map[string]any{"instance": "filesystem", "scope": "engineering", "id": "quota"},
		"title": "Quota limits", "body": "More", "operation_id": "doc-2", "confirm_token": "cf_forged"})
	if !isErr {
		t.Fatalf("forged token accepted: %s", out)
	}
	wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)

	// Reads publish nothing, from the command line or the tool.
	em, _, err = r.cli(t, false, "docs", "get", ref)
	if err != nil || !strings.Contains(stdout(), "Quota limits") {
		t.Fatalf("docs get: %v, output %q", err, stdout())
	}
	wantOpTypes(t, em)
	em, _, err = r.cli(t, false, "docs", "search", "quota", "--space", "engineering")
	if err != nil {
		t.Fatal(err)
	}
	wantOpTypes(t, em)
	em, out, isErr = r.tool(t, "docs.search", map[string]any{"query": "quota", "space": "engineering"})
	if isErr || !strings.Contains(out, "Quota limits") {
		t.Fatalf("search tool: %v %s", isErr, out)
	}
	wantOpTypes(t, em)
}

func TestDocsUsageErrorsMatchTheLegacyCommand(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", "")
	for _, args := range [][]string{{"docs", "get"}, {"docs", "search"}, {"docs", "publish"}} {
		if _, _, err := r.cli(t, false, args...); err == nil || !strings.HasPrefix(err.Error(), "usage: agentflow docs get REF [--json] | agentflow docs search QUERY") {
			t.Errorf("agentflow %v: %v", args, err)
		}
	}
	if _, _, err := r.cli(t, false, "docs", "get", "--json"); err == nil || err.Error() != "usage: agentflow docs get REF [--json]" {
		t.Errorf("docs get without a reference: %v", err)
	}
	if _, _, err := r.cli(t, false, "docs", "publish", "filesystem/a/b", "--title", "T"); err == nil || err.Error() != "--body is required" {
		t.Errorf("docs publish without a body: %v", err)
	}
	if _, _, err := r.cli(t, false, "adr", "new"); err == nil || err.Error() != "usage: agentflow adr new TITLE" {
		t.Errorf("adr new without a title: %v", err)
	}
}
