package nativemap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
)

func jiraCall(site, tool string, args map[string]any) (native.Call, error) {
	origin, err := httpsOrigin(site)
	if err != nil {
		return native.Call{}, err
	}
	args["cloudId"] = origin
	return native.Call{Provider: string(Jira), Server: "atlassian", Tool: tool, Arguments: args}, nil
}

func jiraIssueCall(site, tool string, ref provider.Ref, args map[string]any) (native.Call, error) {
	if err := ref.Validate(); err != nil {
		return native.Call{}, err
	}
	if !jiraIssueKey.MatchString(ref.ID) || !strings.EqualFold(ref.ID[:strings.LastIndexByte(ref.ID, '-')], ref.Scope) {
		return native.Call{}, errors.New("jira requires an issue key in the requested project")
	}
	args["issueIdOrKey"] = ref.ID
	return jiraCall(site, tool, args)
}

func JiraRemoteLinks(site string, ref provider.Ref) (native.Call, error) {
	return jiraIssueCall(site, "listJiraIssueRemoteIssueLinks", ref, map[string]any{})
}

// JiraDeferredRead uses Rovo's read pathway on the standard v2 endpoint.
func JiraDeferredRead(call native.Call) native.Call {
	inputs := make(map[string]any, len(call.Arguments))
	for name, value := range call.Arguments {
		if name != "cloudId" {
			inputs[name] = value
		}
	}
	return native.Call{Provider: call.Provider, Server: call.Server, Tool: "executeRead", Arguments: map[string]any{
		"name": call.Tool, "cloudId": call.Arguments["cloudId"], "inputs": inputs,
	}}
}

// DecodeJiraRemoteLinks projects Jira's native web links. It also accepts the
// REST list shape used by Atlassian for Jira remote link objects.
func DecodeJiraRemoteLinks(raw json.RawMessage) ([]atlassian.Page, error) {
	if len(raw) > MaxResultBytes {
		return nil, ErrInvalidResult
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		var reply struct {
			RemoteLinks json.RawMessage `json:"remoteLinks"`
		}
		if err := decodeObject(raw, &reply); err != nil {
			return nil, err
		}
		raw = reply.RemoteLinks
	}
	var rows []struct {
		Object struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"object"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil || rows == nil {
		return nil, fmt.Errorf("%w: expected a jira remote link list", ErrInvalidResult)
	}
	pages := make([]atlassian.Page, 0, len(rows))
	for _, row := range rows {
		if remote := row.Object; validJiraRemoteURL(remote.URL) {
			pages = append(pages, atlassian.Page{Title: remote.Title, URL: remote.URL})
		}
	}
	return pages, nil
}

func validJiraRemoteURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}

func JiraLinkRemote(site string, ref provider.Ref, linkURL, title string) (native.Call, error) {
	if !validJiraRemoteURL(linkURL) {
		return native.Call{}, errors.New("jira remote links require an absolute HTTP or HTTPS url without credentials")
	}
	return jiraIssueCall(site, "createJiraIssueRemoteIssueLink", ref, map[string]any{"url": linkURL, "title": title})
}

func JiraCreate(site string, draft provider.TaskDraft) (native.Call, error) {
	// A link to an existing issue is a distinct ordered write; its exact
	// target (the new issue) cannot be bound before creation.
	if len(draft.Links) != 0 || draft.Priority != "" {
		return native.Call{}, native.ErrUnavailable
	}
	args := map[string]any{"projectKey": draft.Scope, "issueType": draft.Type, "summary": draft.Title, "labels": draft.Labels}
	if draft.Description != "" {
		args["description"] = jiraTextHTML(draft.Description)
		args["contentFormat"] = "html"
	}
	return jiraCall(site, "createJiraIssue", args)
}

const JiraProjectIssueTypesTool = "listJiraProjectIssueTypesMetadata"

func JiraProjectIssueTypes(site, project string) (native.Call, error) {
	return jiraCall(site, JiraProjectIssueTypesTool, map[string]any{
		"projectIdOrKey": project, "maxResults": 200,
	})
}

