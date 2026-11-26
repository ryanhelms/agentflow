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

func init() {
	command.Register(command.Spec[taskNativeReadIn, any]{
		Name:    "work",
		CLIPath: []string{"work"},
		CLI: &command.CLI[taskNativeReadIn]{
			// Like the legacy command, `work` takes no arguments and ignores any.
			Args: func(*taskNativeReadIn, []string) error { return nil },
			Run: func(ctx context.Context, call command.CLICall[taskNativeReadIn], in taskNativeReadIn) error {
				out, err := call.Invoke(ctx, in)
				if err != nil {
					return err
				}
				items, ok := out.([]app.WorkItem)
				if !ok && out != nil {
					return errors.New("expected a work list")
				}
				for _, i := range items {
					fmt.Fprintf(os.Stdout, "%-12s %-14s %-8s %s\n", i.Key, i.Status, i.Priority, i.Summary)
				}
				return nil
			},
			Usage:   "  work                           list work from the selected task provider",
			Section: command.SectionWork,
			Order:   2000,
		},
		MCP: &command.MCP{Tool: "agentflow_my_work", Description: "List work from the configured task provider.",
			Annotations: command.ReadOnly},
		Policy:      command.Policy{Class: policy.Read},
		NativeReads: true,
		Run: func(ctx context.Context, a *app.App, in taskNativeReadIn) (any, error) {
			return a.MyWork(taskProviderContext(ctx, a, in))
		},
	})
}
