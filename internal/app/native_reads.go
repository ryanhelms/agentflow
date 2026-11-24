package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/nscaledev/agentflow/internal/atlassian"
	"github.com/nscaledev/agentflow/internal/auth"
	"github.com/nscaledev/agentflow/internal/managedcli"
	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/legacy"
	"github.com/nscaledev/agentflow/internal/provider/native"
	"github.com/nscaledev/agentflow/internal/provider/nativemap"
	"github.com/nscaledev/agentflow/internal/provider/twg"
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
		page, err := readNative(ctx, a.Repo.Dir, call, a.decodeJiraWork, func(ctx context.Context) (jiraWorkPage, error) {
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
	if useAtlassianCLI(ctx, site, "jira") {
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
		return fmt.Errorf("%s: %w; run agentflow setup login --provider %s", name, native.ErrSignIn, name)
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
	return readNative(ctx, a.Repo.Dir, native.Call{Provider: "confluence", Server: "atlassian", Tool: "searchConfluence",
		Arguments: map[string]any{"cloudId": strings.TrimRight(site, "/"), "cql": cql, "limit": 10}},
		func(raw json.RawMessage) ([]atlassian.Page, error) { return decodeConfluencePages(raw, site, space) },
		func(ctx context.Context) ([]atlassian.Page, error) {
			return a.legacyConfluencePages(ctx, site, query, space)
		})
}

func (a *App) legacyConfluencePages(ctx context.Context, site, query, space string) ([]atlassian.Page, error) {
	q := provider.DocumentQuery{Text: query, Scope: space, Limit: 10}
	if useAtlassianCLI(ctx, site, "confluence") {
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
				ID      string                `json:"id"`
				Title   string                `json:"title"`
				Version atlassian.PageVersion `json:"version"`
				Space   struct {
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
		if !atlassian.ValidConfluenceID(page.ID) || space != "" && !strings.EqualFold(page.Space.Key, space) {
			return nil, errors.New("invalid or out-of-scope Confluence search result")
		}
		var revision string
		if page.Version.Number > 0 {
			revision = strconv.FormatInt(page.Version.Number, 10)
		}
		pages = append(pages, atlassian.Page{
			ID: page.ID, Space: page.Space.Key, Title: page.Title, Excerpt: item.Excerpt, Revision: revision,
			URL: strings.TrimRight(site, "/") + "/wiki/pages/viewpage.action?pageId=" + page.ID,
		})
	}
	return pages, nil
}

func mappedNativeRead[T any](ctx context.Context, repo, server string, read nativemap.Read[T], fallback func(context.Context) (T, error)) (T, error) {
	call := read.Call()
	var args map[string]any
	if err := json.Unmarshal(call.Arguments(), &args); err != nil {
		var zero T
		return zero, err
	}
	return readNative(ctx, repo, native.Call{
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

func readNative[T any](ctx context.Context, repo string, call native.Call, decode func(json.RawMessage) (T, error), fallback func(context.Context) (T, error)) (T, error) {
	return native.Read(ctx, call, decode, func(mcpErr error) (T, error) {
		if mcpErr == nil {
			return fallback(ctx)
		}
		tool, ok := managedcli.ForProvider(call.Provider)
		var zero T
		if !ok {
			if errors.Is(mcpErr, native.ErrUnavailable) {
				return fallback(ctx)
			}
			return zero, mcpErr
		}
		path, err := managedcli.Ensure(ctx, repo, tool)
		if err != nil {
			return zero, fmt.Errorf("could not prepare %s fallback: %w", tool.Command, err)
		}
		return fallback(managedcli.WithCommand(ctx, tool, path))
	})
}

func (a *App) standaloneReadContext(ctx context.Context) context.Context {
	ci, _ := strconv.ParseBool(os.Getenv("CI"))
	actions, _ := strconv.ParseBool(os.Getenv("GITHUB_ACTIONS"))
	if a.Client != "cli" || native.HasHost(ctx) || ci || actions {
		return ctx
	}
	return managedcli.WithReadInstall(ctx, a.Repo.Dir)
}

func useAtlassianCLI(ctx context.Context, site, name string) bool {
	if !strings.HasPrefix(site, "https://") {
		return false
	}
	// Do not replace an explicitly selected headless service identity with the
	// workstation CLI's saved OAuth identity.
	if native.HasHost(ctx) {
		return true
	}
	if name == "confluence" && (os.Getenv("CONFLUENCE_USER") != "" || os.Getenv("CONFLUENCE_TOKEN") != "") {
		return false
	}
	return os.Getenv("JIRA_USER") == "" || os.Getenv("JIRA_TOKEN") == ""
}
