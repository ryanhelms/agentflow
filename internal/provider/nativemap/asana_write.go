package nativemap

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/asana"
)

func AsanaCreate(draft provider.TaskDraft) (TaskWrite, error) {
	if err := asana.ValidateDraft("asana", draft); err != nil {
		return TaskWrite{}, err
	}
	if !decimalID.MatchString(draft.Scope) {
		return TaskWrite{}, provider.Unsupported("asana", "native creation without a numeric project gid")
	}
	notes, err := asana.EncodeTaskNotes(draft)
	if err != nil {
		return TaskWrite{}, err
	}
	args := map[string]any{"default_project": draft.Scope, "tasks": []any{map[string]any{"name": draft.Title, "project_id": draft.Scope, "notes": notes}}}
	return asanaTaskWrite(draft.Scope, "create_tasks", args, func(raw json.RawMessage) (provider.WriteResult, error) {
		task, err := asanaBatchTask(raw)
		if err != nil {
			return provider.WriteResult{}, err
		}
		if task.Name != "" && task.Name != draft.Title {
			return provider.WriteResult{}, invalidTaskWriteResult(Asana)
		}
		for _, membership := range task.Memberships {
			if membership.Project.GID != draft.Scope {
				return provider.WriteResult{}, invalidTaskWriteResult(Asana)
			}
		}
		ref := provider.Ref{Instance: "asana", Scope: draft.Scope, ID: task.GID}
		return provider.WriteResult{Ref: ref, Status: provider.Applied}, nil
	}), nil
}

func AsanaComplete(ref provider.Ref) (TaskWrite, error) {
	if err := asanaWriteRef(ref); err != nil {
		return TaskWrite{}, err
	}
	args := map[string]any{"tasks": []any{map[string]any{"task_id": ref.ID, "completed": true}}}
	return asanaTaskWrite(ref.Scope, "update_tasks", args, func(raw json.RawMessage) (provider.WriteResult, error) {
		task, err := asanaBatchTask(raw)
		if err != nil {
			return provider.WriteResult{}, err
		}
		if task.GID != ref.ID || (task.Completed != nil && !*task.Completed) {
			return provider.WriteResult{}, invalidTaskWriteResult(Asana)
		}
		if task.Memberships != nil && !slices.ContainsFunc(task.Memberships, func(m asanaMembership) bool { return m.Project.GID == ref.Scope }) {
			return provider.WriteResult{}, invalidTaskWriteResult(Asana)
		}
		return provider.WriteResult{Ref: ref, Status: provider.Applied}, nil
	}), nil
}

func AsanaComment(ref provider.Ref, body string) (TaskWrite, error) {
	if err := asanaWriteRef(ref); err != nil {
		return TaskWrite{}, err
	}
	if strings.TrimSpace(body) == "" {
		return TaskWrite{}, errors.New("asana comment is empty")
	}
	args := map[string]any{"task_id": ref.ID, "text": body}
	return asanaTaskWrite(ref.Scope, "add_comment", args, func(raw json.RawMessage) (provider.WriteResult, error) {
		type story struct {
			GID             string        `json:"gid"`
			Task            asanaResource `json:"task"`
			Target          asanaResource `json:"target"`
			Anchor          string        `json:"anchor_gid"`
			Text            *string       `json:"text"`
			ResourceSubtype string        `json:"resource_subtype"`
		}
		var result struct {
			story
			Data *story `json:"data"`
		}
		if err := decodeObject(raw, &result); err != nil {
			return provider.WriteResult{}, err
		}
		if result.Data != nil {
			if result.GID != "" {
				return provider.WriteResult{}, invalidTaskWriteResult(Asana)
			}
			result.story = *result.Data
		}
		foundTarget := false
		for _, id := range []string{result.Task.GID, result.Target.GID, result.Anchor} {
			if id == "" {
				continue
			}
			if id != ref.ID {
				return provider.WriteResult{}, invalidTaskWriteResult(Asana)
			}
			foundTarget = true
		}
		if !foundTarget || !decimalID.MatchString(result.GID) ||
			(result.Text != nil && *result.Text != body) ||
			(result.ResourceSubtype != "" && result.ResourceSubtype != "comment_added") {
			return provider.WriteResult{}, invalidTaskWriteResult(Asana)
		}
		return provider.WriteResult{Ref: ref, Status: provider.Applied}, nil
	}), nil
}

func asanaTaskWrite(project, tool string, args map[string]any, decode func(json.RawMessage) (provider.WriteResult, error)) TaskWrite {
	write := taskWrite(Asana, tool, args, decode)
	write.call.Scope = project
	return write
}

func asanaWriteRef(ref provider.Ref) error {
	if ref.Instance != "asana" || !decimalID.MatchString(ref.Scope) || !decimalID.MatchString(ref.ID) {
		return provider.Unsupported("asana", "native writes without numeric project and task gids")
	}
	return ref.Validate()
}

func asanaBatchTask(raw json.RawMessage) (asanaTaskFields, error) {
	var result struct {
		Succeeded []struct {
			asanaTaskFields
			TaskID string `json:"task_id"`
		} `json:"succeeded"`
		Failed []json.RawMessage `json:"failed"`
	}
	if err := decodeObject(raw, &result); err != nil {
		return asanaTaskFields{}, err
	}
	if len(result.Succeeded) != 1 || len(result.Failed) != 0 {
		return asanaTaskFields{}, invalidTaskWriteResult(Asana)
	}
	task := result.Succeeded[0]
	if task.GID == "" {
		task.GID = task.TaskID
	}
	if !decimalID.MatchString(task.GID) || (task.TaskID != "" && task.TaskID != task.GID) {
		return asanaTaskFields{}, invalidTaskWriteResult(Asana)
	}
	return task.asanaTaskFields, nil
}
