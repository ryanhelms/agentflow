package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/githubissues"
	"github.com/nscaledev/devflow/internal/tasktemplate"
)

func (a *App) resolveIssueDraft(ctx context.Context, project, title, description string, ref tasktemplate.Ref) (provider.TaskDraft, error) {
	var draft provider.TaskDraft
	t, err := a.ResolveTaskTemplate(ref)
	if err != nil {
		return draft, err
	}
	if t != nil {
		if err := t.CheckSupport(githubissues.SupportedFields()); err != nil {
			return draft, err
		}
		if project == "" && t.Defaults.Scope != nil {
			project = *t.Defaults.Scope
		}
	}
	if project == "" {
		project, err = a.defaultIssueRepo(ctx)
	} else {
		project, err = issueScope(project)
	}
	if err != nil {
		return draft, err
	}
	draft = provider.TaskDraft{Scope: project, Title: title, Description: description, Labels: a.issueSettings().Labels}
	if t != nil {
		inputs := tasktemplate.Fields{Scope: &project}
		if title != "" {
			inputs.Title = &title
		}
		if description != "" {
			inputs.Description = &description
		}
		if t.Defaults.Labels == nil {
			inputs.Labels = draft.Labels
		}
		draft, err = t.Resolve(inputs)
		if err != nil {
			return draft, err
		}
		draft.Template = &t.Ref
	}
	return draft, githubissues.ValidateDraft(issuesInstance, draft)
}

// IssueTemplateSnapshot binds approval to a pinned template and the issue fields it validates.
type IssueTemplateSnapshot struct {
	tasktemplate.Ref
	ContentHash string `json:"content_hash"`
}

func (a *App) validateIssueTemplate(task provider.Task) (*IssueTemplateSnapshot, error) {
	if task.Template == nil {
		return nil, nil
	}
	if err := task.Template.Validate(); err != nil {
		return nil, err
	}
	t, err := a.ResolveTaskTemplate(*task.Template)
	if err != nil {
		return nil, err
	}
	if err := t.CheckSupport(githubissues.SupportedFields()); err != nil {
		return nil, err
	}
	d := provider.TaskDraft{Scope: task.Ref.Scope, Title: task.Title, Description: task.Description, Labels: slices.Clone(task.Labels)}
	if err := t.Validate(d); err != nil {
		return nil, fmt.Errorf("issue %s, template %q revision %q: %w", taskKey(task.Ref), t.Name, t.Revision, err)
	}
	slices.Sort(d.Labels)
	content, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return &IssueTemplateSnapshot{Ref: t.Ref, ContentHash: fmt.Sprintf("%x", sha256.Sum256(content))}, nil
}

func (a *App) checkIssueTemplate(expected *IssueTemplateSnapshot) func(provider.Task) error {
	return func(task provider.Task) error {
		current, err := a.validateIssueTemplate(task)
		if err != nil {
			return err
		}
		if expected == nil && current == nil || expected != nil && current != nil && *expected == *current {
			return nil
		}
		return fmt.Errorf("issue template or content changed since preview: %w", provider.ErrConflict)
	}
}
