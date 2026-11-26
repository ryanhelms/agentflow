package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/provider"
)

type secopsTriageIn struct {
	app.SecOpsTriageInput
	ConfirmToken string `json:"confirm_token,omitempty"`

	// The command-line flags, which the CLI turns into SecOpsTriageInput.
	source, since string
	dryRun, yes   bool
}

// secops triage has no tool. Without --apply it only previews tasks, which
// publishes nothing.
func init() {
	command.Register(command.Spec[secopsTriageIn, any]{
		Name:    "secops.triage",
		CLIPath: []string{"secops", "triage"},
		CLI: &command.CLI[secopsTriageIn]{
			Flags: func(fs *flag.FlagSet, in *secopsTriageIn) {
				fs.StringVar(&in.source, "source", "", "qualified finding or incident reference")
				fs.StringVar(&in.since, "since", "", "inclusive RFC3339 cutoff")
				fs.BoolVar(&in.Apply, "apply", false, "create a task for the selected source")
				fs.BoolVar(&in.dryRun, "dry-run", false, "preview tasks (default)")
				fs.BoolVar(&in.yes, "yes", false, "approve the selected task creation")
			},
			Args: func(in *secopsTriageIn, pos []string) error {
				if len(pos) != 0 || (in.Apply && (in.dryRun || in.source == "")) || (in.yes && !in.Apply) {
					return errors.New("secops triage --apply requires --source and cannot be combined with --dry-run; --yes requires --apply")
				}
				if in.source != "" {
					ref, err := provider.ParseRef(in.source)
					if err != nil {
						return fmt.Errorf("invalid secops --source: %w", err)
					}
					in.Source = ref
				}
				if in.since != "" {
					cutoff, err := time.Parse(time.RFC3339, in.since)
					if err != nil {
						return fmt.Errorf("invalid secops --since: %w", err)
					}
					in.Since = cutoff
				}
				return nil
			},
			Run: func(ctx context.Context, call command.CLICall[secopsTriageIn], in secopsTriageIn) error {
				return call.Env.Confirm(in.yes, func(token string) (any, error) {
					next := in
					next.ConfirmToken = token
					result, err := call.Invoke(ctx, next)
					if pending, ok := result.(*app.Pending); ok && pending != nil && pending.Status == "suggest" {
						return nil, errors.New(pending.Reason)
					}
					return result, err
				})
			},
			Usage:   "  secops triage [--source REF] [--since RFC3339] [--apply] [--yes]\n                                 preview or create tasks from configured security events",
			Section: command.SectionWork,
			Order:   4600,
		},
		Policy: command.Policy{Class: policy.Remote, Action: "ticket_create"},
		Token:  func(in *secopsTriageIn) *string { return &in.ConfirmToken },
		// Without --apply the command previews tasks and writes nothing.
		DryRun: func(in secopsTriageIn) bool { return !in.Apply },
		Describe: func(secopsTriageIn) command.Detail {
			return command.Detail{Subject: "create a task from a security event"}
		},
		Run: func(ctx context.Context, a *app.App, in secopsTriageIn) (any, error) {
			return a.SecOpsTriage(ctx, in.SecOpsTriageInput, in.ConfirmToken)
		},
	})
}
