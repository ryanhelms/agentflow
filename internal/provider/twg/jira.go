package twg

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nscaledev/agentflow/internal/atlassian"
	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/nativemap"
)

// Jira operates on work items on one Atlassian site. References use the Jira project
// key as scope and the issue key as ID.
type Jira struct{ binding }

var _ provider.Tasks = (*Jira)(nil)

const jiraFields = "summary,description,status,issuetype,priority,assignee,labels,parent,updated,project"

// NewJira binds reads to the configured project keys. An empty list permits
// searches across the configured site.
func NewJira(instance, site string, projects []string) (*Jira, error) {
	projects = slices.Clone(projects)
	for i, project := range projects {
		projects[i] = strings.ToUpper(project)
	}
	b, err := newBinding(instance, site, projects, jiraProject)
	if err != nil {
		return nil, err
	}
	return &Jira{binding: b}, nil
}

type named struct {
	ID          string        `json:"id"`
	Key         string        `json:"key"`
	Name        string        `json:"name"`
	DisplayName string        `json:"displayName"`
	Category    *jiraCategory `json:"statusCategory"`
}

func (n *named) UnmarshalJSON(raw []byte) error {
	if raw[0] == '"' {
		return json.Unmarshal(raw, &n.Name)
	}
	type object named
	return json.Unmarshal(raw, (*object)(n))
}

type jiraCategory struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

func (c *jiraCategory) UnmarshalJSON(raw []byte) error {
	if raw[0] == '"' {
		return json.Unmarshal(raw, &c.Name)
	}
	type object jiraCategory
	return json.Unmarshal(raw, (*object)(c))
}

type jiraFieldsJSON struct {
	Summary        string          `json:"summary"`
	Description    json.RawMessage `json:"description"`
	Status         named           `json:"status"`
	StatusCategory jiraCategory    `json:"statusCategory"`
	IssueType      named           `json:"issuetype"`
	Priority       named           `json:"priority"`
	Assignee       named           `json:"assignee"`
	Project        named           `json:"project"`
	ProjectKey     string          `json:"projectKey"`
	Labels         []string        `json:"labels"`
	Updated        string          `json:"updated"`
	RemoteLinks    json.RawMessage `json:"remoteLinks"`
	Parent         *struct {
		Key     string `json:"key"`
		Summary string `json:"summary"`
		Fields  struct {
			Summary string `json:"summary"`
		} `json:"fields"`
	} `json:"parent"`
}

type jiraIssue struct {
	jiraFieldsJSON
	Key    string          `json:"key"`
	URL    string          `json:"url"`
	Fields *jiraFieldsJSON `json:"fields"`
	Error  json.RawMessage `json:"error"`
}

func (j *Jira) Get(ctx context.Context, ref provider.Ref) (provider.Task, error) {
	return j.get(ctx, ref, false)
}

// GetWithRemoteLinks hydrates Jira's Web links with the same saved OAuth login.
func (j *Jira) GetWithRemoteLinks(ctx context.Context, ref provider.Ref) (provider.Task, error) {
	return j.get(ctx, ref, true)
}

func (j *Jira) get(ctx context.Context, ref provider.Ref, remoteLinks bool) (provider.Task, error) {
	ref, err := j.issueRef(ref)
	if err != nil {
		return provider.Task{}, err
	}
	var issues []jiraIssue
	args := []string{"jira", "workitem", "get", ref.ID, "--fields", jiraFields}
	if remoteLinks {
		args = append(args, "--remote-links")
	}
	if err := j.read(ctx, "read jira work item", args, &issues); err != nil {
		return provider.Task{}, err
	}
	if len(issues) != 1 {
		return provider.Task{}, fmt.Errorf("jira read returned an unexpected number of work items: %w", ErrFailure)
	}
	task, err := j.task(issues[0], []string{ref.Scope})
	if err != nil {
		return provider.Task{}, err
	}
	if task.Ref.ID != ref.ID {
		return provider.Task{}, fmt.Errorf("jira read returned a different work item: %w", ErrFailure)
	}
	if remoteLinks {
		raw := issues[0].RemoteLinks
		if len(raw) == 0 && issues[0].Fields != nil {
			raw = issues[0].Fields.RemoteLinks
		}
		pages, err := nativemap.DecodeJiraRemoteLinks(raw)
		if err != nil {
			return provider.Task{}, fmt.Errorf("read jira remote links: %w", err)
		}
		for _, page := range pages {
			task.Links = append(task.Links, provider.Link{URL: page.URL, Title: page.Title})
		}
	}
	return task, nil
}

func (j *Jira) Find(ctx context.Context, q provider.TaskQuery) ([]provider.Task, error) {
	return j.find(ctx, q, false)
}

// FindMyWork finds the signed-in user's open work, optionally restricted to
// active sprints. Both variants honor the configured project allowlist.
func (j *Jira) FindMyWork(ctx context.Context, activeSprint bool, limit int) ([]provider.Task, error) {
	return j.find(ctx, provider.TaskQuery{AssignedToMe: true, OpenOnly: true, Limit: limit}, activeSprint)
}

