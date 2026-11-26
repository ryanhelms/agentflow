package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/asana"
	"github.com/ryanhelms/agentflow/internal/provider/githubissues"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
)

// ResolveTaskTemplate selects an exact ref from the repository's configured catalog.
// On creation, pass a zero ref to use the configured default. A nil result and nil
// error mean no template applies, so the caller can continue its existing task path.
// For an existing task, call only when it has a stored ref and pass that ref here.
func (a *App) ResolveTaskTemplate(ref tasktemplate.Ref) (*tasktemplate.Template, error) {
	settings := a.Cfg.Providers.TaskTemplates
	if ref == (tasktemplate.Ref{}) {
		if settings == nil || settings.Default == nil {
			return nil, nil
		}
		ref = *settings.Default
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if settings == nil {
		return nil, fmt.Errorf("task_management.templates.catalog is required to select a task template")
	}
	path, err := settings.CatalogPath()
	if err != nil {
		return nil, fmt.Errorf("task_management.templates.catalog: %w", err)
	}
	catalog, err := readTaskTemplateCatalog(a.Repo.Dir, path)
	if err != nil {
		return nil, fmt.Errorf("task template catalog %q: %w", settings.Catalog, err)
	}
	template, err := catalog.Select(ref)
	if err != nil {
		return nil, fmt.Errorf("task template catalog %q: %w", settings.Catalog, err)
	}
	return &template, nil
}

func readTaskTemplateCatalog(repo, path string) (tasktemplate.Catalog, error) {
	root, err := os.OpenRoot(repo)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("expected a regular file")
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return tasktemplate.DecodeCatalog(f)
}

// TaskTemplateSnapshot binds approval to a pinned template and the task fields it validates.
type TaskTemplateSnapshot struct {
	tasktemplate.Ref
	ContentHash string `json:"content_hash"`
}

// IssueTemplateSnapshot is the original name used by GitHub Issues callers.
type IssueTemplateSnapshot = TaskTemplateSnapshot

func (a *App) validateTaskTemplate(task provider.Task) (*TaskTemplateSnapshot, error) {
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
	d := provider.TaskDraft{
		Scope: task.Ref.Scope, Title: task.Title, Description: task.Description, Labels: slices.Clone(task.Labels),
	}
	fields := githubissues.SupportedFields()
	if a.usesAsana() {
		fields, d.Links = asana.SupportedFields(), task.Links
	}
	if a.usesFileTasks() {
		fields = fileTaskFields
		d.Type, d.Priority, d.Links = task.Type, task.Priority, task.Links
	}
	if err := t.CheckSupport(fields); err != nil {
		return nil, err
	}
	if err := t.Validate(d); err != nil {
		return nil, fmt.Errorf("task %s, template %q revision %q: %w", taskKey(task.Ref), t.Name, t.Revision, err)
	}
	slices.Sort(d.Labels)
	content, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return &TaskTemplateSnapshot{Ref: t.Ref, ContentHash: fmt.Sprintf("%x", sha256.Sum256(content))}, nil
}

func (a *App) checkTaskTemplate(expected *TaskTemplateSnapshot) func(provider.Task) error {
	return func(task provider.Task) error {
		current, err := a.validateTaskTemplate(task)
		if err != nil {
			return err
		}
		if expected == nil && current == nil || expected != nil && current != nil && *expected == *current {
			return nil
		}
		return fmt.Errorf("task template or content changed since preview: %w", provider.ErrConflict)
	}
}
