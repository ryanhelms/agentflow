package twg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/native"
)

type PreparedJiraCreate struct {
	client                      *Jira
	profile                     jiraProfile
	selector, projectID, typeID string
	draft                       provider.TaskDraft
}

func (p *PreparedJiraCreate) Intent() map[string]any {
	login := p.profile.intent(p.selector)
	login["profile_environment"] = p.profile.Environment
	return map[string]any{"backend": "twg", "site": p.client.site, "login": login,
		"project": p.draft.Scope, "project_id": p.projectID, "type": p.draft.Type, "type_id": p.typeID,
		"summary": p.draft.Title, "description": p.draft.Description, "description_format": "plain", "labels": p.draft.Labels}
}

func literalCLIValue(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) &&
		strings.TrimFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }) == value
}

// PrepareCreate resolves both IDs on the selected OAuth profile. TWG accepts
// project names and normalizes issue type names, so dispatch uses the IDs.
func (j *Jira) PrepareCreate(ctx context.Context, draft provider.TaskDraft) (*PreparedJiraCreate, error) {
	if draft.Scope == "" || len(draft.Links) != 0 || draft.Template != nil || draft.Priority != "" {
		return nil, errors.New("twg Jira creation requires an unlinked project draft without a template or priority")
	}
	draft.Scope = strings.ToUpper(draft.Scope)
	if _, err := j.queryScopes(draft.Scope); err != nil {
		return nil, err
	}
	profile, selector, err := j.jiraProfile(ctx)
	if err != nil {
		return nil, err
	}
	if draft.Title == "" || !literalCLIValue(draft.Title) || !literalCLIValue(draft.Description) || jiraMention.MatchString(draft.Description) {
		return nil, errors.New("twg requires literal Jira text without surrounding whitespace, NUL or description mentions")
	}
	for _, label := range draft.Labels {
		if label == "" || !literalCLIValue(label) || strings.ContainsAny(label, ",\\") {
			return nil, errors.New("jira label cannot be passed literally to twg")
		}
	}
	var metadata struct {
		Space      struct{ ID, Key string }
		IssueTypes []struct {
			ID, Name string
			Subtask  *bool
		}
	}
	// TWG slices the returned types to --first without a continuation or total.
	const first = 201
	args := []string{"jira", "space", "issue-types", "--id-or-key", draft.Scope, "--first", strconv.Itoa(first), "--profile", selector}
	var reply envelope
	if err := j.read(ctx, "resolve jira create target", args, &reply); err != nil {
		if errors.Is(err, ErrToolAbsent) {
			return nil, fmt.Errorf("twg disappeared during jira create preparation: %w", ErrFailure)
		}
		return nil, err
	}
	var request struct{ Site, IDOrKey string }
	if reply.Command != "jira.space.issue-types" || json.Unmarshal(reply.Request, &request) != nil ||
		request.Site != j.site || request.IDOrKey != draft.Scope || json.Unmarshal(reply.Data, &metadata) != nil {
		return nil, fmt.Errorf("twg returned metadata for an unverified Jira target: %w", ErrAccessDenied)
	}
	if metadata.Space.Key != draft.Scope || !decimalID.MatchString(metadata.Space.ID) {
		return nil, fmt.Errorf("twg returned a different or incomplete Jira project: %w", ErrAccessDenied)
	}
	if metadata.IssueTypes == nil || len(metadata.IssueTypes) >= first {
		return nil, fmt.Errorf("twg returned incomplete Jira issue type metadata: %w", ErrFailure)
	}
	var typeID string
	for _, candidate := range metadata.IssueTypes {
		if !strings.EqualFold(candidate.Name, draft.Type) {
			continue
		}
		if typeID != "" || candidate.Subtask == nil || *candidate.Subtask || !decimalID.MatchString(candidate.ID) {
			return nil, fmt.Errorf("ambiguous or unsupported Jira issue type: %w", ErrAccessDenied)
		}
		typeID = candidate.ID
	}
	if typeID == "" {
		return nil, fmt.Errorf("issue type is unavailable in the requested Jira project: %w", ErrAccessDenied)
	}
	draft.Labels = slices.Clone(draft.Labels)
	return &PreparedJiraCreate{client: j, profile: profile, selector: selector, projectID: metadata.Space.ID, typeID: typeID, draft: draft}, nil
}

