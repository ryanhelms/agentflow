package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/githubissues"
	"github.com/ryanhelms/agentflow/internal/provider/native"
	"github.com/ryanhelms/agentflow/internal/provider/nativemap"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
)

func (a *App) applyIssueTransition(ctx context.Context, plan TransitionResult) (TransitionResult, error) {
	if plan.From != "open" || plan.To != "closed" || a.issueSettings().Transitions[plan.Event] != plan.To {
		return plan, errors.New("invalid forward issue transition")
	}
	ref, err := a.taskRef(ctx, plan.Key)
	if err != nil {
		return plan, err
	}
	if err := a.taskWriteScope(ctx, ref.Scope); err != nil {
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
	if id := os.Getenv("AGENTFLOW_OPERATION_ID"); id != "" {
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
	if err := a.taskWriteScope(ctx, ref.Scope); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("issue comment is empty")
	}
	body := text + viaNote(a, "comment")
	operationID := issueOperation()
	var task provider.Task
	if operationID == "" && os.Getenv("GITHUB_ACTIONS") != "true" {
		mapped, err := nativemap.GitHubIssueComment(ref, body)
		write, err := bindTaskWrite(ctx, mapped, err)
		if err != nil {
			return nil, err
		}
		if write != nil {
			task, write, err = a.prepareTaskForWrite(ctx, ref, write)
			if err != nil {
				return nil, err
			}
			if write != nil {
				return a.commentBoundTask(ctx, task, body, token, write)
			}
		}
	}
	s := map[string]any{"key": taskKey(ref), "task_ref": ref.String(), "comment": body, "operation_id": operationID}
	tasks, err := a.issueTasks(ctx)
	if err != nil {
		return nil, err
	}
	comments, ok := tasks.(provider.TaskComments)
	if !ok {
		return nil, provider.Unsupported(ref.Instance, "comments")
	}
	if task.Ref.ID == "" {
		task, err = tasks.Get(ctx, ref)
		if err != nil {
			return nil, err
		}
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
	if p, err := a.gate(policy.Remote, "comment", s, token, token != "", nil); p != nil || err != nil {
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
	if err := a.taskWriteScope(ctx, draft.Scope); err != nil {
		return nil, err
	}
	var link provider.Ref
	document, parseErr := provider.ParseRef(linkTo)
	if parseErr == nil && document.Instance != issuesInstance {
		if selected := a.Cfg.Providers.KnowledgeBase.Provider; selected == "none" || document.Instance != selected {
			return nil, fmt.Errorf("document %s does not match the selected knowledge provider %q", document, selected)
		}
		draft.Description, err = githubissues.AppendRelatedKnowledge(draft.Description, document)
		if err != nil {
			return nil, err
		}
	} else {
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
	}
	s := map[string]any{"provider": issuesInstance, "project": draft.Scope, "type": "Issue", "summary": draft.Title, "description": draft.Description, "link_to": linkTo, "labels": draft.Labels, "operation_id": issueOperation()}
	if draft.Template != nil {
		s["template"] = draft.Template
	}
	if s["operation_id"] == "" && os.Getenv("GITHUB_ACTIONS") != "true" {
		mapped, err := nativemap.GitHubIssueCreate(draft)
		write, err := bindTaskWrite(ctx, mapped, err)
		if err != nil {
			return nil, err
		}
		if write != nil {
			id, err := issueRepositoryForWrite(ctx, draft.Scope, write)
			if err == nil {
				s["repository_id"] = id
				return a.createBoundIssue(ctx, s, token, write)
			}
			if !errors.Is(err, native.ErrUnavailable) {
				return nil, err
			}
		}
		if a.Client == "claude" || a.Client == "claude-desktop" {
			client, err := a.GitHub(ctx)
			if err != nil {
				return nil, err
			}
			account, err := client.SignedInCLIAccount(ctx)
			if err != nil {
				return nil, err
			}
			if account != nil {
				s["backend"], s["github_account"] = "gh", account
			}
		}
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

// GitHub resolves repository names before creating issues. A same-session
// lookup rejects stale or aliased names before sending the mutation.
func issueRepositoryForWrite(ctx context.Context, scope string, write *boundTaskWrite) (int64, error) {
	read, err := nativemap.GitHubIssueRepository(scope)
	if err != nil {
		return 0, err
	}
	return readBoundTaskData(ctx, write, read)
}

func (a *App) createBoundIssue(ctx context.Context, summary map[string]any, token string, write *boundTaskWrite) (any, error) {
	summary["native_write"] = write.taskWritePreview
	if p, err := a.gate(policy.Remote, "ticket_create", summary, token, true, nil, write.Binding.ExpiresAt); p != nil || err != nil {
		return p, err
	}
	result, err := applyTaskAppend(ctx, a.Home, token, write.Call.Provider, write.taskWritePreview, func() (provider.WriteResult, error) { return write.apply(ctx) })
	a.audit(policy.Remote, "ticket_create", summary, err)
	if err != nil {
		return nil, err
	}
	return map[string]string{"key": taskKey(result.Ref)}, nil
}
