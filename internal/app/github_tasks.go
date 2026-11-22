package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/tasktemplate"
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
	operationID := plan.operationID
	if operationID == "" {
		operationID = ref.String() + "/" + plan.Event
	}
	w, err := tasks.Transition(ctx, ref, provider.TaskState{ID: "closed", Name: "Closed", Terminal: true}, provider.WriteOptions{OperationID: operationID, CheckTask: a.checkTaskTemplate(plan.Template)})
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
	tasks, err := a.issueTasks(ctx)
	if err != nil {
		return nil, err
	}
	comments, ok := tasks.(provider.TaskComments)
	if !ok {
		return nil, provider.Unsupported(ref.Instance, "comments")
	}
	task, err := tasks.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	snapshot, err := a.validateTaskTemplate(task)
	if err != nil {
		return nil, err
	}
	// Retries identify the comment by its target and text, even after an issue edit.
	opts, err := issueWriteOptions("comment", s, token)
	if err != nil {
		return nil, err
	}
	if snapshot != nil {
		s["template"] = snapshot
	}
	if p, err := a.gate(policy.Remote, "comment", s, token, false, nil); p != nil || err != nil {
		return p, err
	}
	opts.CheckTask = a.checkTaskTemplate(snapshot)
	_, err = comments.Comment(ctx, ref, body, opts)
	a.audit(policy.Remote, "comment", s, err)
	return map[string]any{"key": taskKey(ref), "commented": err == nil}, err
}

func (a *App) createIssue(ctx context.Context, project, issueType, title, description, linkTo string, ref tasktemplate.Ref, token string) (any, error) {
	if issueType != "" && !strings.EqualFold(issueType, "issue") && !strings.EqualFold(issueType, "task") {
		return nil, provider.Unsupported(issuesInstance, "issue type "+issueType)
	}
	draft, err := a.resolveIssueDraft(ctx, project, title, description, ref)
	if err != nil {
		return nil, err
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
		draft.Description += "\n\nRelated issue: " + linkTo
	}
	s := map[string]any{"provider": issuesInstance, "project": draft.Scope, "type": "Issue", "summary": draft.Title, "description": draft.Description, "link_to": linkTo, "labels": draft.Labels, "operation_id": issueOperation()}
	if draft.Template != nil {
		s["template"] = draft.Template
	}
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
