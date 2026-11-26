package app

import (
	"errors"
	"strings"

	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
)

// FileSystem and Repository round-trip the same task fields in Markdown.
var fileTaskFields = []string{"scope", "title", "description", "type", "priority", "labels", "links"}

func (a *App) resolveFileTaskDraft(d provider.TaskDraft, ref tasktemplate.Ref) (provider.TaskDraft, error) {
	t, err := a.ResolveTaskTemplate(ref)
	if err != nil {
		return d, err
	}
	if d.Scope == "" && (t == nil || t.Defaults.Scope == nil) {
		d.Scope = a.fileScope()
	}
	if d.Type == "" && (t == nil || t.Defaults.Type == nil) {
		d.Type = "Task"
	}
	if t != nil {
		if err := t.CheckSupport(fileTaskFields); err != nil {
			return d, err
		}
		inputs := tasktemplate.Fields{
			Scope: supplied(d.Scope), Title: supplied(d.Title), Description: supplied(d.Description),
			Type: supplied(d.Type), Links: d.Links,
		}
		d, err = t.Resolve(inputs)
		if err != nil {
			return d, err
		}
		d.Template = &t.Ref
	}
	if strings.TrimSpace(d.Title) == "" {
		return d, errors.New("task title is required")
	}
	instance := a.Cfg.Providers.TaskManagement.Provider
	return d, (provider.Ref{Instance: instance, Scope: d.Scope, ID: "_"}).Validate()
}

// CreateTicket passes an empty string when the caller omits a field.
func supplied(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
