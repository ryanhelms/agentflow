package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/auth"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/legacy"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
	"github.com/nscaledev/devflow/internal/provider/twg"
)

type jiraWorkPage struct {
	items []WorkItem
	next  string
}

func (a *App) jiraWorkSearch(ctx context.Context, site, jql string, activeSprint bool) ([]WorkItem, error) {
	const limit = 30
	var items []WorkItem
	var cursor string
	seen := make(map[string]bool)
	for range 3 {
		call := native.Call{Provider: "jira", Server: "atlassian", Tool: "searchJiraIssuesUsingJql",
			Arguments: map[string]any{
				"cloudId": strings.TrimRight(site, "/"), "jql": jql, "maxResults": 10,
				"fields": []string{"summary", "status", "issuetype", "priority"},
			}}
		if cursor != "" {
			call.Arguments["nextPageToken"] = cursor
		}
		page, err := native.Read(ctx, call, a.decodeJiraWork, func() (jiraWorkPage, error) {
			if cursor != "" {
				return jiraWorkPage{}, fmt.Errorf("Jira MCP pagination: %w", native.ErrUnavailable)
			}
			list, err := a.cliJiraWork(ctx, site, jql, activeSprint)
			return jiraWorkPage{items: list}, err
		})
		if err != nil {
			return nil, err
		}
		items = append(items, page.items...)
		if len(items) >= limit {
			return items[:limit], nil
		}
		if page.next == "" {
			if items == nil {
				return []WorkItem{}, nil
			}
			return items, nil
		}
		if seen[page.next] {
			return nil, errors.New("Jira provider returned a repeated page cursor")
		}
		seen[page.next] = true
		cursor = page.next
	}
	return items, nil
}

func (a *App) cliJiraWork(ctx context.Context, site, jql string, activeSprint bool) ([]WorkItem, error) {
	if strings.HasPrefix(site, "https://") {
		cli, err := twg.NewJira("jira", site, a.jiraSettings().Projects)
		if err != nil {
			return nil, err
		}
		tasks, err := cli.FindMyWork(ctx, activeSprint, 30)
		if !errors.Is(err, twg.ErrToolAbsent) {
			return jiraWorkItems(tasks), atlCLIError("jira", err)
		}
	}
	j, err := a.jiraTasks(nil)
	if errors.Is(err, auth.ErrNoCredentials) {
		return nil, fmt.Errorf("Jira needs a connected Atlassian MCP or the signed-in Teamwork Graph CLI: %w", native.ErrUnavailable)
	}
	if err != nil {
		return nil, err
	}
	tasks, err := j.Search(ctx, jql, 30)
	if err != nil {
		return nil, err
	}
	return jiraWorkItems(tasks), nil
}

func jiraWorkItems(tasks []provider.Task) []WorkItem {
	out := make([]WorkItem, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, WorkItem{Key: task.Key, Summary: task.Title, Status: task.State.Name, Type: task.Type, Priority: task.Priority})
	}
	return out
}

func atlCLIError(name string, err error) error {
	switch {
	case errors.Is(err, twg.ErrAuthRequired):
		return fmt.Errorf("%s: %w; run devflow setup login --provider %s", name, native.ErrSignIn, name)
	case errors.Is(err, twg.ErrAccessDenied):
		return fmt.Errorf("%s: %w", name, native.ErrDenied)
	default:
		return err
	}
}

