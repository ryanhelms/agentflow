package policy

import (
	"errors"
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

func TestPluginRedeemsOnlyItsOwnApprovals(t *testing.T) {
	dir := t.TempDir()
	person, err := NewGate(dir, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, "mcp:claude")
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"signal": "s"}
	d, err := person.Check(Remote, "notify", payload, "", false)
	if err != nil || d.Outcome != NeedsConfirmation {
		t.Fatal(d, err)
	}
	if _, err := person.Approve(d.Approval.OperationID, d.Token); err != nil {
		t.Fatal(err)
	}
	spy, err := NewGate(dir, config.Autonomy{Local: config.Auto, Remote: config.Auto}, "plugin:spy")
	if err != nil {
		t.Fatal(err)
	}
	spy = spy.WithCeiling(config.Confirm)
	if _, err := spy.Check(Remote, "notify", payload, d.Approval.OperationID, false); !errors.Is(err, ErrNotRequester) {
		t.Fatalf("a plugin redeemed another client's approval: %v", err)
	}
	if ap, err := spy.Status(d.Approval.OperationID); !errors.Is(err, ErrNotRequester) || ap.State != "" {
		t.Fatalf("a plugin read another client's approval: %+v %v", ap, err)
	}
	if ap, err := person.Status(d.Approval.OperationID); err != nil || ap.State == "" {
		t.Fatalf("the requester's status: %+v %v", ap, err)
	}
	// The person's resume still works.
	if ok, err := person.Check(Remote, "notify", payload, d.Approval.OperationID, false); err != nil || ok.Outcome != Proceed {
		t.Fatalf("the requester's resume: %+v %v", ok, err)
	}
	// A plugin may redeem the approval it requested itself.
	own, err := spy.Check(Remote, "notify", map[string]any{"signal": "own"}, "", false)
	if err != nil || own.Outcome != NeedsConfirmation {
		t.Fatal(own, err)
	}
	if _, err := spy.Approve(own.Approval.OperationID, own.Token); err != nil {
		t.Fatal(err)
	}
	if ok, err := spy.Check(Remote, "notify", map[string]any{"signal": "own"}, own.Approval.OperationID, false); err != nil || ok.Outcome != Proceed {
		t.Fatalf("own approval: %+v %v", ok, err)
	}
}
