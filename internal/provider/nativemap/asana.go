package nativemap

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/asana"
)

// AsanaTask maps a task GID in a project GID. The caller selects the
// workspace-scoped Asana v2 session for the reference's instance.
func AsanaTask(ref provider.Ref) (Read[provider.Task], error) {
	if err := ref.Validate(); err != nil {
		return Read[provider.Task]{}, err
	}
	if !decimalID.MatchString(ref.ID) || !decimalID.MatchString(ref.Scope) {
		return Read[provider.Task]{}, errors.New("asana read requires a numeric task gid and project gid")
	}
	input := struct {
		TaskGID         string `json:"task_gid"`
		IncludeComments bool   `json:"include_comments"`
		IncludeSubtasks bool   `json:"include_subtasks"`
	}{TaskGID: ref.ID}
	return newRead(Asana, GetTask, ref, input, func(raw json.RawMessage) (provider.Task, error) {
		return asanaTask(ref, raw)
	})
}

type asanaResource struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type asanaMembership struct {
	Project asanaResource `json:"project"`
	Section asanaResource `json:"section"`
}

type asanaTaskFields struct {
	GID         string            `json:"gid"`
	Name        string            `json:"name"`
	Notes       string            `json:"notes"`
	Permalink   string            `json:"permalink_url"`
	Subtype     string            `json:"resource_subtype"`
	Completed   *bool             `json:"completed"`
	ModifiedAt  string            `json:"modified_at"`
	Assignee    asanaResource     `json:"assignee"`
	Memberships []asanaMembership `json:"memberships"`
}

func asanaTask(ref provider.Ref, raw json.RawMessage) (provider.Task, error) {
	var result struct {
		asanaTaskFields
		Data *asanaTaskFields `json:"data"`
	}
	if err := decodeObject(raw, &result); err != nil {
		return provider.Task{}, err
	}
	task := result.asanaTaskFields
	if result.Data != nil {
		if task.GID != "" {
			return provider.Task{}, fmt.Errorf("%w: ambiguous asana task envelope", ErrInvalidResult)
		}
		task = *result.Data
	}
	if task.GID != "" && task.GID != ref.ID {
		return provider.Task{}, fmt.Errorf("asana returned a different task gid: %w", provider.ErrConflict)
	}
	if task.GID == "" || task.Completed == nil || strings.TrimSpace(task.Name) == "" || task.Memberships == nil {
		return provider.Task{}, fmt.Errorf("%w: asana task requires gid, name, completion and memberships", ErrInvalidResult)
	}
	for _, membership := range task.Memberships {
		if membership.Project.GID != ref.Scope {
			continue
		}
		state := provider.TaskState{ID: "open", Name: "Open"}
		switch {
		case *task.Completed:
			state = provider.TaskState{ID: "completed", Name: "Completed", Terminal: true}
		case membership.Section.GID != "":
			if !decimalID.MatchString(membership.Section.GID) || membership.Section.Name == "" {
				return provider.Task{}, fmt.Errorf("%w: invalid asana section", ErrInvalidResult)
			}
			state = provider.TaskState{ID: membership.Section.GID, Name: membership.Section.Name}
		}
		taskURL, ok := httpsURL(task.Permalink)
		permalink := ""
		if ok && strings.EqualFold(taskURL.Host, "app.asana.com") {
			permalink = taskURL.String()
		}
		notes, template, links, err := asana.DecodeTaskNotes(task.Notes)
		if err != nil {
			return provider.Task{}, err
		}
		return provider.Task{
			Ref: ref, Title: task.Name, URL: permalink, Description: notes, Template: template, Links: links,
			State: state, Type: task.Subtype, Assignee: task.Assignee.GID, Revision: task.ModifiedAt,
		}, nil
	}
	return provider.Task{}, fmt.Errorf("asana task is outside the requested project: %w", provider.ErrNotFound)
}
