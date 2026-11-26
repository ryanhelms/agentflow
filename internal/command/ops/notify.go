package ops

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"strings"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type notifyIn struct {
	app.NotifyInput
	DryRun       bool   `json:"dry_run,omitempty"`
	ConfirmToken string `json:"confirm_token,omitempty"`

	// capture, when set by the CLI, receives the delivery result of each run.
	// The tool returns only the Pending of a gated run, but the CLI also
	// prints the planned deliveries of a run that policy blocked.
	capture *app.NotificationResult
}

const notifyUsage = "usage: agentflow notify SIGNAL --operation-id ID [--title T] [--body TEXT] [--links JSON] [--dry-run] [--automation] [--yes]"

var notifySpec = command.Spec[notifyIn, any]{
	Name:    "notify",
	CLIPath: []string{"notify"},
	MCP: &command.MCP{Tool: "agentflow_notify", Description: "Preview or deliver a configured notification. Supply a stable operation_id, and reuse it for retries.",
		Annotations: command.RemoteWrite, WriteHost: command.WriteHostSlack},
	Policy: command.Policy{Class: policy.Remote, Action: "notify"},
	Token:  func(in *notifyIn) *string { return &in.ConfirmToken },
	DryRun: func(in notifyIn) bool { return in.DryRun },
	Describe: func(in notifyIn) command.Detail {
		subject := "notify"
		if config.ValidNotificationName(in.Signal) {
			subject += " " + in.Signal
		}
		return command.Detail{Subject: subject}
	},
	Run: func(ctx context.Context, a *app.App, in notifyIn) (any, error) {
		in.NotifyInput.DryRun = in.DryRun
		result, pending, err := a.Notify(ctx, in.NotifyInput, in.ConfirmToken)
		if in.capture != nil {
			*in.capture = result
		}
		if pending != nil {
			return pending, err
		}
		return result, err
	},
}

func init() {
	command.Register(notifySpec)
	// A verb: the signal comes before the flags, as in the legacy command.
	command.RegisterVerb(command.Verb{
		Path:    []string{"notify"},
		Usage:   "  notify SIGNAL --operation-id ID [--title T] [--body TEXT] [--links JSON] [--dry-run] [--automation] [--yes]",
		Section: command.SectionWork,
		Order:   5400,
		Run:     runNotify,
	})
}

func runNotify(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	var in notifyIn
	flags := flag.NewFlagSet("notify SIGNAL", flag.ContinueOnError)
	flags.StringVar(&in.OperationID, "operation-id", "", "stable ID to reuse on replay")
	flags.StringVar(&in.Title, "title", "", "notification title")
	flags.StringVar(&in.Body, "body", "", "notification body (Slack supports mrkdwn)")
	flags.BoolVar(&in.Automation, "automation", false, "allow configured Slack automation webhooks")
	flags.BoolVar(&in.DryRun, "dry-run", false, "show the selected routes")
	links := flags.String("links", "", "notification links as a JSON array")
	yes := flags.Bool("yes", false, "accept the confirmation prompt")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		in.Signal, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if in.Signal == "" || flags.NArg() != 0 {
		return errors.New(notifyUsage)
	}
	if *links != "" {
		if err := json.Unmarshal([]byte(*links), &in.Links); err != nil {
			return errors.New("--links must be a JSON array of links")
		}
	}
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	origin := env.Origin(a)
	var outcome, result app.NotificationResult
	var blocked bool
	in.capture = &outcome
	if err := env.Confirm(*yes, func(token string) (any, error) {
		next := in
		next.ConfirmToken = token
		out, err := command.Invoke(ctx, inv, a, notifySpec, next, origin)
		pending, _ := out.(*app.Pending)
		if *yes && pending != nil && pending.Status == "needs_confirmation" {
			next.ConfirmToken = pending.ConfirmToken
			out, err = command.Invoke(ctx, inv, a, notifySpec, next, origin)
			pending, _ = out.(*app.Pending)
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
