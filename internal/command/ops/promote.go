package ops

import (
	"context"
	"flag"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type promoteIn struct {
	From         string `json:"from,omitempty"`
	To           string `json:"to,omitempty"`
	DryRun       bool   `json:"dry_run,omitempty"`
	ConfirmToken string `json:"confirm_token,omitempty"`
}

// promote has no tool: it is for people, and the method refuses to run inside
// an AI agent.
func init() {
	command.Register(command.Spec[promoteIn, any]{
		Name:    "promote",
		CLIPath: []string{"promote"},
		CLI: &command.CLI[promoteIn]{
			Flags: func(fs *flag.FlagSet, in *promoteIn) {
				fs.StringVar(&in.From, "from", "", "integration branch (default: origin's default branch, or develop)")
				fs.StringVar(&in.To, "to", "main", "release branch")
				fs.BoolVar(&in.DryRun, "dry-run", false, "show the PR without opening it")
				fs.Bool("yes", false, "skip the confirmation prompt")
			},
			// The legacy command ignored positional arguments.
			Args:    func(*promoteIn, []string) error { return nil },
			Confirm: true,
			Usage:   "  promote [--from develop] [--to main] [--dry-run] [--yes]\n                                 open the develop -> main release PR (merge commit); people only, not agents",
			Section: command.SectionWork,
			Order:   2800,
		},
		Policy: command.Policy{Class: policy.Remote, Action: "promote"},
		// A promotion belongs to the repository, not to the worktree that ran it.
		Events: command.ScopeRepo,
		Token:  func(in *promoteIn) *string { return &in.ConfirmToken },
		DryRun: func(in promoteIn) bool { return in.DryRun },
		Describe: func(in promoteIn) command.Detail {
			return command.Detail{Subject: "promote to " + in.To, Branch: in.To}
		},
		Run: func(ctx context.Context, a *app.App, in promoteIn) (any, error) {
			return a.Promote(ctx, in.From, in.To, in.DryRun, in.ConfirmToken, nil)
		},
	})
}
