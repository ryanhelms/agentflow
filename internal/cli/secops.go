package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/nscaledev/agentflow/internal/app"
	"github.com/nscaledev/agentflow/internal/provider"
)

func runSecOps(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "triage" {
		return errors.New("usage: agentflow secops triage [--source REF] [--since RFC3339] [--apply] [--yes]")
	}
	in := app.SecOpsTriageInput{}
	fs := flag.NewFlagSet("secops triage", flag.ContinueOnError)
	source := fs.String("source", "", "qualified finding or incident reference")
	since := fs.String("since", "", "inclusive RFC3339 cutoff")
	fs.BoolVar(&in.Apply, "apply", false, "create a task for the selected source")
	dryRun := fs.Bool("dry-run", false, "preview tasks (default)")
	yes := fs.Bool("yes", false, "approve the selected task creation")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || (in.Apply && (*dryRun || *source == "")) || (*yes && !in.Apply) {
		return errors.New("secops triage --apply requires --source and cannot be combined with --dry-run; --yes requires --apply")
	}
	if *source != "" {
		ref, err := provider.ParseRef(*source)
		if err != nil {
			return fmt.Errorf("invalid secops --source: %w", err)
		}
		in.Source = ref
	}
	if *since != "" {
		cutoff, err := time.Parse(time.RFC3339, *since)
		if err != nil {
			return fmt.Errorf("invalid secops --since: %w", err)
		}
		in.Since = cutoff
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	return confirmLoop(*yes, func(token string) (any, error) {
		result, err := a.SecOpsTriage(ctx, in, token)
		if pending, ok := result.(*app.Pending); ok && pending != nil && pending.Status == "suggest" {
			return nil, errors.New(pending.Reason)
		}
		return result, err
	})
}
