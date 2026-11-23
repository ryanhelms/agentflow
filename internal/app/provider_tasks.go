package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/contextx"
	"github.com/nscaledev/devflow/internal/flowmap"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/asana"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
)

func (a *App) usesFileTasks() bool {
	p := a.Cfg.Providers.TaskManagement.Provider
	return p == "filesystem" || p == "repository"
}

// UsesTaskRefs reports whether task identity is carried in explicit Task-Ref trailers.
func (a *App) UsesTaskRefs() bool { return a.usesIssues() || a.usesFileTasks() || a.usesAsana() }

func (a *App) workQuery(ctx context.Context) (provider.TaskQuery, error) {
	if a.usesAsana() {
		scope := a.asanaSettings().Project
		if scope == "" {
			return provider.TaskQuery{}, errors.New("set task_management.settings.project to the Asana project GID")
		}
		return provider.TaskQuery{Scope: scope, OpenOnly: true, Limit: 30}, nil
	}
	if a.usesFileTasks() {
		return provider.TaskQuery{Scope: a.fileScope(), OpenOnly: true, Limit: 30}, nil
	}
	scope, err := a.defaultIssueRepo(ctx)
	search := a.issueSettings().Search
	return provider.TaskQuery{Scope: scope, AssignedToMe: true, OpenOnly: search.OpenOnly, Limit: search.Limit}, err
}

func (a *App) taskTransitions(scope string) map[string]string {
	if a.usesAsana() {
		return a.asanaSettings().Transitions[scope]
	}
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
	if a.usesAsana() {
		tasks, err := a.asanaTasks()
		return tasks, func() {}, err
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
	switch {
	case a.usesAsana():
		read, err := nativemap.AsanaTask(ref)
		if err != nil {
			return provider.Task{}, err
		}
		return mappedNativeRead(ctx, "asana", read, func() (provider.Task, error) {
			return a.readTaskStore(ctx, ref)
		})
	case a.usesIssues():
		read, err := nativemap.GitHubIssue(ref)
		if err != nil {
			return provider.Task{}, err
		}
		return mappedNativeRead(ctx, "github", read, func() (provider.Task, error) {
			return a.readTaskStore(ctx, ref)
		})
	default:
		return a.readTaskStore(ctx, ref)
	}
}

func (a *App) readTaskStore(ctx context.Context, ref provider.Ref) (provider.Task, error) {
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
		if !a.usesIssues() {
			tc.Text = "devflow · no task bound to branch " + tc.Branch + ". Pick a task from devflow_my_work and run devflow_start, or add a Task-Ref trailer to a branch commit."
		}
		return tc, nil
	}
	tc.Key = taskKey(ref)
	t, err := a.getTask(ctx, ref)
	if err != nil {
		var request *native.Request
		if errors.As(err, &request) || errors.Is(err, native.ErrSignIn) || errors.Is(err, native.ErrDenied) {
			return nil, err
		}
		tc.Warnings = append(tc.Warnings, err.Error())
	} else {
		tc.Summary, tc.Type, tc.Assignee, tc.Status = t.Title, taskType(t), t.Assignee, t.State.Name
		cl, err := a.syncTaskChecklist(ctx, ref, t)
		if err != nil {
			tc.Warnings = append(tc.Warnings, err.Error())
		} else if len(cl.Items) > 0 {
			tc.Criteria = cl
		}
		if a.usesAsana() || a.usesIssues() {
			if err := a.addTaskDocuments(ctx, tc, t); err != nil {
				return nil, err
			}
		}
		tc.FlowNext = a.nextTaskStates(t)
	}
	tc.Text = a.renderContext(tc)
	return tc, nil
}

func (a *App) nextTaskStates(t provider.Task) map[string]string {
	next := map[string]string{}
	for event, state := range a.taskTransitions(t.Ref.Scope) {
		if a.usesAsana() && !t.State.Terminal && state != "none" && state != t.State.ID {
			next[event] = state
		}
		if t.State.ID == "open" && !t.State.Terminal && state == "closed" {
			next[event] = state
		}
	}
	return next
}

func (a *App) syncTaskChecklist(ctx context.Context, ref provider.Ref, t provider.Task) (*contextx.Checklist, error) {
	store := a.Checklists(ctx)
	syncCriteria := store.Sync
	if native.HasClientReplies(ctx) {
		syncCriteria = store.PreviewSync
	}
	cl, err := syncCriteria(ref.StorageKey(), contextx.ParseCriteria(t.Description))
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
	target := a.taskTransitions(ref.Scope)[event]
	if target == "" || target == "none" {
		plan.Note = "no task state mapping for this event"
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
	case a.usesAsana() && task.State.Terminal:
		plan.Note = "completed Asana tasks are terminal"
	case !a.usesAsana() && (task.State.Terminal || plan.From != "open" || target != "closed"):
		plan.Note = "only open issues can move forward to closed"
	default:
		plan.Template, err = a.validateTaskTemplate(*task)
		if err != nil {
			return plan, err
		}
		plan.To = target
		if a.usesAsana() && target == asana.StateCompleted {
			plan.Note = "completes this task across all its Asana projects"
		}
	}
	return plan, nil
}

