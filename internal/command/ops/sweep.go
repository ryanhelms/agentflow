package ops

import (
	"context"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type sweepIn struct {
	AllRepos     bool   `json:"all_repos,omitempty" jsonschema:"every repository under worktree.root and in worktree.checkouts"`
	DryRun       bool   `json:"dry_run,omitempty" jsonschema:"report only; finish nothing"`
	StaleDays    int    `json:"stale_days,omitempty" jsonschema:"report worktrees with no commits for longer than this many days (default 7)"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"token from a needs_confirmation response, after the user agreed"`
}

// The sweep CLI stays in internal/cli for now: by default it prints a table after
// the confirmation, and the CLI environment's confirmation flow prints the
// final result as JSON itself.
func init() {
	command.Register(command.Spec[sweepIn, any]{
		Name:    "sweep",
		CLIPath: []string{"sweep"},
		MCP: &command.MCP{Tool: "agentflow_sweep", Description: "Finish every merged task worktree and report dirty, unpushed, stale and orphaned worktrees without deleting them. Use dry_run to report only.",
			Annotations: command.RemoteWrite},
		Policy: command.Policy{Class: policy.Remote, Action: "sweep"},
		// A sweep finishes the worktrees of the whole repository.
		Events: command.ScopeRepo,
		Token:  func(in *sweepIn) *string { return &in.ConfirmToken },
		DryRun: func(in sweepIn) bool { return in.DryRun },
		Describe: func(sweepIn) command.Detail {
			return command.Detail{Subject: "finish merged worktrees"}
		},
		Run: func(ctx context.Context, a *app.App, in sweepIn) (any, error) {
			res, pending, err := a.Sweep(ctx, app.SweepRequest{AllRepos: in.AllRepos, DryRun: in.DryRun, StaleDays: in.StaleDays}, in.ConfirmToken)
			if pending != nil {
				return pending, err
			}
			return res, err
		},
	})
}
