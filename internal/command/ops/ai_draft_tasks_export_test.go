package ops

import (
	"context"

	"github.com/ryanhelms/agentflow/internal/command"
)

// Test seams of ai_draft_tasks.go for the external tests.

// SetTasksDraftAsk answers the --ai questions.
func SetTasksDraftAsk(f func(prompt string) string) (restore func()) {
	old := tasksDraftAsk
	tasksDraftAsk = f
	return func() { tasksDraftAsk = old }
}

// TaskCreateCall is what task create --ai passes to task create's own path.
type TaskCreateCall struct {
	Summary, Description, Project, Type, LinkTo, Template, Revision string
	Yes, DryRun                                                     bool
}

// SetTasksCreate replaces task create's own path.
func SetTasksCreate(f func(TaskCreateCall) error) (restore func()) {
	old := tasksCreate
	tasksCreate = func(_ context.Context, _ command.CLICall[taskCreateIn], in taskCreateIn) error {
		return f(TaskCreateCall{in.Summary, in.Description, in.Project, in.Type, in.LinkTo, in.Template, in.TemplateRevision, in.yes, in.dryRun})
	}
	return func() { tasksCreate = old }
}
