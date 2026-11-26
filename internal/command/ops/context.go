package ops

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// contextIn is the input of agentflow_context: a task key and the client's
// native read results.
type contextIn struct {
	taskKeyIn
	taskNativeReadIn
}

// contextOut is the ticket bundle. An MCP client gets its text as the tool
// result, not the JSON of the whole bundle.
type contextOut struct{ *app.TicketContext }

// ContextText is the text the MCP server returns for the bundle.
func (c contextOut) ContextText() string { return c.Text }

func init() {
	command.Register(command.Spec[contextIn, any]{
		Name:    "context",
		CLIPath: []string{"context"},
		CLI: &command.CLI[contextIn]{
			// Like the legacy command: the first argument is the key and any
			// further ones are ignored.
			Args: func(in *contextIn, args []string) error {
				if len(args) > 0 {
					in.Key = args[0]
				}
				return nil
			},
			Run: func(ctx context.Context, call command.CLICall[contextIn], in contextIn) error {
				out, err := call.Invoke(ctx, in)
				if err != nil {
					return err
				}
				bundle, ok := out.(contextOut)
				if !ok || bundle.TicketContext == nil {
					return errors.New("expected a ticket bundle")
				}
				fmt.Fprint(os.Stdout, bundle.Text)
				return nil
			},
			Usage:   "  context [KEY]                  ticket bundle for the current branch",
			Section: command.SectionWork,
			Order:   2900,
		},
		MCP: &command.MCP{Tool: "agentflow_context", Description: "Current ticket bundle: summary, status, acceptance criteria, epic, design docs, flow-map next statuses and other work on the ticket.",
			Annotations: command.ReadOnly},
		Policy:      command.Policy{Class: policy.Read},
		NativeReads: true,
		Run: func(ctx context.Context, a *app.App, in contextIn) (any, error) {
			tc, err := a.Context(taskProviderContext(ctx, a, in.taskNativeReadIn), in.Key)
			return contextOut{tc}, err
		},
	})
}
