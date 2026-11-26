package policy

import (
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
)

func TestWithCeilingCapsEveryLevel(t *testing.T) {
	g, err := NewGate(t.TempDir(), config.Autonomy{Local: config.Auto, Remote: config.Auto}, "plugin:x")
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Explain(Remote, "notify", nil, false).Level; got != config.Auto {
		t.Fatalf("without a ceiling: %s", got)
	}
	capped := g.WithCeiling(config.Confirm)
	d := capped.Explain(Remote, "notify", nil, false)
	if d.Level != config.Confirm || d.Source != CeilingSource {
		t.Fatalf("auto under a confirm ceiling: %+v", d)
	}
	if got := g.Explain(Remote, "notify", nil, false).Level; got != config.Auto {
		t.Fatalf("WithCeiling changed the original gate: %s", got)
	}
	// A write without a token waits for a person instead of proceeding.
	dec, err := capped.Check(Remote, "notify", map[string]any{"a": 1}, "", false)
	if err != nil || dec.Outcome != NeedsConfirmation {
		t.Fatalf("auto write under ceiling: %+v %v", dec, err)
	}
	// The approval ID alone never proceeds before a person approved it.
	if _, err := capped.Check(Remote, "notify", map[string]any{"a": 1}, dec.Approval.OperationID, false); ErrorCode(err) != "approval_pending" {
		t.Fatalf("resume before approval: %v", err)
	}
	if _, err := g.Approve(dec.Approval.OperationID, dec.Token); err != nil {
		t.Fatal(err)
	}
	ok, err := capped.Check(Remote, "notify", map[string]any{"a": 1}, dec.Approval.OperationID, false)
	if err != nil || ok.Outcome != Proceed {
		t.Fatalf("resume after approval: %+v %v", ok, err)
	}

	suggest, err := NewGate(t.TempDir(), config.Autonomy{Local: config.Suggest, Remote: config.Suggest}, "plugin:x")
	if err != nil {
		t.Fatal(err)
	}
	if got := suggest.WithCeiling(config.Confirm).Explain(Local, "commit", nil, false).Level; got != config.Suggest {
		t.Fatalf("suggest must stay suggest: %s", got)
	}
	// A ceiling never loosens, and an unknown one is the strictest.
	if got := capped.WithCeiling(config.Auto).Explain(Remote, "notify", nil, false).Level; got != config.Confirm {
		t.Fatalf("a second, looser ceiling loosened: %s", got)
	}
	if got := g.WithCeiling("bogus").Explain(Remote, "notify", nil, false).Level; got != config.Suggest {
		t.Fatalf("unknown ceiling: %s", got)
	}
}
