package app

import (
	"context"

	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/githubissues"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
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
