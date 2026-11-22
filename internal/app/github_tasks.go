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
	"github.com/nscaledev/devflow/internal/flowmap"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
)

func (a *App) applyIssueTransition(ctx context.Context, plan TransitionResult) (TransitionResult, error) {
	if plan.From != "open" || plan.To != "closed" || a.issueSettings().Transitions[plan.Event] != plan.To {
		return plan, errors.New("invalid forward issue transition")
	}
	ref, err := a.taskRef(ctx, plan.Key)
	if err != nil {
		return plan, err
	}
	if plan.Key != taskKey(ref) {
		return plan, errors.New("issue transition plan requires a qualified target")
	}
	tasks, err := a.issueTasks(ctx)
	if err != nil {
		return plan, err
	}
	w, err := tasks.Transition(ctx, ref, provider.TaskState{ID: "closed", Name: "Closed", Terminal: true}, provider.WriteOptions{OperationID: ref.String() + "/" + plan.Event})
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
	ref, err := a.taskRef(ctx, key)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("issue comment is empty")
	}
	body := text + viaNote(a)
	s := map[string]any{"key": taskKey(ref), "task_ref": ref.String(), "comment": body, "operation_id": issueOperation()}
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
	return map[string]any{"key": taskKey(ref), "commented": err == nil}, err
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
		link, err = a.branchTask(ctx)
	} else {
		link, err = a.taskRef(ctx, linkTo)
	}
	if err != nil {
		return nil, err
	}
	if link.ID != "" {
		linkTo = taskKey(link)
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
	return map[string]string{"key": taskKey(result.Ref)}, nil
}

func (a *App) startIssue(ctx context.Context, key string, worktree bool, token string) (any, error) {
	ref, err := a.taskRef(ctx, key)
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
	plan, err := a.planTaskTransition(ctx, taskKey(ref), flowmap.BranchCreated, &task)
	if err != nil {
		return nil, err
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