func (a *App) decodeJiraWork(raw json.RawMessage) (jiraWorkPage, error) {
	var result struct {
		Issues json.RawMessage `json:"issues"`
		Next   string          `json:"nextPageToken"`
	}
	var out jiraWorkPage
	if err := native.Decode(raw, &result); err != nil || len(result.Issues) == 0 {
		return out, errors.New("invalid Jira provider result")
	}
	var issues []atlassian.Issue
	if result.Issues[0] == '[' {
		if err := json.Unmarshal(result.Issues, &issues); err != nil {
			return out, errors.New("invalid Jira issue list")
		}
		out.next = result.Next
	} else {
		var connection struct {
			Nodes    []atlassian.Issue `json:"nodes"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		}
		if err := json.Unmarshal(result.Issues, &connection); err != nil || connection.Nodes == nil {
			return out, errors.New("invalid Jira issue connection")
		}
		issues = connection.Nodes
		if connection.PageInfo.HasNextPage {
			if connection.PageInfo.EndCursor == "" {
				return out, errors.New("Jira next page has no cursor")
			}
			out.next = connection.PageInfo.EndCursor
		}
	}
	projects := a.jiraSettings().Projects
	out.items = make([]WorkItem, 0, len(issues))
	for _, issue := range issues {
		ref, err := legacy.JiraRef("jira", issue.Key)
		if err != nil {
			return jiraWorkPage{}, errors.New("invalid Jira issue key in provider result")
		}
		if len(projects) != 0 && !slices.ContainsFunc(projects, func(project string) bool { return strings.EqualFold(project, ref.Scope) }) {
			return jiraWorkPage{}, errors.New("Jira provider returned a result outside the configured projects")
		}
		f := issue.Fields
		item := WorkItem{Key: ref.ID, Summary: f.Summary, Status: f.Status.Name, Type: f.IssueType.Name}
		if f.Priority != nil {
			item.Priority = f.Priority.Name
		}
		out.items = append(out.items, item)
	}
	return out, nil
}

func (a *App) nativeConfluenceSearch(ctx context.Context, site, query, space string) ([]atlassian.Page, error) {
	cql := fmt.Sprintf("type=page AND text ~ %q", query)
	if space != "" {
		cql += fmt.Sprintf(" AND space = %q", space)
	}
	return native.Read(ctx, native.Call{Provider: "confluence", Server: "atlassian", Tool: "searchConfluence",
		Arguments: map[string]any{"cloudId": strings.TrimRight(site, "/"), "cql": cql, "limit": 10}},
		func(raw json.RawMessage) ([]atlassian.Page, error) { return decodeConfluencePages(raw, site, space) },
		func() ([]atlassian.Page, error) { return a.legacyConfluencePages(ctx, site, query, space) })
}

func (a *App) legacyConfluencePages(ctx context.Context, site, query, space string) ([]atlassian.Page, error) {
	q := provider.DocumentQuery{Text: query, Scope: space, Limit: 10}
	if strings.HasPrefix(site, "https://") {
		cli, err := twg.NewConfluence("confluence", site, nil)
		if err != nil {
			return nil, err
		}
		docs, err := cli.Find(ctx, q)
		if !errors.Is(err, twg.ErrToolAbsent) {
			return confluencePages(docs), atlCLIError("confluence", err)
		}
	}
	k, err := a.confluenceKnowledge(site)
	if errors.Is(err, auth.ErrNoCredentials) {
		return nil, fmt.Errorf("the Confluence provider needs a connected Atlassian MCP or the signed-in Teamwork Graph CLI: %w", native.ErrUnavailable)
	}
	if err != nil {
		return nil, err
	}
	docs, err := k.Find(ctx, q)
	return confluencePages(docs), err
}

func confluencePages(docs []provider.Document) []atlassian.Page {
	pages := make([]atlassian.Page, 0, len(docs))
	for _, d := range docs {
		pages = append(pages, atlassian.Page{ID: d.Ref.ID, Space: d.Ref.Scope, Title: d.Title, URL: d.URL, Excerpt: d.Body, Revision: d.Revision})
	}
	return pages
}

func decodeConfluencePages(raw json.RawMessage, site, space string) ([]atlassian.Page, error) {
	var result struct {
		Results []struct {
			Content struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Space struct {
					Key string `json:"key"`
				} `json:"space"`
			} `json:"content"`
			URL     string `json:"url"`
			Excerpt string `json:"excerpt"`
		} `json:"results"`
	}
	if err := native.Decode(raw, &result); err != nil || result.Results == nil {
		return nil, errors.New("invalid Confluence provider result")
	}
	pages := make([]atlassian.Page, 0, len(result.Results))
	for _, item := range result.Results {
		page := item.Content
		if !atlassian.ValidConfluenceID(page.ID) || space != "" && page.Space.Key != "" && !strings.EqualFold(page.Space.Key, space) {
			return nil, errors.New("invalid or out-of-scope Confluence search result")
		}
		pageSpace := page.Space.Key
		if pageSpace == "" {
			pageSpace = space
		}
		pages = append(pages, atlassian.Page{
			ID: page.ID, Space: pageSpace, Title: page.Title, Excerpt: item.Excerpt,
			URL: strings.TrimRight(site, "/") + "/wiki/pages/viewpage.action?pageId=" + page.ID,
		})
	}
	return pages, nil
}

func mappedNativeRead[T any](ctx context.Context, server string, read nativemap.Read[T], fallback func() (T, error)) (T, error) {
	call := read.Call()
	var args map[string]any
	if err := json.Unmarshal(call.Arguments(), &args); err != nil {
		var zero T
		return zero, err
	}
	return native.Read(ctx, native.Call{
		Provider: string(call.Capability().Kind), Server: server, Tool: call.Capability().Tool, Arguments: args,
	}, func(raw json.RawMessage) (T, error) {
		payload, err := native.Payload(raw)
		if err != nil {
			var zero T
			return zero, err
		}
		return read.Decode(payload)
	}, fallback)
}

func (a *App) nativeJiraTask(ctx context.Context, ref provider.Ref, fallback func() (provider.Task, error)) (provider.Task, error) {
	site, err := a.jiraSite()
	if err != nil {
		return provider.Task{}, err
	}
	if !strings.HasPrefix(site, "https://") {
		return fallback()
	}
	read, err := nativemap.JiraIssue(ref, site)
	if err != nil {
		return provider.Task{}, err
	}
	return mappedNativeRead(ctx, "atlassian", read, func() (provider.Task, error) {
		cli, err := twg.NewJira("jira", site, a.jiraSettings().Projects)
		if err != nil {
			return provider.Task{}, err
		}
		task, err := cli.Get(ctx, ref)
		if errors.Is(err, twg.ErrToolAbsent) {
			return fallback()
		}
		return task, atlCLIError("jira", err)
	})
}
