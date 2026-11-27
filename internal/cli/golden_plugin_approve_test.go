package cli

import (
	"context"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// goldenPluginRequest holds a plugin's write for a person in the current
// repository, as the plugin host does, and returns its approval ID.
func goldenPluginRequest(t *testing.T, signal string) string {
	t.Helper()
	a, err := app.Open(context.Background(), ".", "plugin:ci-notify")
	if err != nil {
		t.Fatal(err)
	}
	a = a.WithAutonomyCeiling(config.Confirm)
	d, err := a.Gate.Check(policy.Remote, "notify", map[string]string{"signal": signal}, "", false)
	if err != nil || d.Approval == nil {
		t.Fatalf("plugin request: %+v %v", d, err)
	}
	if err := a.Gate.DescribePluginRequest(d.Approval.OperationID, policy.PluginRequest{Command: "notify", Subject: "notify " + signal}); err != nil {
		t.Fatal(err)
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

	other := goldenPluginRequest(t, "ci.recovered")
	g.check("plugin/approve_decline.txt", g.run("y\n", "plugin", "approve", other, "--decline"))
}
