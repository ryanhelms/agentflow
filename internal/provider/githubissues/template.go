package githubissues

import (
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/nscaledev/agentflow/internal/provider"
)

const templatePrefix = "<!-- agentflow:github-issues:template:v1:"

// An envelope occupies the first line, followed by EOF or one blank line.
// A template envelope can also carry the operation key; its body is always literal.
var templateHeader = regexp.MustCompile(`^<!-- agentflow:github-issues:template:v1:([0-9a-f]+):([0-9a-f]+)(?::([0-9a-f]{64}))? -->(?:\n\n|$)`)

func templateMarker(ref provider.TaskTemplateRef, operation string) string {
	header := templatePrefix + hex.EncodeToString([]byte(ref.Name)) + ":" + hex.EncodeToString([]byte(ref.Revision))
	if operation != "" {
		header += ":" + operation
	}
	return header + markerSuffix
}

func issueBody(body string) (string, *provider.TaskTemplateRef, error) {
	if !strings.HasPrefix(body, templatePrefix) {
		return plainBody(body), nil, nil
	}
	matches := templateHeader.FindStringSubmatch(body)
	if matches == nil {
		return "", nil, errors.New("invalid github issue template marker")
	}
	name, nameErr := hex.DecodeString(matches[1])
	revision, revisionErr := hex.DecodeString(matches[2])
	if nameErr != nil || revisionErr != nil || !utf8.Valid(name) || !utf8.Valid(revision) {
		return "", nil, errors.New("invalid github issue template marker encoding")
	}
	ref := provider.TaskTemplateRef{Name: string(name), Revision: string(revision)}
	if err := ref.Validate(); err != nil {
		return "", nil, err
	}
	return body[len(matches[0]):], &ref, nil
}

// SupportedFields lists canonical fields carried through both Create and Get.
func SupportedFields() []string { return []string{"scope", "title", "description", "labels"} }

// ValidateDraft also rejects unsupported optional defaults before approval.
func ValidateDraft(instance string, draft provider.TaskDraft) error {
	if draft.Type != "" || draft.Priority != "" || len(draft.Links) != 0 {
		return provider.Unsupported(instance, "creating github issues with type, priority or structured links")
	}
	if strings.TrimSpace(draft.Title) == "" {
		return errors.New("github issue title is required")
	}
	if draft.Template != nil {
		if !utf8.ValidString(draft.Template.Name) || !utf8.ValidString(draft.Template.Revision) {
			return errors.New("github issue template reference requires valid UTF-8")
		}
		return draft.Template.Validate()
	}
	return nil
}

func checkTask(task provider.Task, options provider.WriteOptions) error {
	if options.CheckTask != nil {
		return options.CheckTask(task)
	}
	if task.Template != nil {
		return errors.New("github issue requires validation against its stored template")
	}
	return nil
}

// DecodeIssueBody extracts a task template from the provider body.
func DecodeIssueBody(body string) (string, *provider.TaskTemplateRef, error) {
	return issueBody(body)
}

// EncodeIssueBody protects user content that resembles an internal marker.
func EncodeIssueBody(body string) string { return markedBody(body, "") }
