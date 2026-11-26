// Package githubissues adapts GitHub repository issues to agentflow tasks.
package githubissues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/ryanhelms/agentflow/internal/gh"
	"github.com/ryanhelms/agentflow/internal/provider"
)

type Adapter struct {
	client      *gh.Client
	instance    string
	defaultRepo string
}

var (
	_ provider.Tasks          = (*Adapter)(nil)
	_ provider.TaskComments   = (*Adapter)(nil)
	_ provider.TaskAssignment = (*Adapter)(nil)
)

// New uses the supplied client without discovering credentials or repositories.
// An optional defaultRepo (owner/repo) is used by Find and Create when their
// scope is empty. Issue references always require an explicit scope.
func New(client *gh.Client, instance, defaultRepo string) (*Adapter, error) {
	if client == nil {
		return nil, errors.New("github issues requires a client")
	}
	if strings.TrimSpace(instance) == "" || strings.ContainsFunc(instance, unicode.IsControl) {
		return nil, errors.New("github issues requires a nonempty instance without control characters")
	}
	if defaultRepo != "" {
		repo, err := canonicalRepo(defaultRepo)
		if err != nil {
			return nil, fmt.Errorf("default repository: %w", err)
		}
		defaultRepo = repo
	}
	return &Adapter{client: client, instance: instance, defaultRepo: defaultRepo}, nil
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)

func canonicalRepo(scope string) (string, error) {
	if !repoPattern.MatchString(scope) || strings.HasSuffix(scope, "/.") || strings.HasSuffix(scope, "/..") {
		return "", fmt.Errorf("github issues repository must be owner/repo, got %q", scope)
	}
	return strings.ToLower(scope), nil
}

func (a *Adapter) scope(scope string) (string, error) {
	if scope == "" && a.defaultRepo != "" {
		return a.defaultRepo, nil
	}
	return canonicalRepo(scope)
}

func (a *Adapter) issueRef(ref provider.Ref) (provider.Ref, error) {
	if ref.Instance != a.instance {
		return provider.Ref{}, fmt.Errorf("github issues reference belongs to instance %q, expected %q", ref.Instance, a.instance)
	}
	repo, err := canonicalRepo(ref.Scope)
	if err != nil {
		return provider.Ref{}, err
	}
	number, err := strconv.ParseInt(ref.ID, 10, 64)
	if err != nil || number < 1 || strconv.FormatInt(number, 10) != ref.ID {
		return provider.Ref{}, fmt.Errorf("github issue id must be a positive decimal number, got %q", ref.ID)
	}
	ref.Scope = repo
	return ref, nil
}

func issuesPath(repo string) string { return "/repos/" + repo + "/issues" }

func issuePath(ref provider.Ref) string { return issuesPath(ref.Scope) + "/" + ref.ID }

type account struct {
	Login string `json:"login"`
}

type issueLabel struct {
	Name string `json:"name"`
}

