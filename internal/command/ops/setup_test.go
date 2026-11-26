package ops_test

import (
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
)

func TestSetupApplyPublishesRefusals(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", confirmEverything)

	// Nothing selected: refused before any gate.
	em, out, isErr := r.tool(t, "setup.apply", map[string]any{})
	if !isErr || !strings.Contains(out, "select a project adapter") {
		t.Fatalf("empty setup apply: %v %s", isErr, out)
	}
	evs := wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	p := wantOpPayload(t, evs[0], "setup.apply", "setup.register_client", "local")
	if p.Subject != "register " {
		t.Errorf("subject %q", p.Subject)
	}
	if p, _ := commandtest.Payload(evs[1]); p.Code != "error" || strings.Contains(string(evs[1].Payload), "select a project") {
		t.Errorf("refusal payload %s", evs[1].Payload)
	}

	// A forged token for a real selection is refused by the gate, or finds
	// nothing to apply: either way the call is joined to its request.
	em, _, _ = r.tool(t, "setup.apply", map[string]any{"install_gh": true, "confirm_token": "cf_forged"})
	if got := em.Types(); len(got) != 2 || got[0] != bus.TypeOpRequested {
		t.Fatalf("events %v", got)
	}
	if p, _ := commandtest.Payload(em.Events()[0]); !p.Resumed || p.Subject != "install gh" {
		t.Errorf("request payload %+v", p)
	}
	noTokensInEvents(t, em)
}

func TestReadOperationsPublishNothing(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", "")
	for _, tc := range []struct {
		name string
		in   map[string]any
	}{
		{"setup.plan", map[string]any{}},
		{"setup.providers", map[string]any{"explain": true}},
		{"events", map[string]any{}},
	} {
		em, out, isErr := r.tool(t, tc.name, tc.in)
		if isErr {
			t.Fatalf("%s: %s", tc.name, out)
		}
		wantOpTypes(t, em)
	}
}

func TestDelegateRefusalsAndPreviewPublish(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", confirmEverything)

	// A repository with no checkout is refused.
	em, out, isErr := r.tool(t, "delegate", map[string]any{"repo": "other", "task": "fix the quota", "start": true})
	if !isErr || !strings.Contains(out, "no checkout of other") {
		t.Fatalf("delegate without a checkout: %v %s", isErr, out)
	}
	evs := wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	p := wantOpPayload(t, evs[0], "delegate", "delegate", "local")
	if p.Subject != "delegate to other" {
		t.Errorf("subject %q", p.Subject)
	}
	for _, ev := range evs {
		if strings.Contains(string(ev.Payload), "fix the quota") {
			t.Errorf("payload carries the task text: %s", ev.Payload)
		}
	}

	// A preview publishes nothing, even when it is refused.
	em, _, isErr = r.tool(t, "delegate", map[string]any{"repo": "other", "task": "fix the quota", "dry_run": true})
	if !isErr {
		t.Fatal("preview without a checkout succeeded")
	}
	wantOpTypes(t, em)
}
