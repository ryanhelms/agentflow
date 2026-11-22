package app

import (
	"context"
	"errors"
	"io/fs"

	"github.com/nscaledev/devflow/internal/contextx"
	"github.com/nscaledev/devflow/internal/provider"
)

func (a *App) usesFileTasks() bool {
	p := a.Cfg.Providers.TaskManagement.Provider
	return p == "filesystem" || p == "repository"
}

// UsesTaskRefs reports whether task identity is carried in explicit Task-Ref trailers.
func (a *App) UsesTaskRefs() bool { return a.usesIssues() || a.usesFileTasks() }

func (a *App) workQuery(ctx context.Context) (provider.TaskQuery, error) {
	if a.usesFileTasks() {
		return provider.TaskQuery{Scope: a.fileScope(), OpenOnly: true, Limit: 30}, nil
	}
	scope, err := a.defaultIssueRepo(ctx)
	search := a.issueSettings().Search
	return provider.TaskQuery{Scope: scope, AssignedToMe: true, OpenOnly: search.OpenOnly, Limit: search.Limit}, err
}

func (a *App) taskTransitions() map[string]string {
	if a.usesFileTasks() {
		return a.fileTaskSettings().Transitions
	}
	return a.issueSettings().Transitions
}

func taskType(t provider.Task) string {
	if t.Ref.Instance == issuesInstance {
		return "Issue"
	}
	return t.Type
}

func (a *App) openTaskStore(ctx context.Context) (provider.Tasks, func(), error) {
	if a.usesFileTasks() {
		target, err := a.fileTarget(ctx, false)
		if err != nil {
			return nil, nil, err
		}
		return target.open()
	}
	tasks, err := a.issueTasks(ctx)
	return tasks, func() {}, err
}

func (a *App) providerWork(ctx context.Context) ([]WorkItem, error) {
	query, err := a.workQuery(ctx)
	if err != nil {
		return nil, err
	}
	tasks, closeStore, err := a.openTaskStore(ctx)
	if a.Cfg.Providers.TaskManagement.Provider == "filesystem" && errors.Is(err, fs.ErrNotExist) {
		// Create opens the directory lazily after approval.
		return []WorkItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer closeStore()
	items, err := tasks.Find(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make([]WorkItem, 0, len(items))
	for _, t := range items {
		out = append(out, WorkItem{Key: taskKey(t.Ref), Summary: t.Title, Status: t.State.Name, Type: taskType(t), Priority: t.Priority})
	}
	return out, nil
}

func (a *App) getTask(ctx context.Context, ref provider.Ref) (provider.Task, error) {
	tasks, closeStore, err := a.openTaskStore(ctx)
	if err != nil {
		return provider.Task{}, err
	}
	defer closeStore()
	return tasks.Get(ctx, ref)
}

func (a *App) providerContext(ctx context.Context, key string) (*TicketContext, error) {
	tc := &TicketContext{Branch: a.Repo.Branch(ctx)}
	var ref provider.Ref
	var err error
	if key == "" {
		ref, err = a.branchTask(ctx)
	} else {
		ref, err = a.taskRef(ctx, key)
	}
	if err != nil {
		return nil, err
	}
	if ref.ID == "" {
		tc.Text = "devflow · no issue bound to branch " + tc.Branch + ". Call devflow_my_work to pick an issue, then devflow_start."
		if a.usesFileTasks() {
			tc.Text = "devflow · no task bound to branch " + tc.Branch + ". Pick a task from devflow_my_work or add a Task-Ref trailer to a branch commit."
		}
		return tc, nil
	}
	tc.Key = taskKey(ref)
	t, err := a.getTask(ctx, ref)
	if err != nil {
		tc.Warnings = append(tc.Warnings, err.Error())
	} else {
		tc.Summary, tc.Type, tc.Assignee, tc.Status = t.Title, taskType(t), t.Assignee, t.State.Name
		cl, err := a.syncTaskChecklist(ctx, ref, t)
		if err != nil {
			tc.Warnings = append(tc.Warnings, err.Error())
		} else if len(cl.Items) > 0 {
			tc.Criteria = cl
		}
		tc.FlowNext = a.nextTaskStates(t)
	}
	tc.Text = a.renderContext(tc)
	return tc, nil
}

func (a *App) nextTaskStates(t provider.Task) map[string]string {
	next := map[string]string{}
	for event, state := range a.taskTransitions() {
		if t.State.ID == "open" && !t.State.Terminal && state == "closed" {
			next[event] = state
		}
	}
	return next
}

func (a *App) syncTaskChecklist(ctx context.Context, ref provider.Ref, t provider.Task) (*contextx.Checklist, error) {
	cl, err := a.Checklists(ctx).Sync(ref.StorageKey(), contextx.ParseCriteria(t.Description))
	if err == nil {
		cl.Key = taskKey(ref)
	}
	return cl, err
}

func (a *App) planTaskTransition(ctx context.Context, key, event string, task *provider.Task) (TransitionResult, error) {
	ref, err := a.taskRef(ctx, key)
	plan := TransitionResult{Key: key, Event: event}
	if err != nil {
		return plan, err
	}
	plan.Key = taskKey(ref)
	target := a.taskTransitions()[event]
	if target == "" || target == "none" {
		plan.Note = "no issue state mapping for this event"
		return plan, nil
	}
	if task == nil {
		current, err := a.getTask(ctx, ref)
		if err != nil {
			return plan, err
		}
		task = &current
	}
	if a.usesFileTasks() {
		plan.revision = task.Revision
	}
	plan.From = task.State.ID
	switch {
	case plan.From == target:
		plan.Note = "already " + target
	case task.State.Terminal || plan.From != "open" || target != "closed":
		plan.Note = "only open issues can move forward to closed"
	default:
		plan.To = target
	}
	return plan, nil
}