func (j *Jira) find(ctx context.Context, q provider.TaskQuery, activeSprint bool) ([]provider.Task, error) {
	scopes, err := j.queryScopes(strings.ToUpper(q.Scope))
	if err != nil {
		return nil, err
	}
	query, err := textFilter(scopeClause("project", scopes), "text", q.Text)
	if err != nil {
		return nil, err
	}
	if q.AssignedToMe {
		query = andFilter(query, "assignee = currentUser()")
	}
	if q.OpenOnly {
		query = andFilter(query, "statusCategory != Done")
	}
	if activeSprint {
		query = andFilter(query, "sprint in openSprints()")
	}
	query = strings.TrimSpace(query + " ORDER BY priority DESC, updated DESC")
	limit := queryLimit(q.Limit, 30, 100)
	var result struct {
		Issues []jiraIssue `json:"issues"`
	}
	args := []string{"jira", "workitem", "query", "--jql", query, "--fields", jiraFields, "--limit", strconv.Itoa(limit)}
	if err := j.read(ctx, "find jira work items", args, &result); err != nil {
		return nil, err
	}
	if result.Issues == nil {
		return nil, fmt.Errorf("jira query omitted its work item list: %w", ErrFailure)
	}
	tasks := make([]provider.Task, 0, min(len(result.Issues), limit))
	for _, issue := range result.Issues {
		task, err := j.task(issue, scopes)
		if err != nil {
			return nil, err
		}
		if len(tasks) < limit {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}

func (j *Jira) issueRef(ref provider.Ref) (provider.Ref, error) {
	ref.Scope = strings.ToUpper(ref.Scope)
	ref.ID = strings.ToUpper(ref.ID)
	if err := j.checkRef(ref); err != nil {
		return provider.Ref{}, err
	}
	if jiraScope(ref.ID) != ref.Scope {
		return provider.Ref{}, fmt.Errorf("issue key is outside the requested jira project: %w", ErrAccessDenied)
	}
	return ref, nil
}

func jiraScope(key string) string {
	if !jiraIssueKey.MatchString(key) {
		return ""
	}
	return key[:strings.LastIndexByte(key, '-')]
}

func (j *Jira) task(issue jiraIssue, scopes []string) (provider.Task, error) {
	if present(issue.Error) {
		return provider.Task{}, fmt.Errorf("jira work item read: %w", classify(issue.Error))
	}
	key := strings.ToUpper(issue.Key)
	scope := jiraScope(key)
	if scope == "" || (len(scopes) > 0 && !slices.Contains(scopes, scope)) {
		return provider.Task{}, fmt.Errorf("jira result is outside the requested project: %w", ErrFailure)
	}
	f := issue.jiraFieldsJSON
	if issue.Fields != nil {
		f = *issue.Fields
	}
	if (f.Project.Key != "" && !strings.EqualFold(f.Project.Key, scope)) ||
		(f.ProjectKey != "" && !strings.EqualFold(f.ProjectKey, scope)) {
		return provider.Task{}, fmt.Errorf("jira result has conflicting project identity: %w", ErrFailure)
	}
	taskURL := j.site + "/browse/" + key
	if issue.URL != "" {
		u, err := j.sameSiteURL(issue.URL)
		if err != nil {
			return provider.Task{}, err
		}
		if u.Path != "/browse/"+key {
			return provider.Task{}, fmt.Errorf("jira result has a mismatched work item url: %w", ErrFailure)
		}
	}
	if strings.TrimSpace(f.Summary) == "" || strings.TrimSpace(f.Status.Name) == "" {
		return provider.Task{}, fmt.Errorf("jira result omitted its summary or status: %w", ErrFailure)
	}
	category := first(f.StatusCategory.Key, f.StatusCategory.Name)
	if f.Status.Category != nil {
		category = first(f.Status.Category.Key, f.Status.Category.Name, category)
	}
	category = strings.ToLower(category)
	task := provider.Task{
		Ref: j.ref(scope, key), Key: key, URL: taskURL, Title: f.Summary,
		Description: atlassian.ADFText(f.Description), Type: f.IssueType.Name,
		State:    provider.TaskState{ID: first(f.Status.ID, f.Status.Name), Name: f.Status.Name, Category: category, Terminal: category == "done"},
		Priority: f.Priority.Name, Assignee: first(f.Assignee.DisplayName, f.Assignee.Name), Labels: f.Labels, Revision: f.Updated,
	}
	if f.Parent != nil {
		parent := strings.ToUpper(f.Parent.Key)
		if parentScope := jiraScope(parent); parentScope == scope || slices.Contains(j.scopes, parentScope) {
			ref := j.ref(parentScope, parent)
			task.Links = []provider.Link{{Ref: &ref, Title: first(f.Parent.Summary, f.Parent.Fields.Summary), URL: j.site + "/browse/" + parent}}
		}
	}
	return task, nil
}

func (j *Jira) Create(context.Context, provider.TaskDraft, provider.WriteOptions) (provider.WriteResult, error) {
	return provider.WriteResult{}, provider.Unsupported(j.instance, "create a jira work item through twg")
}

func (j *Jira) Transition(context.Context, provider.Ref, provider.TaskState, provider.WriteOptions) (provider.WriteResult, error) {
	return provider.WriteResult{}, provider.Unsupported(j.instance, "transition a jira work item through twg")
}
