package ops

import (
	"context"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// The push family: "push" is a Verb that prepares or applies, by its flags.
// push.prepare writes no audit line in its app method, so the Invoker writes
// one; push audits itself.
var (
	pushPrepareSpec = command.Spec[deliveryNoIn, appliedMap]{
		Name:    "push.prepare",
		CLIPath: []string{"push"},
		MCP: &command.MCP{Tool: "agentflow_push_prepare", Description: "Prepare the exact feature-branch push without publishing.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Remote, Action: "push", PrepareOnly: true, AuditByInvoker: true},
		Run:    func(ctx context.Context, a *app.App, _ deliveryNoIn) (any, error) { return a.PushPrepare(ctx) },
	}

	pushSpec = command.Spec[branchTokenIn, appliedMap]{
		Name:    "push",
		CLIPath: []string{"push"},
		MCP: &command.MCP{Tool: "agentflow_push", Description: "Publish the exact feature-branch head to the configured source remote.",
			Annotations: command.RemoteWrite},
		Policy: command.Policy{Class: policy.Remote, Action: "push"},
		Token:  func(in *branchTokenIn) *string { return &in.ConfirmToken },
		Run: func(ctx context.Context, a *app.App, in branchTokenIn) (any, error) {
			return appliedStatus("pushed")(a.Push(ctx, in.ConfirmToken))
		},
		Links: func(o appliedMap) []bus.OpLink {
			var links []bus.OpLink
			if branch, _ := o["branch"].(string); branch != "" {
				links = append(links, bus.OpLink{Kind: bus.OpLinkBranch, ID: branch})
			}
			if sha, _ := o["head_sha"].(string); sha != "" {
				links = append(links, bus.OpLink{Kind: bus.OpLinkCommit, ID: sha})
			}
			return links
		},
	}
)

func init() {
	command.Register(pushPrepareSpec)
	command.Register(pushSpec)
	command.RegisterVerb(branchWriteVerb([]string{"push"},
		"  push [--prepare | --confirm-token TOKEN] [--yes]   publish the current feature branch", 4100,
		pushPrepareSpec, pushSpec))
}
