package nativemap

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/provider"
)

var jiraIssueKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*-[0-9]+$`)

// JiraIssue maps a project-key and issue-key reference on a configured site.
// The site origin serves as Rovo's cloudId and anchors the human-facing URL.
func JiraIssue(ref provider.Ref, siteOrigin string) (Read[provider.Task], error) {
	if err := ref.Validate(); err != nil {
		return Read[provider.Task]{}, err
	}
	if !jiraIssueKey.MatchString(ref.ID) || len(ref.ID) > 128 ||
		!strings.EqualFold(ref.ID[:strings.LastIndexByte(ref.ID, '-')], ref.Scope) {
		return Read[provider.Task]{}, errors.New("jira read requires a project key and an issue key in that project")
	}
	site, err := httpsOrigin(siteOrigin)
	if err != nil {
		return Read[provider.Task]{}, err
	}
	input := struct {
		CloudID      string   `json:"cloudId"`
		IssueIDOrKey string   `json:"issueIdOrKey"`
		Fields       []string `json:"fields"`
	}{
		CloudID: site, IssueIDOrKey: ref.ID,
		Fields: []string{"summary", "description", "status", "issuetype", "priority", "assignee", "labels", "parent", "updated"},
	}
	return newRead(Jira, GetTask, ref, input, func(raw json.RawMessage) (provider.Task, error) {
		return jiraTask(ref, site, raw)
	})
}

func jiraTask(ref provider.Ref, site string, raw json.RawMessage) (provider.Task, error) {
	var issue atlassian.Issue
	if err := decodeObject(raw, &issue); err != nil {
		return provider.Task{}, err
	}
	if issue.Key != "" && !strings.EqualFold(issue.Key, ref.ID) {
		return provider.Task{}, fmt.Errorf("jira returned a different issue key: %w", provider.ErrConflict)
	}
	f := &issue.Fields
	if issue.Key == "" || strings.TrimSpace(f.Summary) == "" || f.Status.Name == "" || f.Status.Category.Key == "" {
		return provider.Task{}, fmt.Errorf("%w: jira issue requires key, summary and status", ErrInvalidResult)
	}
	ref.ID = issue.Key
	ref.Scope = issue.Key[:strings.LastIndexByte(issue.Key, '-')]
	task := provider.Task{
		Ref: ref, Key: issue.Key, URL: site + "/browse/" + issue.Key, Title: f.Summary,
		Description: issue.DescriptionText(), Type: f.IssueType.Name, Labels: f.Labels, Revision: f.Updated,
		State: provider.TaskState{ID: f.Status.Name, Name: f.Status.Name, Category: f.Status.Category.Key, Terminal: f.Status.Category.Key == "done"},
	}
	if f.Priority != nil {
		task.Priority = f.Priority.Name
	}
	if f.Assignee != nil {
		task.Assignee = f.Assignee.DisplayName
	}
	if f.Parent != nil && jiraIssueKey.MatchString(f.Parent.Key) {
		key := f.Parent.Key
		parent := provider.Ref{Instance: ref.Instance, Scope: key[:strings.LastIndexByte(key, '-')], ID: key}
		task.Links = []provider.Link{{Ref: &parent, URL: site + "/browse/" + key, Title: f.Parent.Fields.Summary}}
	}
	return task, nil
}
