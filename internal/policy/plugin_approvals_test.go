package policy

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
)

func pluginGate(t *testing.T, dir string) *Gate {
	t.Helper()
	g, err := NewPluginGate(dir, config.Autonomy{Local: config.Auto, Remote: config.Auto}, "ci-notify")
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
	summary := json.RawMessage(`{"notification":{"title":"CI failed\u202e","body":"line one\nline two\u0007"},"confirm_token":"cf_x","deliveries":[{"webhook_url":"https://hooks.example/x"}]}`)
	if err := plug.DescribePluginRequest(id, PluginRequest{Plugin: "forged", Command: "notify", Subject: "notify ci.failed\u202e\x1b[31m\nsecond", Branch: "feat", Summary: summary}); err != nil {
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
	r := got.Request
	if err != nil || got.State != pending || got.RequestedBy != "plugin:ci-notify" || got.ExpiresAt.IsZero() ||
		r.Plugin != "ci-notify" || r.Command != "notify" || r.Subject != "notify ci.failed[31m" || r.Branch != "feat" {
		t.Fatalf("request: %+v %v", got, err)
	}
	// The full summary, cleaned: lines kept, control and bidi characters
	// gone, secrets redacted.
	wantSummary := `{"confirm_token":"(redacted)","deliveries":[{"webhook_url":"(redacted)"}],"notification":{"body":"line one\nline two","title":"CI failed"}}`
	if string(r.Summary) != wantSummary {
		t.Fatalf("summary %s", r.Summary)
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
	// No caller may name itself a plugin: only NewPluginGate makes one.
	for _, c := range []string{"plugin:ci-notify", "plugin:"} {
		if _, err := NewGate(dir, config.Autonomy{}, c); !errors.Is(err, ErrReservedClient) {
			t.Fatalf("NewGate(%q): %v", c, err)
		}
	}
	for _, id := range []string{"", "a:b", "a b"} {
		if _, err := NewPluginGate(dir, config.Autonomy{}, id); err == nil {
			t.Fatalf("NewPluginGate(%q) accepted", id)
		}
	}
	// A plugin record the host never described cannot be approved, only
	// declined: whoever asked, the person sees nothing of what would run.
	undescribed := pluginGate(t, dir)
	u, err := undescribed.Check(Remote, "push", map[string]any{"b": "forged"}, "", false)
	if err != nil || u.Approval == nil {
		t.Fatal(u, err)
	}
	if got, err := cli.PluginRequestOf(u.Approval.OperationID); err != nil || got.Request.Command != "" || got.Request.Plugin != "ci-notify" {
		t.Fatalf("undescribed request: %+v %v", got, err)
	}
	if _, err := cli.ApprovePluginRequest(u.Approval.OperationID); !errors.Is(err, ErrNoHostRequest) {
		t.Fatalf("approved an undescribed request: %v", err)
	}
	// A described request for a command no plugin may write, or whose
	// gate action is another command, cannot be approved either.
	for i, tc := range []struct{ action, command string }{{"push", "push"}, {"push", "notify"}, {"notify", "comment"}} {
		w, err := undescribed.Check(Remote, tc.action, map[string]any{"i": i}, "", false)
		if err != nil || w.Approval == nil {
			t.Fatal(w, err)
		}
		if err := undescribed.DescribePluginRequest(w.Approval.OperationID, PluginRequest{Command: tc.command, Summary: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := cli.ApprovePluginRequest(w.Approval.OperationID); !errors.Is(err, ErrNotPluginWrite) {
			t.Fatalf("approved %s described as %s: %v", tc.action, tc.command, err)
		}
	}
	if _, err := cli.DeclinePluginRequest(u.Approval.OperationID); err != nil {
		t.Fatalf("decline an undescribed request: %v", err)
	}
	// A request with no summary cannot be approved, only declined.
	bare := pluginGate(t, dir)
	b, err := bare.Check(Remote, "notify", map[string]any{"signal": "bare"}, "", false)
	if err != nil || b.Approval == nil {
		t.Fatal(b, err)
	}
	if err := bare.DescribePluginRequest(b.Approval.OperationID, PluginRequest{Command: "notify"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ApprovePluginRequest(b.Approval.OperationID); !errors.Is(err, ErrNoRequestSummary) {
		t.Fatalf("approved a request without a summary: %v", err)
	}
	if _, err := cli.DeclinePluginRequest(b.Approval.OperationID); err != nil {
		t.Fatalf("decline without a summary: %v", err)
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
	if err := plug.DescribePluginRequest(p.Approval.OperationID, PluginRequest{Command: "notify", Summary: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(48 * time.Hour)
	cli.now = func() time.Time { return later }
	if _, err := cli.ApprovePluginRequest(p.Approval.OperationID); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired request: %v", err)
	}
}
