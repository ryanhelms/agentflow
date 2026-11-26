package ops

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// standupIn is the tool's input. The command line takes a duration, which
// can be shorter than an hour, so it fills since instead of SinceHours.
type standupIn struct {
	SinceHours int `json:"since_hours,omitempty" jsonschema:"look back this many hours; default is since the last working day"`

	since  time.Duration // --since
	asJSON bool          // --json
}

func init() {
	command.Register(command.Spec[standupIn, any]{
		Name:    "standup",
		CLIPath: []string{"standup"},
		CLI: &command.CLI[standupIn]{
			Flags: func(fs *flag.FlagSet, in *standupIn) {
				fs.DurationVar(&in.since, "since", 0, "look back this long (default: since the last working day)")
				fs.BoolVar(&in.asJSON, "json", false, "print raw JSON")
			},
			// Like the legacy command, extra arguments are ignored.
			Args: func(*standupIn, []string) error { return nil },
			Run: func(ctx context.Context, call command.CLICall[standupIn], in standupIn) error {
				out, err := call.Invoke(ctx, in)
				if err != nil {
					return err
				}
				if in.asJSON {
					return call.Env.Print(out, nil)
				}
				report, _ := out.(map[string]any)
				standupPrint(report)
				return nil
			},
			Usage:   "  standup [--since 24h] [--json] activity to draft a standup",
			Section: command.SectionWork,
			Order:   5100,
		},
		MCP: &command.MCP{Tool: "agentflow_standup", Description: "Activity since the last working day (commits, PRs, reviews, tickets, blockers) to draft a standup.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in standupIn) (any, error) {
			since := time.Duration(in.SinceHours) * time.Hour
			if in.since != 0 {
				since = in.since
			}
			return a.Standup(ctx, since)
		},
	})
}

// standupPrint prints the report's sections as text.
func standupPrint(out map[string]any) {
	section := func(title, key string) {
		items, _ := out[key].([]string)
		if len(items) == 0 {
			return
		}
		fmt.Fprintln(os.Stdout, title)
		for _, i := range items {
			fmt.Fprintln(os.Stdout, "  -", i)
		}
	}
	section("Commits", "commits")
	section("PRs", "prs")
	section("Reviewed", "reviewed")
	section("Tickets", "tickets")
	section("Blocked", "blocked")
}
