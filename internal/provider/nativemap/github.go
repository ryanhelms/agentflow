package nativemap

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/githubissues"
)

var repositoryScope = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)

// GitHubIssue maps issue_read's get method for a github.com repository.
func GitHubIssue(ref provider.Ref) (Read[provider.Task], error) {
	if err := ref.Validate(); err != nil {
		return Read[provider.Task]{}, err
	}
	number, err := strconv.ParseInt(ref.ID, 10, 32)
	owner, repo, _ := strings.Cut(ref.Scope, "/")
	if err != nil || number < 1 || strconv.FormatInt(number, 10) != ref.ID ||
		!repositoryScope.MatchString(ref.Scope) || repo == "." || repo == ".." {
		return Read[provider.Task]{}, errors.New("github read requires an owner/repo scope and a positive issue number")
	}
	ref.Scope = strings.ToLower(ref.Scope)
	input := struct {
		Method string `json:"method"`
		Owner  string `json:"owner"`
		Repo   string `json:"repo"`
		Number int64  `json:"issue_number"`
	}{"get", owner, repo, number}
	return newRead(GitHub, GetTask, ref, input, func(raw json.RawMessage) (provider.Task, error) {
		return githubTask(ref, number, raw)
	})
}

type githubIssueFields struct {
	Number    int64    `json:"number"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	State     string   `json:"state"`
	HTMLURL   string   `json:"html_url"`
	Assignees []string `json:"assignees"`
	Labels    []string `json:"labels"`
	IssueType string   `json:"issue_type"`
	UpdatedAt string   `json:"updated_at"`
}

func githubTask(ref provider.Ref, number int64, raw json.RawMessage) (provider.Task, error) {
	var result struct {
		githubIssueFields
		Method string             `json:"method"`
		Issue  *githubIssueFields `json:"issue"`
	}
	if err := decodeObject(raw, &result); err != nil {
		return provider.Task{}, err
	}
	issue := result.githubIssueFields
	if result.Method != "" || result.Issue != nil {
		if result.Method != "get" || result.Issue == nil || issue.Number != 0 {
			return provider.Task{}, fmt.Errorf("%w: invalid github issue_read envelope", ErrInvalidResult)
		}
		issue = *result.Issue
	}
	if issue.Number != 0 && issue.Number != number {
		return provider.Task{}, fmt.Errorf("github returned a different issue number: %w", provider.ErrConflict)
	}
	u, ok := httpsURL(issue.HTMLURL)
	if issue.Number == 0 || strings.TrimSpace(issue.Title) == "" || !ok ||
		!strings.EqualFold(u.Host, "github.com") || u.RawQuery != "" || u.Fragment != "" {
		return provider.Task{}, fmt.Errorf("%w: github issue requires number, title and github.com url", ErrInvalidResult)
	}
	if strings.EqualFold(u.Path, "/"+ref.Scope+"/pull/"+ref.ID) {
		return provider.Task{}, fmt.Errorf("github result is a pull request: %w", provider.ErrNotFound)
	}
	if !strings.EqualFold(u.Path, "/"+ref.Scope+"/issues/"+ref.ID) {
		return provider.Task{}, fmt.Errorf("github returned a different issue url: %w", provider.ErrConflict)
	}
	state := provider.TaskState{ID: "open", Name: "Open"}
	switch issue.State {
	case "closed":
		state = provider.TaskState{ID: "closed", Name: "Closed", Terminal: true}
	case "open":
	default:
		return provider.Task{}, fmt.Errorf("%w: unknown github issue state", ErrInvalidResult)
	}
	var assignee string
	if len(issue.Assignees) != 0 {
		assignee = issue.Assignees[0]
	}
	body, template, err := githubissues.DecodeIssueBody(issue.Body)
	if err != nil {
		return provider.Task{}, err
	}
	return provider.Task{
		Ref: ref, Key: ref.Scope + "#" + ref.ID, Title: issue.Title, URL: u.String(),
		Description: body, Template: template, State: state, Type: issue.IssueType, Assignee: assignee,
		Labels: issue.Labels, Revision: issue.UpdatedAt,
	}, nil
}