func CheckJiraCreateType(raw json.RawMessage, issueType string) error {
	var result struct {
		IssueTypes []struct {
			Name    string `json:"name"`
			Subtask *bool  `json:"subtask"`
		} `json:"issueTypes"`
		StartAt int  `json:"startAt"`
		Total   *int `json:"total"`
	}
	if err := decodeObject(raw, &result); err != nil {
		return err
	}
	if result.IssueTypes == nil || result.Total == nil || result.StartAt != 0 || *result.Total != len(result.IssueTypes) {
		return fmt.Errorf("%w: jira returned incomplete issue type metadata", ErrInvalidResult)
	}
	matches := 0
	for _, candidate := range result.IssueTypes {
		if strings.EqualFold(candidate.Name, issueType) && candidate.Subtask != nil && !*candidate.Subtask {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("%w: issue type %q is not uniquely available as a standard issue in the requested jira project", native.ErrDenied, issueType)
	}
	return nil
}

func JiraComment(site string, ref provider.Ref, body string) (native.Call, error) {
	return jiraIssueCall(site, "addOrEditJiraIssueComment", ref, map[string]any{"commentBody": jiraTextHTML(body), "contentFormat": "html"})
}

// DecodeJiraCommentReceipt verifies the flat Rovo acknowledgement for a
// create-only call on an authenticated, issue-bound session.
func DecodeJiraCommentReceipt(raw json.RawMessage, ref provider.Ref, text string) (string, error) {
	var receipt struct {
		CommentID    string `json:"commentId"`
		IssueKey     string `json:"issueKey"`
		IssueIDOrKey string `json:"issueIdOrKey"`
		Body         string `json:"body"`
		Format       string `json:"appliedContentFormat"`
		OK           *bool  `json:"ok"`
	}
	if err := decodeObject(raw, &receipt); err != nil {
		return "", err
	}
	if !decimalID.MatchString(receipt.CommentID) || receipt.Format != "html" || receipt.Body != jiraTextHTML(text) ||
		receipt.OK != nil && !*receipt.OK ||
		receipt.IssueKey != "" && !strings.EqualFold(receipt.IssueKey, ref.ID) ||
		receipt.IssueIDOrKey != "" && !strings.EqualFold(receipt.IssueIDOrKey, ref.ID) {
		return "", fmt.Errorf("%w: could not verify the Jira comment receipt; inspect the issue before approving another comment", ErrInvalidResult)
	}
	return receipt.CommentID, nil
}

// Preserve existing plain-text semantics when a native tool accepts rich text.
func jiraTextHTML(body string) string {
	return "<p>" + strings.ReplaceAll(html.EscapeString(body), "\n", "<br>") + "</p>"
}

func JiraWriteIssue(site string, ref provider.Ref) (native.Call, error) {
	return jiraIssueCall(site, "getJiraIssue", ref, map[string]any{
		"fields":       []string{"summary", "status", "project", "assignee"},
		"fieldsByKeys": true,
	})
}

// CheckJiraIssueIdentity binds an operation that needs the issue and project,
// without requiring workflow fields such as the status category.
func CheckJiraIssueIdentity(raw json.RawMessage, ref provider.Ref) error {
	var issue struct {
		Key    string `json:"key"`
		Fields struct {
			Project struct {
				Key string `json:"key"`
			} `json:"project"`
		} `json:"fields"`
	}
	if err := decodeObject(raw, &issue); err != nil {
		return err
	}
	if !strings.EqualFold(issue.Key, ref.ID) ||
		(issue.Fields.Project.Key != "" && !strings.EqualFold(issue.Fields.Project.Key, ref.Scope)) {
		return fmt.Errorf("jira returned a different issue or project: %w", provider.ErrConflict)
	}
	return nil
}

type JiraIssueSnapshot struct {
	Title     string
	Status    atlassian.Status
	AccountID string
}

func DecodeJiraWriteIssue(raw json.RawMessage, ref provider.Ref) (JiraIssueSnapshot, error) {
	var issue struct {
		Key    string `json:"key"`
		Fields struct {
			Summary  string           `json:"summary"`
			Status   atlassian.Status `json:"status"`
			Assignee *atlassian.User  `json:"assignee"`
			Project  struct {
				Key string `json:"key"`
			} `json:"project"`
		} `json:"fields"`
	}
	if err := decodeObject(raw, &issue); err != nil {
		return JiraIssueSnapshot{}, err
	}
	fields := issue.Fields
	if !strings.EqualFold(issue.Key, ref.ID) || (fields.Project.Key != "" && !strings.EqualFold(fields.Project.Key, ref.Scope)) {
		return JiraIssueSnapshot{}, fmt.Errorf("jira returned a different issue or project: %w", provider.ErrConflict)
	}
	if strings.TrimSpace(fields.Summary) == "" || strings.TrimSpace(fields.Status.Name) == "" || fields.Status.Category.Key == "" {
		return JiraIssueSnapshot{}, fmt.Errorf("%w: jira issue omitted its summary or status", ErrInvalidResult)
	}
	snapshot := JiraIssueSnapshot{Title: fields.Summary, Status: fields.Status}
	if fields.Assignee != nil {
		snapshot.AccountID = fields.Assignee.AccountID
	}
	return snapshot, nil
}

func JiraCurrentUser(site string) (native.Call, error) {
	return jiraCall(site, "getJiraCurrentUser", map[string]any{})
}

func DecodeJiraCurrentUser(raw json.RawMessage) (string, error) {
	var user atlassian.User
	if err := decodeObject(raw, &user); err != nil {
		return "", err
	}
	if !validName(user.AccountID) {
		return "", fmt.Errorf("%w: jira current user omitted its account id", ErrInvalidResult)
	}
	return user.AccountID, nil
}

func JiraAssign(site string, ref provider.Ref, accountID string) (native.Call, error) {
	return jiraIssueCall(site, "editJiraIssue", ref, map[string]any{"assignee": accountID})
}

func JiraAvailableTransitions(site string, ref provider.Ref) (native.Call, error) {
	return jiraIssueCall(site, "listJiraIssueTransitions", ref, map[string]any{})
}

type JiraTransition struct {
	atlassian.Transition
	Available *bool `json:"isAvailable"`
}

func DecodeJiraTransitions(raw json.RawMessage) ([]JiraTransition, error) {
	var reply struct {
		Transitions []JiraTransition `json:"transitions"`
	}
	if err := decodeObject(raw, &reply); err != nil {
		return nil, err
	}
	if reply.Transitions == nil {
		return nil, fmt.Errorf("%w: jira result omitted its transition list", ErrInvalidResult)
	}
	return reply.Transitions, nil
}

func JiraMove(site string, ref provider.Ref, transitionID string) (native.Call, error) {
	return jiraIssueCall(site, "transitionJiraIssue", ref, map[string]any{"transitionId": transitionID})
}

func DecodeJiraCreated(raw json.RawMessage, instance, project string) (provider.Ref, error) {
	var issue struct {
		Key string `json:"key"`
	}
	if err := decodeObject(raw, &issue); err != nil {
		return provider.Ref{}, err
	}
	if !jiraIssueKey.MatchString(issue.Key) ||
		!strings.EqualFold(issue.Key[:strings.LastIndexByte(issue.Key, '-')], project) {
		return provider.Ref{}, fmt.Errorf("%w: jira creation omitted a key in the requested project", ErrInvalidResult)
	}
	ref := provider.Ref{Instance: instance, Scope: project, ID: issue.Key}
	return ref, ref.Validate()
}

// CheckJiraWriteResult catches structured errors delivered as MCP success data.
// Some Jira operations return no result fields apart from the acknowledgement.
func CheckJiraWriteResult(raw json.RawMessage) error {
	if len(raw) > MaxResultBytes || !json.Valid(raw) {
		return ErrInvalidResult
	}
	var result struct {
		Error        json.RawMessage   `json:"error"`
		Errors       map[string]any    `json:"errors"`
		Messages     []string          `json:"errorMessages"`
		ToolMessages []json.RawMessage `json:"content"`
		Code         json.RawMessage   `json:"code"`
		Status       json.RawMessage   `json:"status"`
		Success      *bool             `json:"success"`
	}
	if err := decodeObject(raw, &result); err != nil {
		return err
	}
	if (len(result.Error) > 0 && string(result.Error) != "null") || len(result.Errors) > 0 ||
		len(result.Messages) > 0 || jiraFailureStatus(result.Code) || jiraFailureStatus(result.Status) ||
		(result.Success != nil && !*result.Success) || len(result.ToolMessages) > 0 {
		return fmt.Errorf("%w: jira returned an error or an unrecognized acknowledgement", ErrInvalidResult)
	}
	return nil
}

func jiraFailureStatus(raw json.RawMessage) bool {
	value := strings.ToLower(strings.Trim(string(raw), `"`))
	status, _ := strconv.Atoi(value)
	return status >= 400 || value == "error" || value == "failed" || value == "failure"
}
