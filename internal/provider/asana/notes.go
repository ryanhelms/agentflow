package asana

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
)

// Asana notes are plain text. A single reserved first line round-trips the
// template pin and qualified links alongside the editable task description.
const notesPrefix = "devflow-task-v1: "

type metadata struct {
	Template *provider.TaskTemplateRef `json:"template,omitempty"`
	Links    []provider.Link           `json:"links,omitempty"`
}

func (m metadata) validate() error {
	if m.Template != nil {
		if err := m.Template.Validate(); err != nil {
			return err
		}
	}
	for _, link := range m.Links {
		if link.Ref == nil {
			return provider.Unsupported("asana", "link without a qualified reference")
		}
		if err := link.Ref.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func SupportedFields() []string { return []string{"scope", "title", "description", "links"} }

func ValidateDraft(instance string, draft provider.TaskDraft) error {
	if draft.Type != "" || draft.Priority != "" || len(draft.Labels) > 0 {
		return provider.Unsupported(instance, "creating tasks with type, priority or labels")
	}
	if !ValidGID(draft.Scope) || strings.TrimSpace(draft.Title) == "" {
		return errors.New("asana task creation requires a project gid and title")
	}
	return (metadata{draft.Template, draft.Links}).validate()
}

func encodeNotes(draft provider.TaskDraft) (string, error) {
	if draft.Template == nil && len(draft.Links) == 0 && !strings.HasPrefix(draft.Description, notesPrefix) {
		return draft.Description, nil
	}
	line, err := json.Marshal(metadata{draft.Template, draft.Links})
	if err != nil {
		return "", err
	}
	return notesPrefix + string(line) + "\n\n" + draft.Description, nil
}

func decodeNotes(notes string) (string, metadata, error) {
	encoded, ok := strings.CutPrefix(notes, notesPrefix)
	if !ok {
		return notes, metadata{}, nil
	}
	line, body, _ := strings.Cut(encoded, "\n")
	var m metadata
	if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &m) != nil {
		return "", m, errors.New("invalid devflow metadata in asana notes")
	}
	return strings.TrimPrefix(body, "\n"), m, m.validate()
}

// DecodeTaskNotes extracts Devflow's task template and links from provider notes.
func DecodeTaskNotes(notes string) (string, *provider.TaskTemplateRef, []provider.Link, error) {
	body, meta, err := decodeNotes(notes)
	return body, meta.Template, meta.Links, err
}