func (p *PreparedJiraCreate) Apply(ctx context.Context) (provider.Ref, error) {
	args := []string{"jira", "workitem", "create", "--space", p.projectID, "--type", p.typeID, "--summary=" + p.draft.Title}
	if p.draft.Description != "" {
		args = append(args, "--description="+p.draft.Description, "--description-format", "plain")
	}
	if len(p.draft.Labels) != 0 {
		args = append(args, "--labels="+strings.Join(p.draft.Labels, ","))
	}
	args = append(args, "--profile", p.selector, "--yes")
	var reply envelope
	started, err := p.client.run(ctx, "create jira work item", args, &reply)
	if !started {
		return provider.Ref{}, errors.Join(native.ErrWriteNotDispatched, err)
	}
	if err == nil {
		var ref provider.Ref
		ref, err = p.verifyCreated(ctx, reply)
		if err == nil {
			return ref, nil
		}
	}
	return provider.Ref{}, fmt.Errorf("inspect the Jira project before approving another creation: %w", errors.Join(native.ErrWriteUncertain, err))
}

// TWG's plain format wraps the literal string in one unmarked text node,
// including any embedded newlines.
func plainCreateDescription(text string) any {
	if text == "" {
		return nil
	}
	node := map[string]any{"type": "text", "text": text}
	paragraph := map[string]any{"type": "paragraph", "content": []any{node}}
	return map[string]any{"type": "doc", "version": float64(1), "content": []any{paragraph}}
}

// TWG includes the resolved input in its acknowledgement. Reject extra
// fields (such as parent or assignee) before accepting the resulting key.
func (p *PreparedJiraCreate) verifyCreated(ctx context.Context, reply envelope) (provider.Ref, error) {
	var request struct{ Input json.RawMessage }
	var input struct {
		Site, ProjectRef, IssueTypeRef, Summary string
		Description                             any
		Labels                                  []string
	}
	var result struct {
		Success bool
		Issue   struct{ ID, Key, Self string }
	}
	resultDecoder := json.NewDecoder(bytes.NewReader(reply.Data))
	resultDecoder.DisallowUnknownFields()
	if reply.Command != "jira.workitem.create" || json.Unmarshal(reply.Request, &request) != nil ||
		resultDecoder.Decode(&result) != nil || !result.Success || !decimalID.MatchString(result.Issue.ID) ||
		jiraScope(result.Issue.Key) != p.draft.Scope || result.Issue.Self == "" {
		return provider.Ref{}, fmt.Errorf("invalid Jira creation acknowledgement: %w", ErrFailure)
	}
	decoder := json.NewDecoder(bytes.NewReader(request.Input))
	decoder.DisallowUnknownFields()
	description := plainCreateDescription(p.draft.Description)
	if decoder.Decode(&input) != nil || input.Site != p.client.site || input.ProjectRef != p.projectID ||
		input.IssueTypeRef != p.typeID || input.Summary != p.draft.Title ||
		!slices.Equal(input.Labels, p.draft.Labels) || !reflect.DeepEqual(input.Description, description) {
		return provider.Ref{}, fmt.Errorf("Jira creation acknowledgement differs from the approved input: %w", ErrFailure)
	}
	var rows []struct {
		ID, Key, Self, URL, Summary string
		Creator                     struct{ AccountID string }
		Project, IssueType          named
		Description                 json.RawMessage
		Labels                      []string
		Error                       json.RawMessage
	}
	args := []string{"jira", "workitem", "get", result.Issue.Key, "--fields", "project,issuetype,summary,description,labels,creator", "--profile", p.selector}
	if err := p.client.read(ctx, "verify created jira issue", args, &rows); err != nil {
		return provider.Ref{}, err
	}
	if len(rows) != 1 {
		return provider.Ref{}, fmt.Errorf("expected one created Jira issue: %w", ErrFailure)
	}
	row := rows[0]
	if present(row.Error) {
		return provider.Ref{}, classify(row.Error)
	}
	var actual any
	if row.ID != result.Issue.ID || row.Key != result.Issue.Key || row.Self != result.Issue.Self ||
		row.Creator.AccountID != p.profile.UserID || row.URL != p.client.site+"/browse/"+row.Key ||
		row.Project.ID != p.projectID || row.Project.Key != p.draft.Scope || row.IssueType.ID != p.typeID || !strings.EqualFold(row.IssueType.Name, p.draft.Type) ||
		row.Summary != p.draft.Title || row.Labels == nil || !slices.Equal(row.Labels, p.draft.Labels) ||
		json.Unmarshal(row.Description, &actual) != nil || !reflect.DeepEqual(actual, description) {
		return provider.Ref{}, fmt.Errorf("created Jira issue does not match the approved draft: %w", ErrFailure)
	}
	return p.client.ref(p.draft.Scope, row.Key), nil
}
