package legacy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ryanhelms/agentflow/internal/atlassian"
	"github.com/ryanhelms/agentflow/internal/flowmap"
	"github.com/ryanhelms/agentflow/internal/provider"
)

// JiraClient is the subset of the Atlassian client used by Jira.
type JiraClient interface {
	GetIssue(ctx context.Context, key string) (*atlassian.Issue, error)
	Search(ctx context.Context, jql string, max int) ([]atlassian.Issue, error)
	CreateIssue(ctx context.Context, project, issueType, summary, description string, labels []string, linkTo string) (string, error)
	Transitions(ctx context.Context, key string) ([]atlassian.Transition, error)
	DoTransition(ctx context.Context, key, id string) error
	RemoteLinks(ctx context.Context, key string) ([]atlassian.Page, error)
	AddComment(ctx context.Context, key, text string) error
	Myself(ctx context.Context) (atlassian.User, error)
	Assign(ctx context.Context, key, accountID string) error
}

// Jira adapts a single Jira site. References use the project key and issue key.
type Jira struct {
	instance string
	siteURL  string
	client   JiraClient
	flow     *flowmap.Map
}

// NewJira uses siteURL for browse links and flow for the optional project status order.
// Pass nil flow to enforce Jira's status category order alone.
func NewJira(instance, siteURL string, client JiraClient, flow *flowmap.Map) *Jira {
	return &Jira{instance: instance, siteURL: strings.TrimRight(siteURL, "/"), client: client, flow: flow}
}

var (
	_ JiraClient              = (*atlassian.Client)(nil)
	_ provider.Tasks          = (*Jira)(nil)
	_ provider.TaskComments   = (*Jira)(nil)
	_ provider.TaskAssignment = (*Jira)(nil)
)

const jiraProjectKey = `[A-Za-z][A-Za-z0-9_-]*`

var jiraKey = regexp.MustCompile("^" + jiraProjectKey + `-[0-9]+$`)
var jiraProject = regexp.MustCompile("^" + jiraProjectKey + "$")

// ValidateJiraProject checks a Jira project key before it is used in a draft or a query.
func ValidateJiraProject(key string) error {
	if !jiraProject.MatchString(key) {
		return fmt.Errorf("invalid Jira project %q", key)
	}
	return nil
}

// JiraRef constructs a validated reference from a caller-facing Jira key.
func JiraRef(instance, key string) (provider.Ref, error) {
	if !jiraKey.MatchString(key) {
		return provider.Ref{}, fmt.Errorf("invalid jira issue key %q", key)
	}
	ref := provider.Ref{Instance: instance, Scope: key[:strings.LastIndexByte(key, '-')], ID: key}
	return ref, ref.Validate()
}

func (j *Jira) validateRef(ref provider.Ref) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Instance != j.instance {
		return fmt.Errorf("jira reference %s belongs to a different instance", ref)
	}
	want, err := JiraRef(j.instance, ref.ID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(want.Scope, ref.Scope) {
		return fmt.Errorf("jira issue %q is outside project %q", ref.ID, ref.Scope)
	}
	return nil
}

func (j *Jira) issue(ctx context.Context, ref provider.Ref) (*atlassian.Issue, provider.Ref, error) {
	issue, err := j.client.GetIssue(ctx, ref.ID)
	if err != nil {
		return nil, provider.Ref{}, fmt.Errorf("get jira issue %s: %w", ref.ID, atlassianError(err))
	}
	actual, err := JiraRef(j.instance, issue.Key)
	if err != nil {
		return nil, provider.Ref{}, err
	}
	if !strings.EqualFold(ref.ID, actual.ID) {
		return nil, provider.Ref{}, fmt.Errorf("jira issue %s moved to %s: %w", ref.ID, actual.ID, provider.ErrConflict)
	}
	return issue, actual, nil
}

func (j *Jira) browseURL(key string) string {
	return j.siteURL + "/browse/" + url.PathEscape(key)
}

