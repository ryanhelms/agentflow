package policy

import (
	"errors"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
)

func pluginGate(t *testing.T, dir string) *Gate {
	t.Helper()
	g, err := NewGate(dir, config.Autonomy{Local: config.Auto, Remote: config.Auto}, "plugin:ci-notify")
	if err != nil {
		t.Fatal(err)
	}
	return g.WithCeiling(config.Confirm)
}

func TestPersonApprovesAPluginRequestAndThePluginResumes(t *testing.T) {
	dir := t.TempDir()
	plug := pluginGate(t, dir)
	payload := map[string]any{"signal": "ci.failed"}
	d, err := plug.Check(Remote, "notify", payload, "", false)
	if err != nil || d.Approval == nil || d.Approval.State != pending {
		t.Fatalf("plugin request: %+v %v", d, err)
	}
	id := d.Approval.OperationID
	if err := plug.DescribePluginRequest(id, PluginRequest{Plugin: "forged", Command: "notify", Subject: "notify ci.failed\u202e\x1b[31m\nsecond", Branch: "feat"}); err != nil {
		t.Fatal(err)
	}
	// Only the first description sticks.
	if err := plug.DescribePluginRequest(id, PluginRequest{Command: "comment"}); err != nil {
		t.Fatal(err)
	}
	person, err := NewGate(dir, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := person.DescribePluginRequest(id, PluginRequest{Command: "x"}); !errors.Is(err, ErrNotRequester) {
		t.Fatalf("a person's gate described a plugin request: %v", err)
	}
	got, err := person.PluginRequestOf(id)
	want := PluginRequest{Plugin: "ci-notify", Command: "notify", Subject: "notify ci.failed[31m", Branch: "feat"}
	if err != nil || got.State != pending || got.RequestedBy != "plugin:ci-notify" || got.Request != want || got.ExpiresAt.IsZero() {
		t.Fatalf("request: %+v %v", got, err)
	}
	// The plugin cannot approve its own request.
	if _, err := plug.ApprovePluginRequest(id); !errors.Is(err, ErrNotRequester) {
		t.Fatalf("a plugin approved: %v", err)
	}
	// Before approval the plugin's resume waits.
	if _, err := plug.Check(Remote, "notify", payload, id, false); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("resume before approval: %v", err)
	}
	if ap, err := person.ApprovePluginRequest(id); err != nil || ap.State != approved {
		t.Fatalf("approve: %+v %v", ap, err)
	}
	if _, err := person.ApprovePluginRequest(id); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second approve: %v", err)
	}
	if ok, err := plug.Check(Remote, "notify", payload, id, false); err != nil || ok.Outcome != Proceed {
		t.Fatalf("resume after approval: %+v %v", ok, err)
	}
	// Once: the record is consumed.
	if _, err := plug.Check(Remote, "notify", payload, id, false); !errors.Is(err, ErrConsumed) {
		t.Fatalf("second resume: %v", err)
	}
}

func TestPluginApproveRefusesOtherRecords(t *testing.T) {
	dir := t.TempDir()
	person, err := NewGate(dir, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, "mcp:claude")
	if err != nil {
		t.Fatal(err)
	}
	d, err := person.Check(Remote, "push", map[string]any{"b": "x"}, "", false)
	if err != nil || d.Approval == nil {
		t.Fatal(d, err)
	}
	cli, err := NewGate(dir, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ApprovePluginRequest(d.Approval.OperationID); !errors.Is(err, ErrNotPluginRequest) {
		t.Fatalf("approved an agent's record: %v", err)
	}
	if _, err := cli.ApprovePluginRequest("op_" + "0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrNoPluginRequest) {
		t.Fatalf("unknown ID: %v", err)
	}
	if _, err := cli.ApprovePluginRequest("nope"); err == nil {
		t.Fatal("a malformed ID was accepted")
	}
	// A declined request is cancelled: its resume fails.
	plug := pluginGate(t, dir)
	q, err := plug.Check(Remote, "notify", map[string]any{"signal": "declined"}, "", false)
	if err != nil || q.Approval == nil {
		t.Fatal(q, err)
	}
	if ap, err := cli.DeclinePluginRequest(q.Approval.OperationID); err != nil || ap.State != cancelled {
		t.Fatalf("decline: %+v %v", ap, err)
	}
	if _, err := plug.Check(Remote, "notify", map[string]any{"signal": "declined"}, q.Approval.OperationID, false); !errors.Is(err, ErrCancelled) {
		t.Fatalf("resume after decline: %v", err)
	}
	// An expired plugin request cannot be approved.
	p, err := plug.Check(Remote, "notify", map[string]any{"signal": "s"}, "", false)
	if err != nil || p.Approval == nil {
		t.Fatal(p, err)
	}
	later := time.Now().Add(48 * time.Hour)
	cli.now = func() time.Time { return later }
	if _, err := cli.ApprovePluginRequest(p.Approval.OperationID); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired request: %v", err)
	}
}
