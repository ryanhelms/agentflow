package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// goldenPluginRequest holds a plugin's write for a person in the current
// repository, as the plugin host does, and returns its approval ID.
func goldenPluginRequest(t *testing.T, signal string) string {
	t.Helper()
	a, err := app.OpenPlugin(context.Background(), ".", "ci-notify")
	if err != nil {
		t.Fatal(err)
	}
	a = a.WithAutonomyCeiling(config.Confirm)
	d, err := a.Gate.Check(policy.Remote, "notify", map[string]string{"signal": signal}, "", false)
	if err != nil || d.Approval == nil {
		t.Fatalf("plugin request: %+v %v", d, err)
	}
	summary, _ := json.Marshal(map[string]any{
		"notification": map[string]string{"signal": signal, "title": "CI failed on feat", "body": "Failed checks: lint\nSee the run."},
		"deliveries":   []map[string]string{{"provider": "webhook", "destination": "audit", "status": "planned"}},
	})
	if err := a.Gate.DescribePluginRequest(d.Approval.OperationID, policy.PluginRequest{Command: "notify", Subject: "notify " + signal, Summary: summary}); err != nil {
		t.Fatal(err)
	}
	return d.Approval.OperationID
}

// goldenBarePluginRequest holds a plugin's write with req as its stored
// description, or none when req is nil.
func goldenBarePluginRequest(t *testing.T, action string, req *policy.PluginRequest) string {
	t.Helper()
	a, err := app.OpenPlugin(context.Background(), ".", "ci-notify")
	if err != nil {
		t.Fatal(err)
	}
	a = a.WithAutonomyCeiling(config.Confirm)
	d, err := a.Gate.Check(policy.Remote, action, map[string]string{"bare": action}, "", false)
	if err != nil || d.Approval == nil {
		t.Fatalf("plugin request: %+v %v", d, err)
	}
	if req != nil {
		if err := a.Gate.DescribePluginRequest(d.Approval.OperationID, *req); err != nil {
			t.Fatal(err)
		}
	}
	return d.Approval.OperationID
}

// TestGoldenPluginApprove pins what plugin approve prints: the summary and
// the result, a no, the refusals that keep it for a person at a terminal,
// and the approvals it refuses.
func TestGoldenPluginApprove(t *testing.T) {
	g := goldenConfigRepo(t)
	setTerminal := goldenPerson(t)
	id := goldenPluginRequest(t, "ci.failed")

	setTerminal(false)
	g.check("plugin/approve_refused_no_terminal.txt", g.run("y\n", "plugin", "approve", id, "--yes"))
	setTerminal(true)
	t.Setenv("CLAUDECODE", "1")
	g.check("plugin/approve_refused_agent.txt", g.run("y\n", "plugin", "approve", id, "--yes"))
	t.Setenv("CLAUDECODE", "")

	g.check("plugin/approve_unknown.txt", g.run("", "plugin", "approve", "op_0123456789abcdef0123456789abcdef"))
	agent, err := app.Open(context.Background(), ".", "mcp:claude")
	if err != nil {
		t.Fatal(err)
	}
	d, err := agent.Gate.Check(policy.Remote, "push", map[string]string{"branch": "feat"}, "", false)
	if err != nil || d.Approval == nil {
		t.Fatalf("agent request: %+v %v", d, err)
	}
	g.check("plugin/approve_not_plugin.txt", g.run("y\n", "plugin", "approve", d.Approval.OperationID))

	g.check("plugin/approve_declined_prompt.txt", g.run("n\n", "plugin", "approve", id))
	g.check("plugin/approve_yes_ignored.txt", g.run("n\n", "plugin", "approve", id, "--yes"))
	g.check("plugin/approve.txt", g.run("y\n", "plugin", "approve", id))
	g.check("plugin/approve_again.txt", g.run("y\n", "plugin", "approve", id))

	// What the host did not describe in full is never approved.
	g.check("plugin/approve_refused_undescribed.txt", g.run("y\n", "plugin", "approve", goldenBarePluginRequest(t, "push", nil)))
	g.check("plugin/approve_refused_not_a_plugin_write.txt", g.run("y\n", "plugin", "approve",
		goldenBarePluginRequest(t, "push", &policy.PluginRequest{Command: "push", Summary: json.RawMessage(`{}`)})))
	g.check("plugin/approve_refused_no_summary.txt", g.run("y\n", "plugin", "approve",
		goldenBarePluginRequest(t, "notify", &policy.PluginRequest{Command: "notify", Omitted: "the command returned no confirmation summary"})))

	other := goldenPluginRequest(t, "ci.recovered")
	g.check("plugin/approve_decline.txt", g.run("y\n", "plugin", "approve", other, "--decline"))
}