func (j *Jira) task(issue *atlassian.Issue, ref provider.Ref) provider.Task {
	f := &issue.Fields
	task := provider.Task{
		Ref: ref, Key: issue.Key, URL: j.browseURL(issue.Key), Title: f.Summary,
		Description: issue.DescriptionText(), Type: f.IssueType.Name, Revision: f.Updated,
		State: provider.TaskState{ID: f.Status.Name, Name: f.Status.Name, Category: f.Status.Category.Key, Terminal: f.Status.Category.Key == "done"},
	}
	if f.Priority != nil {
		task.Priority = f.Priority.Name
	}
	if f.Assignee != nil {
		task.Assignee = f.Assignee.DisplayName
	}
	if f.Parent != nil {
		if parent, err := JiraRef(j.instance, f.Parent.Key); err == nil {
			task.Links = append(task.Links, provider.Link{Title: f.Parent.Fields.Summary, URL: j.browseURL(parent.ID), Ref: &parent})
		}
	}
	return task
}

// GetFields reads the issue without requiring permission to read remote links.
func (j *Jira) GetFields(ctx context.Context, ref provider.Ref) (provider.Task, error) {
	if err := j.validateRef(ref); err != nil {
		return provider.Task{}, err
	}
	issue, actual, err := j.issue(ctx, ref)
	if err != nil {
		return provider.Task{}, err
	}
	return j.task(issue, actual), nil
}

func (j *Jira) Get(ctx context.Context, ref provider.Ref) (provider.Task, error) {
	task, err := j.GetFields(ctx, ref)
	if err != nil {
		return provider.Task{}, err
	}
	links, err := j.client.RemoteLinks(ctx, task.Ref.ID)
	if err != nil {
		return provider.Task{}, fmt.Errorf("get links for jira issue %s: %w", task.Ref.ID, atlassianError(err))
	}
	for _, link := range links {
		task.Links = append(task.Links, provider.Link{Title: link.Title, URL: link.URL})
	}
	return task, nil
}

// Find maps the issue fields returned by Jira search, including the parent link.
// Remote links are fetched separately by Get.
func (j *Jira) Find(ctx context.Context, query provider.TaskQuery) ([]provider.Task, error) {
	var filters []string
	if query.Scope != "" {
		filters = append(filters, fmt.Sprintf("project = %q", query.Scope))
	}
	if query.Text != "" {
		filters = append(filters, fmt.Sprintf("text ~ %q", query.Text))
	}
	if query.AssignedToMe {
		filters = append(filters, "assignee = currentUser()")
	}
	if query.OpenOnly {
		filters = append(filters, "statusCategory != Done")
	}
	jql := "ORDER BY priority DESC, updated DESC"
	if len(filters) > 0 {
		jql = strings.Join(filters, " AND ") + " " + jql
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 30
	}
	return j.Search(ctx, jql, limit)
}

// Search maps a Jira-specific JQL query to task results.
func (j *Jira) Search(ctx context.Context, jql string, limit int) ([]provider.Task, error) {
	issues, err := j.client.Search(ctx, jql, limit)
	if err != nil {
		return nil, fmt.Errorf("search jira issues: %w", atlassianError(err))
	}
	tasks := make([]provider.Task, 0, len(issues))
	for i := range issues {
		issue := &issues[i]
		ref, err := JiraRef(j.instance, issue.Key)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, j.task(issue, ref))
	}
	return tasks, nil
}

