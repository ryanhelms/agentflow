package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/worktree"
)

type worktreeGoWorkIn struct{}

type finishIn struct {
	Branch       string `json:"branch,omitempty" jsonschema:"branch to finish; default is the worktree the server runs in"`
	DryRun       bool   `json:"dry_run,omitempty" jsonschema:"report the steps without removing anything"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"token from a needs_confirmation response, after the user agreed"`
	// Yes is the CLI's --yes flag.
	Yes bool `json:"-"`
}

func init() {
	command.Register(command.Spec[worktreeGoWorkIn, any]{
		Name:    "worktree.go-work",
		CLIPath: []string{"worktree", "go-work"},
		CLI: &command.CLI[worktreeGoWorkIn]{
			Args: func(_ *worktreeGoWorkIn, pos []string) error {
				if len(pos) > 0 {
					return errors.New("usage: agentflow worktree go-work")
				}
				return nil
			},
			Usage:   "  worktree go-work               regenerate go.work in this worktree from worktree.go_work",
			Section: command.SectionWork,
			Order:   2400,
		},
		MCP: &command.MCP{Tool: "agentflow_worktree_go_work", Description: "Regenerate go.work in the current task worktree from worktree.go_work in .agentflow/config.yml.",
			Annotations: command.LocalWrite},
		// It writes one file in the worktree, with no gate and no audit line of
		// its own, so the Invoker audits it.
		Policy: command.Policy{Class: policy.Local, Action: "worktree_go_work", Gate: command.GateNone, AuditByInvoker: true},
		Describe: func(worktreeGoWorkIn) command.Detail {
			return command.Detail{Subject: "regenerate go.work"}
		},
		Run: func(ctx context.Context, a *app.App, _ worktreeGoWorkIn) (any, error) {
			return a.WorktreeGoWork(ctx)
		},
	})

	command.Register(command.Spec[finishIn, any]{
		Name:    "finish",
		CLIPath: []string{"finish"},
		CLI: &command.CLI[finishIn]{
			Flags: func(fs *flag.FlagSet, in *finishIn) {
				fs.BoolVar(&in.DryRun, "dry-run", false, "show what would be removed")
				fs.BoolVar(&in.Yes, "yes", false, "skip the confirmation prompt")
			},
			Args: func(in *finishIn, pos []string) error {
				if len(pos) > 1 {
					return errors.New("usage: agentflow finish [BRANCH] [--dry-run] [--yes]")
				}
				if len(pos) == 1 {
					in.Branch = pos[0]
				}
				return nil
			},
			Interspersed: true,
			Run: func(ctx context.Context, call command.CLICall[finishIn], in finishIn) error {
				var last any
				err := call.Env.Confirm(in.Yes, func(token string) (any, error) {
					next := in
					next.ConfirmToken = token
					out, err := call.Invoke(ctx, next)
					last = out
					return out, err
				})
				// A refusal prints what it checked, then fails.
				var refused *worktree.RefusedError
				if errors.As(err, &refused) {
					_ = call.Env.Print(refused.Outcome, nil)
				}
				if res, ok := last.(app.FinishResult); ok && err == nil && res.State == worktree.StateFinished {
					fmt.Fprintf(os.Stderr, "Primary checkout: %s\n", res.Primary)
				}
				return err
			},
			Usage:   "  finish [BRANCH] [--dry-run] [--yes]\n                                 after the PR merges: remove the worktree, the local and the remote branch",
			Section: command.SectionWork,
			Order:   2500,
		},
		MCP: &command.MCP{Tool: "agentflow_finish", Description: "After a task's PR is merged, remove its worktree, local branch and remote branch. Refuses with uncommitted or unpushed work or an unmerged PR. Safe to repeat.",
			Annotations: command.RemoteWrite},
		Policy: command.Policy{Class: policy.Remote, Action: "finish"},
		Token:  func(in *finishIn) *string { return &in.ConfirmToken },
		DryRun: func(in finishIn) bool { return in.DryRun },
		Describe: func(in finishIn) command.Detail {
			return command.Detail{Subject: "finish a merged task", Branch: in.Branch}
		},
		Run: func(ctx context.Context, a *app.App, in finishIn) (any, error) {
			return a.Finish(ctx, in.Branch, in.DryRun, in.ConfirmToken)
		},
	})
}
