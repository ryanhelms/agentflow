package ops

import (
	"context"
	"errors"
	"flag"
	"net/url"
	"os"
	"strconv"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/event"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/provider"
)

type transitionIn struct {
	Key          string `json:"key,omitempty" jsonschema:"task key or qualified reference; defaults to the branch task"`
	Event        string `json:"event" jsonschema:"branch.created, pr.opened, pr.merged, deploy.staging, deploy.production, or a verb like #qa"`
	DryRun       bool   `json:"dry_run,omitempty"`
	ConfirmToken string `json:"confirm_token,omitempty"`

	// Command line only: the CI modes and --yes.
	githubEvent, shipped, yes bool
}

func init() {
	command.Register(command.Spec[transitionIn, any]{
		Name:    "transition",
		CLIPath: []string{"transition"},
		CLI: &command.CLI[transitionIn]{
			Flags: func(fs *flag.FlagSet, in *transitionIn) {
				fs.BoolVar(&in.DryRun, "dry-run", false, "preview only")
				fs.BoolVar(&in.githubEvent, "github-event", false, "process the current GitHub Actions lifecycle event")
				fs.BoolVar(&in.shipped, "shipped", false, "preview a shipped task with the runner identity")
				fs.BoolVar(&in.yes, "yes", false, "skip the confirmation prompt")
			},
			Args: func(in *transitionIn, args []string) error {
				if in.githubEvent {
					if len(args) != 0 || in.yes || in.shipped {
						return errors.New("--github-event cannot be combined with a key, event, or --yes")
					}
					return nil
				}
				switch len(args) {
				case 1:
					in.Event = args[0]
				case 2:
					in.Key, in.Event = args[0], args[1]
				default:
					return errors.New("usage: agentflow transition [KEY] EVENT")
				}
				return nil
			},
			Interspersed: true,
			Run:          runTransition,
			Usage:        "  transition [KEY] EVENT [--dry-run] [--yes]      e.g. pr.merged or \"#qa\"",
			Section:      command.SectionWork,
			Order:        4500,
		},
		MCP: &command.MCP{Tool: "agentflow_transition", Description: "Move a task by configured event or verb. Use dry_run to preview. A pending_review result requires a PR merge.",
			Annotations: command.RemoteWrite, WriteHost: command.WriteHostTasks},
		Policy: command.Policy{Class: policy.Remote, Action: "transition"},
		Token:  func(in *transitionIn) *string { return &in.ConfirmToken },
		DryRun: func(in transitionIn) bool { return in.DryRun },
		Describe: func(in transitionIn) command.Detail {
			return command.Detail{Subject: "transition on " + in.Event, Task: in.Key}
		},
		Run: func(ctx context.Context, a *app.App, in transitionIn) (any, error) {
			return a.Transition(ctx, in.Key, in.Event, in.DryRun, in.ConfirmToken)
		},
	})
}

// runTransition is `agentflow transition`. Besides the operation it runs two
// modes for CI that have no tool: --github-event applies the lifecycle event
// of the current GitHub Actions run, and --shipped previews a shipped task. Both
// call their app method directly, as the legacy command did, and publish
// no op events.
func runTransition(ctx context.Context, call command.CLICall[transitionIn], in transitionIn) error {
	a := call.App
	if in.githubEvent {
		return transitionGitHubEvent(ctx, call, in.DryRun)
	}
	if in.shipped {
		if !in.DryRun || in.yes || in.Key == "" {
			return errors.New("--shipped requires an explicit key, event and --dry-run")
		}
		return call.Env.Print(a.PlanShippedTask(ctx, in.Key, in.Event))
	}
	return call.Env.Confirm(in.yes, func(token string) (any, error) {
		next := in
		next.ConfirmToken = token
		return call.Invoke(ctx, next)
	})
}

// transitionGitHubEvent applies the lifecycle event of the current GitHub
// Actions run, described by the runner's environment.
func transitionGitHubEvent(ctx context.Context, call command.CLICall[transitionIn], dryRun bool) error {
	repoID, err := strconv.ParseInt(os.Getenv("GITHUB_REPOSITORY_ID"), 10, 64)
	if err != nil || repoID <= 0 {
		return errors.New("github task events require GITHUB_REPOSITORY_ID")
	}
	host := "github.com"
	if server := os.Getenv("GITHUB_SERVER_URL"); server != "" {
		u, err := url.Parse(server)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("expected an HTTPS GITHUB_SERVER_URL")
		}
		host = u.Host
	}
	metadata := event.GitHubActionsContext{
		Repository:    provider.RepositoryRef{Host: host, Path: os.Getenv("GITHUB_REPOSITORY")},
		RepositoryID:  repoID,
		DefaultBranch: os.Getenv("AGENTFLOW_DEFAULT_BRANCH"),
		EventName:     os.Getenv("GITHUB_EVENT_NAME"),
		EventID:       os.Getenv("GITHUB_RUN_ID"),
		ApprovedIssue: os.Getenv("AGENTFLOW_APPROVED_ISSUE"),
	}
	return call.Env.Print(call.App.ApplyGitHubTaskEvent(ctx, metadata, os.Getenv("GITHUB_EVENT_PATH"), dryRun))
}