func (j *Jira) Create(ctx context.Context, draft provider.TaskDraft, options provider.WriteOptions) (provider.WriteResult, error) {
	if err := j.writeOptions(options); err != nil {
		return provider.WriteResult{}, err
	}
	if strings.TrimSpace(draft.Scope) == "" || strings.TrimSpace(draft.Title) == "" {
		return provider.WriteResult{}, errors.New("jira issue creation requires a project and title")
	}
	if err := ValidateJiraProject(draft.Scope); err != nil {
		return provider.WriteResult{}, err
	}
	if draft.Priority != "" {
		return provider.WriteResult{}, provider.Unsupported(j.instance, "set priority when creating a jira issue")
	}
	var linkTo string
	switch len(draft.Links) {
	case 0:
	case 1:
		link := draft.Links[0].Ref
		if link == nil || link.Instance != j.instance {
			return provider.WriteResult{}, provider.Unsupported(j.instance, "create a jira issue with an external link")
		}
		if err := j.validateRef(*link); err != nil {
			return provider.WriteResult{}, err
		}
		linkTo = link.ID
	default:
		return provider.WriteResult{}, provider.Unsupported(j.instance, "create a jira issue with multiple links")
	}
	issueType := draft.Type
	if issueType == "" {
		issueType = "Task"
	}
	key, err := j.client.CreateIssue(ctx, draft.Scope, issueType, draft.Title, draft.Description, draft.Labels, linkTo)
	if key == "" && err != nil {
		return provider.WriteResult{}, fmt.Errorf("create jira issue: %w", atlassianError(err))
	}
	ref, refErr := JiraRef(j.instance, key)
	if refErr != nil {
		return provider.WriteResult{}, errors.Join(refErr, err)
	}
	// CreateIssue can return a key and a link error after creating the issue.
	result := j.result(ref, provider.Applied, "")
	if err != nil {
		return result, fmt.Errorf("link new jira issue: %w", atlassianError(err))
	}
	return result, nil
}

func (j *Jira) Comment(ctx context.Context, ref provider.Ref, body string, options provider.WriteOptions) (provider.WriteResult, error) {
	if err := j.validateWrite(ref, options); err != nil {
		return provider.WriteResult{}, err
	}
	if err := j.client.AddComment(ctx, ref.ID, body); err != nil {
		return provider.WriteResult{}, fmt.Errorf("comment on jira issue %s: %w", ref.ID, atlassianError(err))
	}
	return j.result(ref, provider.Applied, ""), nil
}

func (j *Jira) AssignToMe(ctx context.Context, ref provider.Ref, options provider.WriteOptions) (provider.WriteResult, error) {
	if err := j.validateWrite(ref, options); err != nil {
		return provider.WriteResult{}, err
	}
	me, err := j.client.Myself(ctx)
	if err != nil {
		return provider.WriteResult{}, fmt.Errorf("get authenticated jira user: %w", atlassianError(err))
	}
	if me.AccountID == "" {
		return provider.WriteResult{}, errors.New("authenticated jira user has no account id")
	}
	issue, actual, err := j.issue(ctx, ref)
	if err != nil {
		return provider.WriteResult{}, err
	}
	if assignee := issue.Fields.Assignee; assignee != nil && assignee.AccountID == me.AccountID {
		return j.result(actual, provider.AlreadyApplied, issue.Fields.Updated), nil
	}
	if err := j.client.Assign(ctx, actual.ID, me.AccountID); err != nil {
		return provider.WriteResult{}, fmt.Errorf("assign jira issue %s: %w", actual.ID, atlassianError(err))
	}
	return j.result(actual, provider.Applied, ""), nil
}

func (j *Jira) result(ref provider.Ref, status provider.WriteStatus, revision string) provider.WriteResult {
	return provider.WriteResult{Ref: ref, Status: status, Revision: revision, URL: j.browseURL(ref.ID)}
}

func (j *Jira) validateWrite(ref provider.Ref, options provider.WriteOptions) error {
	if err := j.validateRef(ref); err != nil {
		return err
	}
	return j.writeOptions(options)
}

func (j *Jira) writeOptions(options provider.WriteOptions) error {
	if options.ExpectedRevision != "" {
		return provider.Unsupported(j.instance, "enforce a jira revision precondition")
	}
	if options.OperationID != "" {
		return provider.Unsupported(j.instance, "deduplicate jira writes by operation id")
	}
	return nil
}

func atlassianError(err error) error {
	var apiErr *atlassian.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %w", provider.ErrNotFound, err)
		case http.StatusConflict:
			return fmt.Errorf("%w: %w", provider.ErrConflict, err)
		}
	}
	return err
}
