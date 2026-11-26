package ops

import (
	"context"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

var shipStatusSpec = command.Spec[deliveryNoIn, any]{
	Name:    "ship.status",
	CLIPath: []string{"status"},
	MCP: &command.MCP{Tool: "agentflow_ship_status", Description: "PR, checks, review threads, deployments and ticket status in one view.",
		Annotations: command.ReadOnly},
	Policy: command.Policy{Class: policy.Read},
	Run:    func(ctx context.Context, a *app.App, _ deliveryNoIn) (any, error) { return a.ShipStatus(ctx) },
}

func init() {
	command.Register(shipStatusSpec)
	// A Verb, not a Spec's CLI: the legacy command ignores its arguments, and
	// a Spec's CLI would refuse them.
	command.RegisterVerb(command.Verb{
		Path:    []string{"status"},
		Usage:   "  status                         PR, checks, deployments and ticket in one view",
		Section: command.SectionWork,
		Order:   5000,
		Run: func(ctx context.Context, env command.CLIEnv, inv command.Invoker, _ []string) error {
			a, err := env.Open(ctx)
			if err != nil {
				return err
			}
			return env.Print(command.Invoke(ctx, inv, a, shipStatusSpec, deliveryNoIn{}, env.Origin(a)))
		},
	})
}