type issue struct {
	Labels      []issueLabel    `json:"labels"`
	Number      int64           `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	State       string          `json:"state"`
	HTMLURL     string          `json:"html_url"`
	Assignee    account         `json:"assignee"`
	Assignees   []account       `json:"assignees"`
	PullRequest json.RawMessage `json:"pull_request,omitempty"`
}

func (i issue) assignedTo(login string) bool {
	return strings.EqualFold(i.Assignee.Login, login) || slices.ContainsFunc(i.Assignees, func(u account) bool {
		return strings.EqualFold(u.Login, login)
	})
}

func taskState(state string) (provider.TaskState, error) {
	switch state {
	case "open":
		return provider.TaskState{ID: "open", Name: "Open"}, nil
	case "closed":
		return provider.TaskState{ID: "closed", Name: "Closed", Terminal: true}, nil
	default:
		return provider.TaskState{}, fmt.Errorf("unknown github issue state %q", state)
	}
}

func (a *Adapter) task(repo string, i issue) (provider.Task, error) {
	if i.PullRequest != nil {
		return provider.Task{}, fmt.Errorf("%s#%d is a pull request: %w", repo, i.Number, provider.ErrNotFound)
	}
	if i.Number < 1 {
		return provider.Task{}, errors.New("github issue response has no issue number")
	}
	state, err := taskState(i.State)
	if err != nil {
		return provider.Task{}, err
	}
	ref := provider.Ref{Instance: a.instance, Scope: repo, ID: strconv.FormatInt(i.Number, 10)}
	assignee := i.Assignee.Login
	if assignee == "" && len(i.Assignees) > 0 {
		assignee = i.Assignees[0].Login
	}
	body, template, err := issueBody(i.Body)
	if err != nil {
		return provider.Task{}, fmt.Errorf("%s#%d: %w", repo, i.Number, err)
	}
	var labels []string
	for _, label := range i.Labels {
		labels = append(labels, label.Name)
	}
	return provider.Task{
		Ref: ref, Key: ref.Scope + "#" + ref.ID, URL: i.HTMLURL, Title: i.Title,
		Description: body, Template: template, Labels: labels, State: state, Assignee: assignee,
	}, nil
}

func (a *Adapter) Get(ctx context.Context, ref provider.Ref) (provider.Task, error) {
	canonical, err := a.issueRef(ref)
	if err != nil {
		return provider.Task{}, err
	}
	_, task, err := a.readIssue(ctx, canonical)
	return task, err
}

func (a *Adapter) readIssue(ctx context.Context, ref provider.Ref) (issue, provider.Task, error) {
	var i issue
	if err := a.do(ctx, http.MethodGet, issuePath(ref), nil, &i); err != nil {
		return issue{}, provider.Task{}, err
	}
	task, err := a.task(ref.Scope, i)
	if err != nil {
		return issue{}, provider.Task{}, err
	}
	if task.Ref != ref {
		return issue{}, provider.Task{}, fmt.Errorf("github issue response references %s, expected %s#%s", task.Key, ref.Scope, ref.ID)
	}
	return i, task, nil
}

func (a *Adapter) viewer(ctx context.Context) (string, error) {
	login, err := a.client.Viewer(ctx)
	if err != nil {
		return "", fmt.Errorf("read github viewer: %w", err)
	}
	if strings.TrimSpace(login) == "" {
		return "", errors.New("github viewer response has no login")
	}
	return login, nil
}

// Find lists issues in one repository, newest first. Text is a case-insensitive
// substring of the title or description. A nonpositive Limit defaults to 30.
func (a *Adapter) Find(ctx context.Context, query provider.TaskQuery) ([]provider.Task, error) {
	repo, err := a.scope(query.Scope)
	if err != nil {
		return nil, err
	}
	params := issueListParams()
	if query.OpenOnly {
		params.Set("state", "open")
	}
	var login string
	if query.AssignedToMe {
		login, err = a.viewer(ctx)
		if err != nil {
			return nil, err
		}
		params.Set("assignee", login)
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 30
	}
	text := strings.ToLower(strings.TrimSpace(query.Text))
	var tasks []provider.Task
	err = listPages(ctx, a, issuesPath(repo), params, func(i issue) (bool, error) {
		if i.PullRequest != nil || (query.OpenOnly && i.State != "open") || (login != "" && !i.assignedTo(login)) {
			return false, nil
		}
		task, err := a.task(repo, i)
		if err != nil {
			return false, err
		}
		if text != "" && !strings.Contains(strings.ToLower(task.Title), text) && !strings.Contains(strings.ToLower(task.Description), text) {
			return false, nil
		}
		tasks = append(tasks, task)
		return len(tasks) == limit, nil
	})
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

func issueListParams() url.Values {
	return url.Values{"state": {"all"}, "sort": {"created"}, "direction": {"desc"}}
}

// The shared client exposes response bodies without Link headers. Request
// numbered pages at the REST page size until a page is short or visit stops.
func listPages[T any](ctx context.Context, a *Adapter, path string, params url.Values, visit func(T) (bool, error)) error {
	const pageSize = 100
	params.Set("per_page", strconv.Itoa(pageSize))
	for page := 1; ; page++ {
		params.Set("page", strconv.Itoa(page))
		var items []T
		if err := a.do(ctx, http.MethodGet, path+"?"+params.Encode(), nil, &items); err != nil {
			return err
		}
		if items == nil {
			return fmt.Errorf("github list response for %s has no items array", path)
		}
		for _, item := range items {
			if done, err := visit(item); done || err != nil {
				return err
			}
		}
		if len(items) < pageSize {
			return nil
		}
	}
}

func (a *Adapter) do(ctx context.Context, method, path string, in, out any) error {
	err := a.client.Do(ctx, method, path, in, out)
	if httpStatus(err, method, path) == http.StatusNotFound {
		return fmt.Errorf("%w: %w", provider.ErrNotFound, err)
	}
	return err
}

// gh.Client.Do reports REST status codes in a fixed error prefix.
func httpStatus(err error, method, path string) int {
	if err == nil {
		return 0
	}
	message, ok := strings.CutPrefix(err.Error(), "github "+method+" "+path+": HTTP ")
	if !ok {
		return 0
	}
	code, _, _ := strings.Cut(message, ":")
	status, _ := strconv.Atoi(code)
	return status
}
