package ops

import (
	"context"
	"errors"
	"flag"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type commentIn struct {
	Key          string `json:"key,omitempty"`
	Text         string `json:"text"`
	ConfirmToken string `json:"confirm_token,omitempty"`
}

func init() {
	command.Register(command.Spec[commentIn, any]{
		Name:    "comment",
		CLIPath: []string{"comment"},
		CLI: &command.CLI[commentIn]{
			// Declared here to keep the legacy command's help text.
			Flags: func(fs *flag.FlagSet, _ *commentIn) { fs.Bool("yes", false, "accept the confirmation prompt") },
			Args: func(in *commentIn, args []string) error {
				switch len(args) {
				case 1:
					in.Text = args[0]
				case 2:
					in.Key, in.Text = args[0], args[1]
				default:
					return errors.New("usage: agentflow comment [KEY] TEXT [--yes]")
				}
				return nil
			},
			// A comment body or key spelled "--yes" follows "--".
			Interspersed: true,
			Confirm:      true,
			Usage:        "  comment [KEY] TEXT [--yes]     comment on the task",
			Section:      command.SectionWork,
			Order:        3300,
		},
		MCP: &command.MCP{Tool: "agentflow_comment", Description: "Comment on a task when its provider supports comments.",
			Annotations: command.RemoteWrite, WriteHost: command.WriteHostTasks},
		Policy: command.Policy{Class: policy.Remote, Action: "comment"},
		Token:  func(in *commentIn) *string { return &in.ConfirmToken },
		// The text is the message body: only the task is named.
		Describe: func(in commentIn) command.Detail {
			return command.Detail{Subject: "comment on a task", Task: in.Key}
		},
		Run: func(ctx context.Context, a *app.App, in commentIn) (any, error) {
			if taskInteractiveClient(a) || taskCLITerminal(a) {
				ctx = app.WithJiraWriteCLI(ctx)
			}
			return a.Comment(ctx, in.Key, in.Text, in.ConfirmToken)
		},
	})
}
