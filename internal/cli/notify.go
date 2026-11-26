package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"strings"

	"github.com/ryanhelms/agentflow/internal/app"
)

func runNotify(ctx context.Context, args []string) error {
	var input app.NotifyInput
	flags := flag.NewFlagSet("notify SIGNAL", flag.ContinueOnError)
	flags.StringVar(&input.OperationID, "operation-id", "", "stable ID to reuse on replay")
	flags.StringVar(&input.Title, "title", "", "notification title")
	flags.StringVar(&input.Body, "body", "", "notification body (Slack supports mrkdwn)")
	flags.BoolVar(&input.Automation, "automation", false, "allow configured Slack automation webhooks")
	flags.BoolVar(&input.DryRun, "dry-run", false, "show the selected routes")
	links := flags.String("links", "", "notification links as a JSON array")
	yes := flags.Bool("yes", false, "accept the confirmation prompt")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		input.Signal, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if input.Signal == "" || flags.NArg() != 0 {
		return errors.New("usage: agentflow notify SIGNAL --operation-id ID [--title T] [--body TEXT] [--links JSON] [--dry-run] [--automation] [--yes]")
	}
	if *links != "" {
		if err := json.Unmarshal([]byte(*links), &input.Links); err != nil {
			return errors.New("--links must be a JSON array of links")
		}
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	var result app.NotificationResult
	var blocked bool
	if err := confirmLoop(*yes, func(token string) (any, error) {
		outcome, pending, err := a.Notify(ctx, input, token)
		if *yes && pending != nil && pending.Status == "needs_confirmation" {
			outcome, pending, err = a.Notify(ctx, input, pending.ConfirmToken)
		}
		if pending != nil {
			if pending.Status != "suggest" {
				return pending, err
			}
			outcome.Reason, blocked = pending.Reason, true
		}
		result = outcome
		return result, err
	}); err != nil {
		return err
	}
	if blocked {
		return errors.New("notifications are blocked by remote autonomy")
	}
	if result.HasFailures() {
		return errors.New("one or more notification deliveries need attention")
	}
	return nil
}
