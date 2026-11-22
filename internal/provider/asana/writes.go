package asana

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/nscaledev/devflow/internal/provider"
)

func (a *Adapter) checkWriteOptions(options provider.WriteOptions) error {
	if options.ExpectedRevision != "" {
		return provider.Unsupported(a.instance, "conditional task writes")
	}
	if options.OperationID != "" {
		return provider.Unsupported(a.instance, "task writes with an operation id")
	}
	return nil
}

func (a *Adapter) Create(ctx context.Context, draft provider.TaskDraft, options provider.WriteOptions) (provider.WriteResult, error) {
	if err := a.checkWriteOptions(options); err != nil {
		return provider.WriteResult{}, err
	}
	if err := ValidateDraft(a.instance, draft); err != nil {
		return provider.WriteResult{}, err
	}
	notes, err := EncodeTaskNotes(draft)
	if err != nil {
		return provider.WriteResult{}, err
	}
	payload := struct {
		Name     string   `json:"name"`
		Notes    string   `json:"notes,omitempty"`
		Projects []string `json:"projects"`
	}{Name: draft.Title, Notes: notes, Projects: []string{draft.Scope}}
	result, err := request[taskData](ctx, a, http.MethodPost, "/tasks", url.Values{"opt_fields": {"gid,permalink_url"}}, payload)
	if err != nil {
		return provider.WriteResult{}, err
	}
	if !ValidGID(result.Data.GID) {
		return provider.WriteResult{}, errors.New("asana task creation returned no valid gid")
	}
	return provider.WriteResult{
		Ref:    provider.Ref{Instance: a.instance, Scope: draft.Scope, ID: result.Data.GID},
		Status: provider.Applied,
		URL:    result.Data.PermalinkURL,
	}, nil
}

// Transition accepts StateCompleted or a section GID configured for ref.Scope.
// Completion is global to the task and terminal in this adapter.
func (a *Adapter) Transition(ctx context.Context, ref provider.Ref, target provider.TaskState, options provider.WriteOptions) (provider.WriteResult, error) {
	if err := a.checkWriteOptions(options); err != nil {
		return provider.WriteResult{}, err
	}
	if err := a.validateRef(ref); err != nil {
		return provider.WriteResult{}, err
	}
	complete := target.ID == StateCompleted
	if !complete {
		if _, allowed := a.sections[ref.Scope][target.ID]; !allowed || target.Terminal {
			return provider.WriteResult{}, provider.Unsupported(a.instance, "transition to an unconfigured section or combine a move with completion")
		}
	}
	task, err := a.Get(ctx, ref)
	if err != nil {
		return provider.WriteResult{}, err
	}
	result := provider.WriteResult{Ref: ref, URL: task.URL, Status: provider.AlreadyApplied}
	if task.State.ID == target.ID {
		return result, nil
	}
	if task.State.Terminal {
		return provider.WriteResult{}, provider.Unsupported(a.instance, "transition out of completion")
	}
	if options.CheckTask != nil {
		if err := options.CheckTask(task); err != nil {
			return provider.WriteResult{}, err
		}
	} else if task.Template != nil {
		return provider.WriteResult{}, errors.New("asana task requires validation against its stored template")
	}
	if complete {
		params := url.Values{"opt_fields": {"gid,completed,permalink_url"}}
		updated, err := request[taskData](ctx, a, http.MethodPut, taskPath(ref.ID), params, struct {
			Completed bool `json:"completed"`
		}{Completed: true})
		if err != nil {
			return provider.WriteResult{}, err
		}
		if updated.Data.GID != ref.ID || updated.Data.Completed == nil || !*updated.Data.Completed {
			return provider.WriteResult{}, errors.New("asana task response did not confirm completion")
		}
		if updated.Data.PermalinkURL != "" {
			result.URL = updated.Data.PermalinkURL
		}
	} else if err := a.moveToSection(ctx, ref, target.ID); err != nil {
		return provider.WriteResult{}, err
	}
	result.Status = provider.Applied
	return result, nil
}

func (a *Adapter) moveToSection(ctx context.Context, ref provider.Ref, sectionGID string) error {
	path := "/sections/" + url.PathEscape(sectionGID)
	if err := a.checkSection(ctx, ref.Scope, sectionGID); err != nil {
		return err
	}
	_, err := request[struct{}](ctx, a, http.MethodPost, path+"/addTask", nil, struct {
		Task string `json:"task"`
	}{Task: ref.ID})
	return err
}

func (a *Adapter) checkSection(ctx context.Context, project, sectionGID string) error {
	path := "/sections/" + url.PathEscape(sectionGID)
	section, err := request[struct {
		GID     string   `json:"gid"`
		Project resource `json:"project"`
	}](ctx, a, http.MethodGet, path, url.Values{"opt_fields": {"gid,project.gid"}}, nil)
	if err != nil {
		return err
	}
	if section.Data.GID != sectionGID || section.Data.Project.GID != project {
		return fmt.Errorf("asana section %q is outside project %q: %w", sectionGID, project, provider.ErrNotFound)
	}
	return nil
}
