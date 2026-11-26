package ops

import (
	"context"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/delegate"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type delegateIn struct {
	Repo         string `json:"repo" jsonschema:"repository the work belongs to: NAME or OWNER/NAME"`
	Task         string `json:"task" jsonschema:"the work, in words the receiving session can act on"`
	Agent        string `json:"agent,omitempty" jsonschema:"client for a new session: claude or codex (default: this client)"`
	Start        bool   `json:"start,omitempty" jsonschema:"start a new tmux session even when one is running there"`
	DryRun       bool   `json:"dry_run,omitempty" jsonschema:"show what would be sent or started"`
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"token from a needs_confirmation response, after the user agreed"`
}

// delegate's CLI stays in internal/cli for now: by default it prints the plan
// as text after the confirmation, and the CLI environment's confirmation flow
// prints the final result as JSON itself.
func init() {
	command.Register(command.Spec[delegateIn, any]{
		Name:    "delegate",
		CLIPath: []string{"delegate"},
		MCP: &command.MCP{Tool: "agentflow_delegate", Description: "Hand a task to an agent session in another repository. A session never changes another repository itself. " +
			"Sends the task to a running tmux session there, returns status native when your client should message or start a session itself, " +
			"or starts a new tmux session in that repository's checkout.",
			Annotations: command.LocalWrite},
		Policy: command.Policy{Class: policy.Local, Action: "delegate"},
		Token:  func(in *delegateIn) *string { return &in.ConfirmToken },
		DryRun: func(in delegateIn) bool { return in.DryRun },
		// The task text is a message body: only the repository goes on the timeline.
		Describe: func(in delegateIn) command.Detail {
			return command.Detail{Subject: "delegate to " + delegate.RepoName(in.Repo)}
		},
		Run: func(ctx context.Context, a *app.App, in delegateIn) (any, error) {
			return a.Delegate(ctx, delegate.Request{Repo: in.Repo, Task: in.Task, Agent: in.Agent, Start: in.Start, DryRun: in.DryRun}, in.ConfirmToken)
		},
	})
}
