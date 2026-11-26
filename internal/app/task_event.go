package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ryanhelms/agentflow/internal/event"
)

type GitHubTaskEventResult struct {
	Plan    event.Plan         `json:"plan"`
	Effects []TransitionResult `json:"effects,omitempty"`
}

// ApplyGitHubTaskEvent uses trusted runner metadata to scope both the event
// and the task provider before applying any write.
func (a *App) ApplyGitHubTaskEvent(ctx context.Context, actions event.GitHubActionsContext, payloadPath string, dryRun bool) (GitHubTaskEventResult, error) {
	result := GitHubTaskEventResult{}
	actions.TaskInstance = a.Cfg.Providers.TaskManagement.Provider
	switch {
	case a.usesIssues():
		scope, err := a.defaultIssueRepo(ctx)
		if err != nil {
			return result, err
		}
		if !strings.EqualFold(scope, actions.Repository.Path) {
			return result, fmt.Errorf("github actions task events require the issue repository to match the runner repository")
		}
		actions.TaskScope = scope
	case a.usesFileTasks():
		actions.TaskScope = a.fileScope()
	case a.usesAsana():
		actions.TaskScope = a.asanaSettings().Project
	case actions.TaskInstance == "none":
		result.Plan = event.Plan{Repository: actions.Repository, RepositoryID: actions.RepositoryID, EventName: actions.EventName, EventID: actions.EventID, Skip: "provider_disabled"}
		return result, nil
	case actions.TaskInstance != "jira":
		return result, fmt.Errorf("workflow events do not support task_management.provider %q", actions.TaskInstance)
	}
	payload, err := os.ReadFile(payloadPath)
	if err != nil {
		return result, fmt.Errorf("read github actions event: %w", err)
	}
	result.Plan, err = event.PlanGitHubActions(actions, payload)
	if err != nil {
		return result, err
	}
	if len(result.Plan.Transitions) == 0 {
		return result, nil
	}
	if !dryRun {
		if !a.usesIssues() || actions.Repository.Host != "github.com" || !strings.EqualFold(actions.Repository.Path, "ryanhelms/agentflow") ||
			actions.EventName != "pull_request" || result.Plan.Lifecycle != "pr.merged" || len(result.Plan.Transitions) != 1 ||
			result.Plan.Transitions[0].Task.ID != actions.ApprovedIssue {
			return result, fmt.Errorf("github task writes require one approved ryanhelms/agentflow issue on a default-branch pull_request merge")
		}
	}
	if err := a.prepareWorkflowTasks(); err != nil {
		return result, err
	}
	for _, intent := range result.Plan.Transitions {
		key := intent.Task.String()
		if actions.TaskInstance == "jira" {
			key = intent.Task.ID
		}
		lifecycle := result.Plan.Lifecycle
		if intent.Lifecycle != "" {
			lifecycle = intent.Lifecycle
		}
		response, err := a.transition(ctx, key, lifecycle, dryRun, "", intent.OperationID)
		if err != nil {
			return result, fmt.Errorf("plan %s for %q with %s (check the reference and runner read access): %w", lifecycle, key, actions.TaskInstance, err)
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
