package legacy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
)

// ErrNoTransition identifies the legacy workflow no-op when no transition is available.
var ErrNoTransition = errors.New("no Jira transition available")

// Transition matches the destination status name exposed by Jira and validates
// the available transition against the issue's observed status and project order.
func (j *Jira) Transition(ctx context.Context, ref provider.Ref, target provider.TaskState, options provider.WriteOptions) (provider.WriteResult, error) {
	if err := j.validateWrite(ref, options); err != nil {
		return provider.WriteResult{}, err
	}
	name := target.Name
	if name == "" {
		name = target.ID
	}
	if strings.TrimSpace(name) == "" || (target.ID != "" && !strings.EqualFold(target.ID, name)) {
		return provider.WriteResult{}, errors.New("jira transition requires a consistent destination status name")
	}
	issue, actual, err := j.issue(ctx, ref)
	if err != nil {
		return provider.WriteResult{}, err
	}
	from := issue.Fields.Status
	switch {
	case strings.EqualFold(from.Name, name):
		return j.result(actual, provider.AlreadyApplied, issue.Fields.Updated), nil
	case strings.TrimSpace(from.Name) == "":
		return provider.WriteResult{}, fmt.Errorf("jira issue %s has no current status name: %w", actual.ID, provider.ErrConflict)
	case from.Category.Key == "done":
		return provider.WriteResult{}, fmt.Errorf("jira issue %s is in terminal status %q: %w", actual.ID, from.Name, provider.ErrConflict)
	case j.flow != nil && j.flow.Backwards(actual.Scope, from.Name, name):
		return provider.WriteResult{}, fmt.Errorf("jira status %q is before %q in the project order: %w", name, from.Name, provider.ErrConflict)
	}
	transitions, err := j.client.Transitions(ctx, actual.ID)
	if err != nil {
		return provider.WriteResult{}, fmt.Errorf("list transitions for jira issue %s: %w", actual.ID, atlassianError(err))
	}
	fromRank := categoryRank(from.Category.Key)
	for _, transition := range transitions {
		if transition.ID == "" || !strings.EqualFold(transition.To.Name, name) {
			continue
		}
		toRank := categoryRank(transition.To.Category.Key)
		if fromRank < 0 || toRank < 0 {
			return provider.WriteResult{}, fmt.Errorf("cannot verify jira status categories for %q to %q: %w", from.Name, transition.To.Name, provider.ErrConflict)
		}
		if toRank < fromRank {
			return provider.WriteResult{}, fmt.Errorf("jira status %q is in an earlier category than %q: %w", transition.To.Name, from.Name, provider.ErrConflict)
		}
		// Global transitions can remain available after someone else advances the
		// issue. Recheck its status after fetching the transition list.
		latest, latestRef, err := j.issue(ctx, actual)
		if err != nil {
			return provider.WriteResult{}, err
		}
		if strings.EqualFold(latest.Fields.Status.Name, transition.To.Name) {
			return j.result(latestRef, provider.AlreadyApplied, latest.Fields.Updated), nil
		}
		if latestRef != actual || latest.Fields.Status != from {
			return provider.WriteResult{}, fmt.Errorf("jira issue %s changed while checking transitions: %w", actual.ID, provider.ErrConflict)
		}
		if err := j.client.DoTransition(ctx, actual.ID, transition.ID); err != nil {
			return provider.WriteResult{}, fmt.Errorf("transition jira issue %s: %w", actual.ID, atlassianError(err))
		}
		return j.result(actual, provider.Applied, ""), nil
	}
	return provider.WriteResult{}, fmt.Errorf("jira has no transition from %q to %q: %w: %w", from.Name, name, ErrNoTransition, provider.ErrConflict)
}

func categoryRank(category string) int {
	switch category {
	case "new":
		return 0
	case "indeterminate":
		return 1
	case "done":
		return 2
	default:
		return -1
	}
}
