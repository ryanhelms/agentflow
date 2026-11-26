package ops

import (
	"context"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type eventsIn struct {
	Since  string   `json:"since,omitempty" jsonschema:"cursor from an earlier result; omit to start at the oldest retained event"`
	Types  []string `json:"types,omitempty" jsonschema:"event type patterns, e.g. git.** or config.layer.changed; * matches one segment, a final ** the rest"`
	Topics []string `json:"topics,omitempty" jsonschema:"topics REPO/WORKTREE/PATTERN; * as REPO means this repository, global names the user-level events"`
	Limit  int      `json:"limit,omitempty" jsonschema:"most events to return (default 100, at most 1000)"`
}

// The events CLI stays in internal/cli for now: --follow and --all-repos use
// the console daemon and cli.servedRepos, which its tests replace.
func init() {
	command.Register(command.Spec[eventsIn, any]{
		Name:    "events",
		CLIPath: []string{"events"},
		MCP: &command.MCP{Tool: "agentflow_events", Description: "Page through this repository's git and configuration events (branch switches, commits, ref and index changes, git operations, .agentflow config edits) and the user-level events (repository global: personal configuration and client settings) recorded by the console daemon. Each event names its repository. Returns {repo, events, cursor, more}; pass cursor as since to get only newer events. Read-only; other repositories are not readable here, and there is no follow mode, so poll with the cursor.",
			Annotations: command.ReadOnly},
		Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in eventsIn) (any, error) {
			return a.Events(ctx, app.EventsQuery{Since: in.Since, Types: in.Types, Topics: in.Topics, Limit: in.Limit})
		},
	})
}
