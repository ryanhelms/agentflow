package ops

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type releasePlanIn struct {
	Unit string `json:"unit,omitempty" jsonschema:"unit name from .agentflow/releases.yml; empty for all units"`
	// JSON is the CLI's --json flag; the tool always returns JSON.
	JSON bool `json:"-"`
}

type releasePreviewIn struct {
	Since string `json:"since,omitempty" jsonschema:"tag or ref; default is the latest tag"`
}

type releasePublishIn struct {
	app.ReleasePublishInput
	ConfirmToken string `json:"confirm_token,omitempty"`
}

const releasePublishUsage = "usage: agentflow release publish TAG --revision SHA [--title T] [--notes TEXT] [--dry-run] [--yes]"

// releaseFlags declares the flags of the legacy release command, which parsed
// one set for every subcommand: a subcommand accepts the flags of the others
// and ignores them.
func releaseFlags(fs *flag.FlagSet, unit, since *string, asJSON *bool) {
	var ignoredStrategy, ignoredUnit, ignoredSince string
	var ignoredForce, ignoredJSON bool
	if unit == nil {
		unit = &ignoredUnit
	}
	if since == nil {
		since = &ignoredSince
	}
	if asJSON == nil {
		asJSON = &ignoredJSON
	}
	fs.StringVar(&ignoredStrategy, "strategy", "", "branching strategy: trunk or gitflow (default: detect)")
	fs.BoolVar(&ignoredForce, "force", false, "re-detect units even if releases.yml exists")
	fs.StringVar(unit, "unit", "", "unit name from .agentflow/releases.yml")
	fs.BoolVar(asJSON, "json", false, "print JSON")
	fs.StringVar(since, "since", "", "tag or ref (preview)")
}

var (
	releasePlanSpec = command.Spec[releasePlanIn, any]{
		Name:    "release.plan",
		CLIPath: []string{"release", "plan"},
		CLI: &command.CLI[releasePlanIn]{
			Flags: func(fs *flag.FlagSet, in *releasePlanIn) { releaseFlags(fs, &in.Unit, nil, &in.JSON) },
			// The legacy command ignored positional arguments.
			Args: func(*releasePlanIn, []string) error { return nil },
			Run: func(ctx context.Context, call command.CLICall[releasePlanIn], in releasePlanIn) error {
				out, err := call.Invoke(ctx, in)
				if err != nil {
					return err
				}
				plans, _ := out.([]app.UnitPlan)
				if in.JSON {
					return call.Env.Print(plans, nil)
				}
				for _, p := range plans {
					mark := " "
					if p.WillRelease {
						mark = "↑"
					}
					fmt.Printf("%s %-16s %-8s → %-8s (%s, %d commits) build version %s\n", mark, p.Unit.Name, p.Current, p.Next, p.Bump, p.Commits, p.Version)
				}
				return nil
			},
			Usage:   "  release plan [--unit U] [--json]   next version of each unit on this branch",
			Section: command.SectionWork,
			Order:   6100,
		},
		MCP: &command.MCP{Tool: "agentflow_release_plan", Description: "Each deployable unit's current version, the bump implied by its commits, the next release version and the prerelease version this branch builds as.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in releasePlanIn) (any, error) {
			return a.ReleasePlan(ctx, in.Unit)
		},
	}

	releasePreviewSpec = command.Spec[releasePreviewIn, any]{
		Name:    "release.preview",
		CLIPath: []string{"release", "preview"},
		CLI: &command.CLI[releasePreviewIn]{
			Flags: func(fs *flag.FlagSet, in *releasePreviewIn) { releaseFlags(fs, nil, &in.Since, nil) },
			Args:  func(*releasePreviewIn, []string) error { return nil },
			// The result prints as JSON, as the legacy command did.
			Usage:   "  release preview [--since TAG]  unreleased commits by type",
			Section: command.SectionWork,
			Order:   6300,
		},
		MCP: &command.MCP{Tool: "agentflow_release_preview", Description: "Unreleased commits grouped by type with the implied version bump and Jira keys.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in releasePreviewIn) (any, error) {
			return a.ReleasePreview(ctx, in.Since)
		},
	}

	// release.publish has no tool: agents publish a release through the
	// release workflow, and a person runs the command.
	releasePublishSpec = command.Spec[releasePublishIn, any]{
		Name:    "release.publish",
		CLIPath: []string{"release", "publish"},
		Policy:  command.Policy{Class: policy.Remote, Action: "release_publish"},
		// A release belongs to the repository, not to the worktree that ran it.
		Events: command.ScopeRepo,
		Token:  func(in *releasePublishIn) *string { return &in.ConfirmToken },
		DryRun: func(in releasePublishIn) bool { return in.DryRun },
		Describe: func(in releasePublishIn) command.Detail {
			return command.Detail{Subject: "publish release " + in.Tag}
		},
		Run: func(ctx context.Context, a *app.App, in releasePublishIn) (any, error) {
			return a.ReleasePublish(ctx, in.ReleasePublishInput, in.ConfirmToken)
		},
	}
)

func init() {
	command.Register(releasePlanSpec)
	command.Register(releasePreviewSpec)
	command.Register(releasePublishSpec)
	// A verb: the tag comes before the flags, as in the legacy command.
	command.RegisterVerb(command.Verb{
		Path:    []string{"release", "publish"},
		Usage:   "  release publish TAG --revision SHA [--title T] [--notes TEXT] [--dry-run] [--yes]\n                                 explicitly publish a stable release at a commit",
		Section: command.SectionWork,
		Order:   6500,
		Run:     runReleasePublish,
	})
}

func runReleasePublish(ctx context.Context, env command.CLIEnv, inv command.Invoker, args []string) error {
	if len(args) == 0 {
		return errors.New(releasePublishUsage)
	}
	in := releasePublishIn{ReleasePublishInput: app.ReleasePublishInput{Tag: args[0]}}
	fs := flag.NewFlagSet("release publish TAG", flag.ContinueOnError)
	fs.StringVar(&in.Revision, "revision", "", "full commit SHA to publish")
	fs.StringVar(&in.Title, "title", "", "release title (defaults to the tag)")
	fs.StringVar(&in.Notes, "notes", "", "release notes")
	fs.BoolVar(&in.DryRun, "dry-run", false, "preview publication without contacting the provider")
	yes := fs.Bool("yes", false, "accept the confirmation prompt")
	flags := args[1:]
	if strings.HasPrefix(in.Tag, "-") {
		flags = args
	}
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.HasPrefix(in.Tag, "-") {
		return errors.New("pass the release tag before its options")
	}
	a, err := env.Open(ctx)
	if err != nil {
		return err
	}
	origin := env.Origin(a)
	return env.Confirm(*yes, func(token string) (any, error) {
		next := in
		next.ConfirmToken = token
		return command.Invoke(ctx, inv, a, releasePublishSpec, next, origin)
	})
}