func (a *App) startProviderTask(ctx context.Context, key string, worktree bool, token string) (any, error) {
	ref, err := a.taskRef(ctx, key)
	if err != nil {
		return nil, err
	}
	write, err := a.taskTransitionWrite(ctx, ref, flowmap.BranchCreated, "")
	if err != nil {
		return nil, err
	}
	task, write, err := a.prepareTaskForWrite(ctx, ref, write)
	if err != nil {
		return nil, err
	}
	// A stable, filesystem-safe name also lets retries resume after a local
	// branch was created but a remote write failed.
	branch := "task-" + ref.StorageKey()[:12]
	if a.usesIssues() {
		branch = "issue-" + ref.ID + "-" + ref.StorageKey()[:12]
	}
	res := StartResult{Key: taskKey(ref), Branch: branch}
	current := a.Repo.Branch(ctx)
	if current != branch && (worktree || a.Repo.HasUncommitted(ctx)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		name := filepath.Base(a.Repo.Dir)
		if common := a.Repo.GitDir(ctx); filepath.Base(common) == ".git" {
			name = filepath.Base(filepath.Dir(common))
		}
		if raw, err := a.Repo.Git(ctx, "remote", "get-url", "origin"); err == nil {
			if remote, err := gitx.ParseRemoteURL(raw); err == nil && filepath.IsLocal(remote.Name) && filepath.Base(remote.Name) == remote.Name {
				name = remote.Name
			}
		}
		res.Worktree = filepath.Join(home, ".worktrees", name, branch)
	}
	s := map[string]any{"key": res.Key, "task_ref": ref.String(), "summary": task.Title, "branch": branch, "worktree": res.Worktree != "", "worktree_path": res.Worktree, "repo": a.Repo.Dir}
	class := policy.Local
	var plan TransitionResult
	var assignment provider.TaskAssignment
	if a.usesIssues() {
		tasks, err := a.issueTasks(ctx)
		if err != nil {
			return nil, err
		}
		var ok bool
		assignment, ok = tasks.(provider.TaskAssignment)
		if !ok {
			return nil, provider.Unsupported(ref.Instance, "assignment")
		}
		s["assign_to_me"] = true
	} else if a.usesAsana() {
		s["assign_to_me"] = false
	}
	if a.usesIssues() || a.usesAsana() {
		plan, err = a.planTaskTransition(ctx, res.Key, flowmap.BranchCreated, &task)
		if err != nil {
			return nil, err
		}
		s["transition"] = plan
		if plan.To != "" && write != nil {
			s["native_write"] = write.taskWritePreview
		}
	}
	if assignment != nil || plan.To != "" {
		level := a.Gate.Autonomy
		if config.Min(level.Local, level.Remote) == level.Remote {
			class = policy.Remote
		}
	}
	if p, err := a.gate(class, "start", s, token, token != "", nil); p != nil || err != nil {
		return p, err
	}
	if err := a.ensureTaskBranch(ctx, ref, res); err != nil {
		a.audit(policy.Local, "branch", s, err)
		return nil, err
	}
	a.audit(policy.Local, "branch", s, nil)
	if assignment != nil {
		_, err = assignment.AssignToMe(ctx, ref, provider.WriteOptions{OperationID: ref.String() + "/start/assign"})
		a.audit(policy.Remote, "assign_to_me", s, err)
		if err != nil {
			return res, err
		}
		res.Assigned = true
	}
	if plan.To != "" {
		tr, err := a.applyPreparedTaskTransition(ctx, ref, plan, write)
		res.Transition = &tr
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

func (a *App) ensureTaskBranch(ctx context.Context, ref provider.Ref, res StartResult) error {
	bound, err := a.boundTask(ctx, res.Branch)
	if err != nil {
		return err
	}
	if bound.ID != "" {
		checkout := a.Repo
		if res.Worktree != "" {
			checkout = &gitx.Repo{Dir: res.Worktree}
		}
		if bound == ref && checkout.Branch(ctx) == res.Branch && checkout.GitDir(ctx) == a.Repo.GitDir(ctx) {
			return nil
		}
		return fmt.Errorf("branch %s already bound; resume start from its worktree", res.Branch)
	}
	if res.Worktree != "" {
		err = a.Repo.AddWorktree(ctx, res.Worktree, res.Branch, a.Repo.BaseRef(ctx))
	} else {
		err = a.Repo.CreateBranch(ctx, res.Branch, a.Repo.BaseRef(ctx))
	}
	if err != nil {
		return err
	}
	_, err = a.Repo.Git(ctx, "config", "--local", "branch."+res.Branch+".devflowTaskRef", ref.String())
	if err != nil {
		return fmt.Errorf("created branch %s but could not save its binding to %s: %w", res.Branch, ref.String(), err)
	}
	return nil
}
