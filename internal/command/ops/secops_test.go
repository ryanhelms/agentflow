package ops_test

import (
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
)

func TestSecOpsTriagePublishesOnlyWhenApplying(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", confirmEverything)
	quietStdout(t)

	// Without --apply the command previews tasks and publishes nothing.
	em, env, err := r.cli(t, false, "secops", "triage")
	if err != nil || strings.TrimSpace(env.Out.String()) != "[]" {
		t.Fatalf("preview: %v, output %q", err, env.Out.String())
	}
	wantOpTypes(t, em)

	// With --apply it writes, so it is an operation, and a repository whose
	// task provider cannot create the task refuses it.
	em, _, err = r.cli(t, false, "secops", "triage", "--apply", "--source", "github/acme/42")
	if err == nil {
		t.Fatal("apply without a task provider succeeded")
	}
	evs := wantOpTypes(t, em, bus.TypeOpRequested, bus.TypeOpFailed)
	wantOpPayload(t, evs[0], "secops.triage", "ticket_create", "remote")
	if p, _ := commandtest.Payload(evs[1]); p.Code != "error" || strings.Contains(string(evs[1].Payload), "provider") {
		t.Errorf("refusal payload %s", evs[1].Payload)
	}
}

func TestSecOpsTriageFlagErrorsMatchTheLegacyCommand(t *testing.T) {
	r := newOpsRepo(t, "agentflow: 1\n", "")
	const combos = "secops triage --apply requires --source and cannot be combined with --dry-run; --yes requires --apply"
	for _, args := range [][]string{
		{"secops", "triage", "--apply"},
		{"secops", "triage", "--apply", "--dry-run", "--source", "github/acme/42"},
		{"secops", "triage", "--yes"},
		{"secops", "triage", "extra"},
	} {
		if _, _, err := r.cli(t, false, args...); err == nil || err.Error() != combos {
			t.Errorf("agentflow %v: %v", args, err)
		}
	}
	if _, _, err := r.cli(t, false, "secops", "triage", "--source", "nope"); err == nil || !strings.HasPrefix(err.Error(), "invalid secops --source: ") {
		t.Errorf("bad source: %v", err)
	}
	if _, _, err := r.cli(t, false, "secops", "triage", "--since", "yesterday"); err == nil || !strings.HasPrefix(err.Error(), "invalid secops --since: ") {
		t.Errorf("bad cutoff: %v", err)
	}
}
