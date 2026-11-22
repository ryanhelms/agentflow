package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/asana"
	"github.com/nscaledev/devflow/internal/tasktemplate"
)

func (a *App) usesAsana() bool { return a.Cfg.Providers.TaskManagement.Provider == "asana" }

func (a *App) asanaSettings() *config.AsanaSettings {
	return a.Cfg.Providers.TaskManagement.Settings.(*config.AsanaSettings)
}

func (a *App) asanaTasks() (provider.Tasks, error) {
	if a.tasks != nil {
		return a.tasks, nil
	}
	return asana.FromEnvironment(a.asanaSettings().AdapterConfig())
}

func (a *App) validateAsanaRef(ref provider.Ref) (provider.Ref, error) {
	if ref.Instance != "asana" || !asana.ValidGID(ref.ID) || !a.asanaSettings().HasProject(ref.Scope) {
		return provider.Ref{}, fmt.Errorf("expected an Asana task GID in a configured project, got %q", ref)
	}
	return ref, nil
}

func (a *App) asanaLink(ctx context.Context, input string) ([]provider.Link, error) {
	if input != "" && strings.Contains(input, "/") {
		ref, err := provider.ParseRef(input)
		if err != nil {
			return nil, err
		}
		if ref.Instance == a.Cfg.Providers.KnowledgeBase.Provider && ref.Instance != "none" {
			return []provider.Link{{Ref: &ref}}, nil
		}
	}
	var ref provider.Ref
	var err error
	if input == "" {
		ref, err = a.branchTask(ctx)
	} else {
		ref, err = a.taskRef(ctx, input)
	}
	if err != nil || ref.ID == "" {
		return nil, err
	}
	return []provider.Link{{Ref: &ref}}, nil
}

func (a *App) createAsanaTask(ctx context.Context, project, typ, title, description, linkTo string, pin tasktemplate.Ref, token string) (any, error) {
	if typ != "" && !strings.EqualFold(typ, "task") {
		return nil, provider.Unsupported("asana", "task type "+typ)
	}
	t, err := a.ResolveTaskTemplate(pin)
	if err != nil {
		return nil, err
	}
	if project == "" && (t == nil || t.Defaults.Scope == nil) {
		project = a.asanaSettings().Project
	}
	links, err := a.asanaLink(ctx, linkTo)
	if err != nil {
		return nil, err
	}
	d := provider.TaskDraft{Scope: project, Title: title, Description: description, Links: links}
	if t != nil {
		if err := t.CheckSupport(asana.SupportedFields()); err != nil {
			return nil, err
		}
		d, err = t.Resolve(tasktemplate.Fields{Scope: supplied(project), Title: supplied(title), Description: supplied(description), Links: links})
		if err != nil {
			return nil, err
		}
		d.Template = &t.Ref
	}
	if err := asana.ValidateDraft("asana", d); err != nil {
		return nil, err
	}
	if !a.asanaSettings().HasProject(d.Scope) {
		return nil, fmt.Errorf("asana project %q is not configured", d.Scope)
	}
	for _, link := range d.Links {
		if link.Ref.Instance == "asana" {
			if _, err := a.validateAsanaRef(*link.Ref); err != nil {
				return nil, err
			}
		} else if link.Ref.Instance != a.Cfg.Providers.KnowledgeBase.Provider || link.Ref.Instance == "none" {
			return nil, fmt.Errorf("link %s does not match the selected knowledge provider", link.Ref)
		}
	}
	if os.Getenv("DEVFLOW_OPERATION_ID") != "" {
		return nil, provider.Unsupported("asana", "deduplicating task creation by operation ID")
	}
	summary := map[string]any{
		"provider": "asana", "task": d,
		"field_mapping": map[string]string{"scope": "projects", "title": "name", "description": "notes", "template": "notes metadata", "links": "notes metadata"},
	}
	if p, err := a.gate(policy.Remote, "ticket_create", summary, token, true, nil); p != nil || err != nil {
		return p, err
	}
	tasks, err := a.asanaTasks()
	if err != nil {
		return nil, err
	}
	result, err := tasks.Create(ctx, d, provider.WriteOptions{})
	a.audit(policy.Remote, "ticket_create", summary, err)
	if err != nil {
		return nil, err
	}
	return map[string]string{"key": taskKey(result.Ref), "url": result.URL}, nil
}

func (a *App) applyAsanaTransition(ctx context.Context, plan TransitionResult) (TransitionResult, error) {
	ref, err := a.taskRef(ctx, plan.Key)
	if err != nil {
		return plan, err
	}
	if plan.Key != ref.String() || a.taskTransitions(ref.Scope)[plan.Event] != plan.To || plan.To == "none" || plan.From == asana.StateCompleted {
		return plan, errors.New("invalid Asana transition plan")
	}
	tasks, err := a.asanaTasks()
	if err != nil {
		return plan, err
	}
	check := a.checkTaskTemplate(plan.Template)
	opts := provider.WriteOptions{CheckTask: func(task provider.Task) error {
		if task.State.ID != plan.From {
			return fmt.Errorf("asana section changed since preview: %w", provider.ErrConflict)
		}
		return check(task)
	}}
	result, err := tasks.Transition(ctx, ref, provider.TaskState{ID: plan.To, Terminal: plan.To == asana.StateCompleted}, opts)
	a.audit(policy.Remote, "transition", plan, err)
	plan.Applied = err == nil && result.Status == provider.Applied
	if err == nil && result.Status == provider.AlreadyApplied {
		plan.Note = "already " + plan.To
	}
	return plan, err
}
