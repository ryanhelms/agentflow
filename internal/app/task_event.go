package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/nscaledev/devflow/internal/event"
)

type GitHubTaskEventResult struct {
	Plan    event.Plan         `json:"plan"`
	Effects []TransitionResult `json:"effects,omitempty"`
}

// ApplyGitHubTaskEvent uses trusted runner metadata to scope both the event
// and the task provider before applying any write.
func (a *App) ApplyGitHubTaskEvent(ctx context.Context, actions event.GitHubActionsContext, payloadPath string, dryRun bool) (GitHubTaskEventResult, error) {
	result := GitHubTaskEventResult{}
	if !a.usesIssues() {
		return result, fmt.Errorf("github task events require the github-issues task provider")
	}
	scope, err := a.defaultIssueRepo(ctx)
	if err != nil {
		return result, err
	}
	actions.TaskInstance, actions.TaskScope = issuesInstance, scope
	if !strings.EqualFold(scope, actions.Repository.Path) {
		return result, fmt.Errorf("github actions task events require the issue repository to match the runner repository")
	}
	payload, err := os.ReadFile(payloadPath)
	if err != nil {
		return result, fmt.Errorf("read github actions event: %w", err)
	}
	result.Plan, err = event.PlanGitHubActions(actions, payload)
	if err != nil {
		return result, err
	}
	if !dryRun && len(result.Plan.Transitions) > 0 {
		if result.Plan.Lifecycle != "pr.merged" || len(result.Plan.Transitions) != 1 ||
			result.Plan.Transitions[0].Task.ID != actions.ApprovedIssue {
			return result, fmt.Errorf("github task writes require one approved issue on a default-branch merge")
		}
	}
	for _, intent := range result.Plan.Transitions {
		response, err := a.transition(ctx, intent.Task.String(), result.Plan.Lifecycle, dryRun, "", intent.OperationID)
		if err != nil {
			return result, err
		}
		switch value := response.(type) {
		case TransitionResult:
			result.Effects = append(result.Effects, value)
		case *Pending:
			return result, fmt.Errorf("github task event requires confirmation for %s: %s", intent.Task, value.Reason)
		default:
			return result, fmt.Errorf("unexpected transition result for %s", intent.Task)
		}
	}
	return result, nil
}
