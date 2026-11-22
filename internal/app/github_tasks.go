package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/contextx"
	"github.com/nscaledev/devflow/internal/flowmap"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
)

func (a *App) issueWork(ctx context.Context) ([]WorkItem, error) {
	scope, err := a.defaultIssueRepo(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := a.issueTasks(ctx)
	if err != nil {
		return nil, err
	}
	search := a.issueSettings().Search
	items, err := tasks.Find(ctx, provider.TaskQuery{Scope: scope, AssignedToMe: true, OpenOnly: search.OpenOnly, Limit: search.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]WorkItem, 0, len(items))
	for _, t := range items {
		out = append(out, WorkItem{Key: issueKey(t.Ref), Summary: t.Title, Status: t.State.Name, Type: "Issue", Priority: t.Priority})
	}
	return out, nil
}

func (a *App) getIssue(ctx context.Context, ref provider.Ref) (provider.Task, error) {
	tasks, err := a.issueTasks(ctx)
	if err != nil {
		return provider.Task{}, err
	}
	return tasks.Get(ctx, ref)
}

func (a *App) issueContext(ctx context.Context, key string) (*TicketContext, error) {
	tc := &TicketContext{Branch: a.Repo.Branch(ctx)}
	var ref provider.Ref
	var err error
	if key == "" {
		ref, err = a.branchIssue(ctx)
	} else {
		ref, err = a.issueRef(ctx, key)
	}
	if err != nil {
		return nil, err
	}
	if ref.ID == "" {
		tc.Text = "devflow · no issue bound to branch " + tc.Branch + ". Call devflow_my_work to pick an issue, then devflow_start."
		return tc, nil
	}
	tc.Key = issueKey(ref)
	t, err := a.getIssue(ctx, ref)
	if err != nil {
		tc.Warnings = append(tc.Warnings, err.Error())
	} else {
		tc.Summary, tc.Type, tc.Assignee, tc.Status = t.Title, "Issue", t.Assignee, t.State.Name
		cl, err := a.syncIssueChecklist(ctx, ref, t)
		if err != nil {
			tc.Warnings = append(tc.Warnings, err.Error())
		} else if len(cl.Items) > 0 {
			tc.Criteria = cl
		}
		tc.FlowNext = a.nextIssueStates(t)
	}
	tc.Text = a.renderContext(tc)
	return tc, nil
}

func (a *App) nextIssueStates(t provider.Task) map[string]string {
	next := map[string]string{}
	for event, state := range a.issueSettings().Transitions {
		if t.State.ID == "open" && !t.State.Terminal && state == "closed" {
			next[event] = state
		}
	}
	return next
}

func (a *App) syncIssueChecklist(ctx context.Context, ref provider.Ref, t provider.Task) (*contextx.Checklist, error) {
	cl, err := a.Checklists(ctx).Sync(ref.StorageKey(), contextx.ParseCriteria(t.Description))
	if err == nil {
		cl.Key = issueKey(ref)
	}
	return cl, err
}

func (a *App) planIssueTransition(ctx context.Context, key, event string, task *provider.Task) (TransitionResult, error) {
	ref, err := a.issueRef(ctx, key)
	plan := TransitionResult{Key: key, Event: event}
	if err != nil {
		return plan, err
	}
	plan.Key = issueKey(ref)
	target := a.issueSettings().Transitions[event]
	if target == "" || target == "none" {
		plan.Note = "no issue state mapping for this event"
		return plan, nil
	}
	if task == nil {
		current, err := a.getIssue(ctx, ref)
		if err != nil {
			return plan, err
		}
		task = &current
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

func (a *App) applyIssueTransition(ctx context.Context, plan TransitionResult) (TransitionResult, error) {
	if plan.From != "open" || plan.To != "closed" || a.issueSettings().Transitions[plan.Event] != plan.To {
		return plan, errors.New("invalid forward issue transition")
	}
	ref, err := a.issueRef(ctx, plan.Key)
	if err != nil {
		return plan, err
	}
	if plan.Key != issueKey(ref) {
		return plan, errors.New("issue transition plan requires a qualified target")
	}
	tasks, err := a.issueTasks(ctx)
	if err != nil {
		return plan, err
	}
	operationID := plan.operationID
	if operationID == "" {
		operationID = ref.String() + "/" + plan.Event
	}
	w, err := tasks.Transition(ctx, ref, provider.TaskState{ID: "closed", Name: "Closed", Terminal: true}, provider.WriteOptions{OperationID: operationID})
	a.audit(policy.Remote, "transition", plan, err)
	plan.Applied = err == nil && w.Status == provider.Applied
	if err == nil && w.Status == provider.AlreadyApplied {
		plan.Note = "already closed"
	}
	return plan, err
}

// Explicit operation IDs survive a retry from a new process. GitHub workflow
// re-runs keep the run ID and job. Interactive approvals identify one write;
// unconfirmed human comments may intentionally repeat the same text.
func issueOperation() string {
	if id := os.Getenv("DEVFLOW_OPERATION_ID"); id != "" {
		return id
	}
	if id := os.Getenv("GITHUB_RUN_ID"); id != "" {
		return "github/" + os.Getenv("GITHUB_REPOSITORY") + "/" + id + "/" + os.Getenv("GITHUB_JOB")
	}
	return ""
}

func issueWriteOptions(action string, payload map[string]any, token string) (provider.WriteOptions, error) {
	id, _ := payload["operation_id"].(string)
	if id == "" {
		id = token
	}
	if id == "" {
		return provider.WriteOptions{}, nil
	}
	data, err := json.Marshal([]any{action, id, payload})
	if err != nil {
		return provider.WriteOptions{}, err
	}
	return provider.WriteOptions{OperationID: fmt.Sprintf("%x", sha256.Sum256(data))}, nil
}

func (a *App) commentIssue(ctx context.Context, key, text, token string) (any, error) {
	ref, err := a.issueRef(ctx, key)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("issue comment is empty")
	}
	body := text + viaNote(a)
	s := map[string]any{"key": issueKey(ref), "task_ref": ref.String(), "comment": body, "operation_id": issueOperation()}
	if p, err := a.gate(policy.Remote, "comment", s, token, false, nil); p != nil || err != nil {
		return p, err
	}
	tasks, err := a.issueTasks(ctx)
	if err != nil {
		return nil, err
	}
	comments, ok := tasks.(provider.TaskComments)
	if !ok {
		return nil, provider.Unsupported(ref.Instance, "comments")
	}
	opts, err := issueWriteOptions("comment", s, token)
	if err != nil {
		return nil, err
	}
	_, err = comments.Comment(ctx, ref, body, opts)
	a.audit(policy.Remote, "comment", s, err)
	return map[string]any{"key": issueKey(ref), "commented": err == nil}, err
}

func (a *App) createIssue(ctx context.Context, project, issueType, title, description, linkTo, token string) (any, error) {
	var err error
	if project == "" {
		project, err = a.defaultIssueRepo(ctx)
	} else {
		project, err = issueScope(project)
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("issue title is required")
	}
	if issueType != "" && !strings.EqualFold(issueType, "issue") && !strings.EqualFold(issueType, "task") {
		return nil, provider.Unsupported(issuesInstance, "issue type "+issueType)
	}
	var link provider.Ref
	if linkTo == "" {
		link, err = a.branchIssue(ctx)
	} else {
		link, err = a.issueRef(ctx, linkTo)
	}
	if err != nil {
		return nil, err
	}
	if link.ID != "" {
		linkTo = issueKey(link)
		description = strings.TrimRight(description, "\n") + "\n\nRelated issue: " + linkTo
	}
	draft := provider.TaskDraft{Scope: project, Title: title, Description: description, Labels: a.issueSettings().Labels}
	s := map[string]any{"provider": issuesInstance, "project": project, "type": "Issue", "summary": title, "description": description, "link_to": linkTo, "labels": draft.Labels, "operation_id": issueOperation()}
	if p, err := a.gate(policy.Remote, "ticket_create", s, token, true, nil); p != nil || err != nil {
		return p, err
	}
	tasks, err := a.issueTasks(ctx)
	if err != nil {
		return nil, err
	}
	opts, err := issueWriteOptions("ticket_create", s, token)
	if err != nil {
		return nil, err
	}
	result, err := tasks.Create(ctx, draft, opts)
	a.audit(policy.Remote, "ticket_create", s, err)
	if err != nil {
		return nil, err
	}
	return map[string]string{"key": issueKey(result.Ref)}, nil
}

func (a *App) startIssue(ctx context.Context, key string, worktree bool, token string) (any, error) {
	ref, err := a.issueRef(ctx, key)
	if err != nil {
		return nil, err
	}
	tasks, err := a.issueTasks(ctx)
	if err != nil {
		return nil, err
	}
	assignment, ok := tasks.(provider.TaskAssignment)
	if !ok {
		return nil, provider.Unsupported(ref.Instance, "assignment")
	}
	task, err := tasks.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	// A stable, filesystem-safe name also lets retries resume after a local
	// branch was created but a remote write failed.
	branch := "issue-" + ref.ID + "-" + ref.StorageKey()[:12]
	plan, err := a.planIssueTransition(ctx, issueKey(ref), flowmap.BranchCreated, &task)
	if err != nil {
		return nil, err
	}
	res := StartResult{Key: issueKey(ref), Branch: branch}
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
	s := map[string]any{"key": res.Key, "task_ref": ref.String(), "summary": task.Title, "branch": branch, "worktree": res.Worktree != "", "worktree_path": res.Worktree, "repo": a.Repo.Dir, "assign_to_me": true, "transition": plan}
	level := a.Gate.Autonomy
	class := policy.Remote
	if config.Min(level.Local, level.Remote) != level.Remote {
		class = policy.Local
	}
	if p, err := a.gate(class, "start", s, token, false, nil); p != nil || err != nil {
		return p, err
	}
	if err := a.ensureIssueBranch(ctx, ref, res); err != nil {
		a.audit(policy.Local, "branch", s, err)
		return nil, err
	}
	a.audit(policy.Local, "branch", s, nil)
	_, err = assignment.AssignToMe(ctx, ref, provider.WriteOptions{OperationID: ref.String() + "/start/assign"})
	a.audit(policy.Remote, "assign_to_me", s, err)
	if err != nil {
		return res, err
	}
	res.Assigned = true
	if plan.To != "" {
		tr, err := a.ApplyTransition(ctx, plan)
		res.Transition = &tr
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

func (a *App) ensureIssueBranch(ctx context.Context, ref provider.Ref, res StartResult) error {
	bound, err := a.boundIssue(ctx, res.Branch)
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
