package legacy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/nscaledev/agentflow/internal/atlassian"
	"github.com/nscaledev/agentflow/internal/flowmap"
	"github.com/nscaledev/agentflow/internal/provider"
)

const jiraSite = "https://agentflow.example"

var jiraTestRef = provider.Ref{Instance: "jira", Scope: "PLAT", ID: "PLAT-17"}

type jiraMock struct {
	issue       *atlassian.Issue
	issueErr    error
	search      func(jql string, max int) ([]atlassian.Issue, error)
	create      func(project, kind, title, description string, labels []string, link string) (string, error)
	transitions func() []atlassian.Transition
	transition  func(key, id string) error
	comment     func(key, body string) error
	assign      func(key, accountID string) error
}

func (m *jiraMock) GetIssue(context.Context, string) (*atlassian.Issue, error) {
	return m.issue, m.issueErr
}

func (m *jiraMock) Search(_ context.Context, jql string, max int) ([]atlassian.Issue, error) {
	return m.search(jql, max)
}

func (m *jiraMock) CreateIssue(_ context.Context, project, kind, title, description string, labels []string, link string) (string, error) {
	return m.create(project, kind, title, description, labels, link)
}

func (m *jiraMock) Transitions(context.Context, string) ([]atlassian.Transition, error) {
	return m.transitions(), nil
}

func (m *jiraMock) DoTransition(_ context.Context, key, id string) error {
	return m.transition(key, id)
}

func (m *jiraMock) RemoteLinks(context.Context, string) ([]atlassian.Page, error) {
	return []atlassian.Page{{Title: "Design", URL: jiraSite + "/wiki/design"}}, nil
}

func (m *jiraMock) AddComment(_ context.Context, key, body string) error {
	return m.comment(key, body)
}

func (m *jiraMock) Myself(context.Context) (atlassian.User, error) {
	return atlassian.User{AccountID: "me-123", DisplayName: "A Developer"}, nil
}

func (m *jiraMock) Assign(_ context.Context, key, accountID string) error {
	return m.assign(key, accountID)
}

func TestJiraTaskMapping(t *testing.T) {
	var issue atlassian.Issue
	err := json.Unmarshal([]byte(`{"key":"PLAT-17","fields":{
		"summary":"Build the adapter","description":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Use the legacy client"}]}]},
		"status":{"name":"Done","statusCategory":{"key":"done"}},"issuetype":{"name":"Story"},"priority":{"name":"High"},
		"assignee":{"accountId":"account-1","displayName":"A Developer"},"updated":"revision",
		"parent":{"key":"CORE-2","fields":{"summary":"Move to providers"}}
	}}`), &issue)
	if err != nil {
		t.Fatal(err)
	}
	mock := &jiraMock{issue: &issue, search: func(jql string, limit int) ([]atlassian.Issue, error) {
		want := `project = "PLAT" AND text ~ "a \"phrase\"" AND assignee = currentUser() AND statusCategory != Done ORDER BY priority DESC, updated DESC`
		if jql != want || limit != 7 {
			t.Fatalf("search query = %q with limit %d", jql, limit)
		}
		return []atlassian.Issue{issue}, nil
	}}
	jira := NewJira("jira", jiraSite+"/", mock, nil)
	parent := provider.Ref{Instance: "jira", Scope: "CORE", ID: "CORE-2"}
	want := provider.Task{
		Ref: jiraTestRef, Key: jiraTestRef.ID, URL: jiraSite + "/browse/PLAT-17", Title: "Build the adapter",
		Description: "Use the legacy client", Type: "Story", Priority: "High", Assignee: "A Developer", Revision: "revision",
		State: provider.TaskState{ID: "Done", Name: "Done", Category: "done", Terminal: true},
		Links: []provider.Link{
			{Title: "Move to providers", URL: jiraSite + "/browse/CORE-2", Ref: &parent},
			{Title: "Design", URL: jiraSite + "/wiki/design"},
		},
	}
	if got, err := jira.Get(t.Context(), jiraTestRef); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("get = %+v, %v", got, err)
	}
	want.Links = want.Links[:1]
	found, err := jira.Find(t.Context(), provider.TaskQuery{Scope: "PLAT", Text: `a "phrase"`, AssignedToMe: true, OpenOnly: true, Limit: 7})
	if err != nil || !reflect.DeepEqual(found, []provider.Task{want}) {
		t.Fatalf("find = %+v, %v", found, err)
	}
	mock.issueErr = &atlassian.APIError{Status: http.StatusNotFound}
	if _, err := jira.Get(t.Context(), jiraTestRef); !errors.Is(err, provider.ErrNotFound) || !errors.Is(err, mock.issueErr) {
		t.Fatalf("missing issue returned %v", err)
	}
}

func TestJiraCreate(t *testing.T) {
	linkErr := errors.New("link request failed")
	mock := &jiraMock{create: func(project, kind, title, description string, labels []string, link string) (string, error) {
		if project != "CORE" || kind != "Task" || title != "Follow up" || description != "Background" ||
			!slices.Equal(labels, []string{"agentflow"}) || link != jiraTestRef.ID {
			t.Fatalf("create args = %q, %q, %q, %q, %v, %q", project, kind, title, description, labels, link)
		}
		return "CORE-2", linkErr
	}}
	jira := NewJira("jira", jiraSite, mock, nil)
	draft := provider.TaskDraft{Scope: "CORE", Title: "Follow up", Description: "Background", Labels: []string{"agentflow"}, Links: []provider.Link{{Ref: &jiraTestRef}}}
	got, err := jira.Create(t.Context(), draft, provider.WriteOptions{})
	want := provider.WriteResult{Ref: provider.Ref{Instance: "jira", Scope: "CORE", ID: "CORE-2"}, Status: provider.Applied, URL: jiraSite + "/browse/CORE-2"}
	if got != want || !errors.Is(err, linkErr) {
		t.Fatalf("partial create = %+v, %v", got, err)
	}
	linkErr = nil
	if got, err := jira.Create(t.Context(), draft, provider.WriteOptions{}); got != want || err != nil {
		t.Fatalf("create = %+v, %v", got, err)
	}
	unsupported := NewJira("jira", jiraSite, &jiraMock{}, nil)
	for _, tc := range []struct {
		name     string
		priority string
		links    []provider.Link
		options  provider.WriteOptions
	}{
		{name: "priority", priority: "High"},
		{name: "external link", links: []provider.Link{{URL: jiraSite}}},
		{name: "multiple links", links: []provider.Link{{Ref: &jiraTestRef}, {Ref: &jiraTestRef}}},
		{name: "operation ID", options: provider.WriteOptions{OperationID: "request-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			draft.Priority, draft.Links = tc.priority, tc.links
			if _, err := unsupported.Create(t.Context(), draft, tc.options); !errors.Is(err, provider.ErrUnsupported) {
				t.Fatalf("create error = %v", err)
			}
		})
	}
}

func TestJiraCommentAndAssign(t *testing.T) {
	mock := &jiraMock{
		issue: &atlassian.Issue{Key: jiraTestRef.ID},
		comment: func(key, body string) error {
			if key != jiraTestRef.ID || body != "Progress\n\nReady for review" {
				t.Fatalf("comment args = %q, %q", key, body)
			}
			return nil
		},
		assign: func(key, account string) error {
			if key != jiraTestRef.ID || account != "me-123" {
				t.Fatalf("assign args = %q, %q", key, account)
			}
			return nil
		},
	}
	jira := NewJira("jira", jiraSite, mock, nil)
	options := provider.WriteOptions{ExpectedRevision: "revision"}
	if _, err := jira.Comment(t.Context(), jiraTestRef, "blocked", options); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("conditional comment returned %v", err)
	}
	wrong := jiraTestRef
	wrong.Instance = "another-site"
	if _, err := jira.Comment(t.Context(), wrong, "blocked", provider.WriteOptions{}); err == nil {
		t.Fatal("comment accepted a reference for another site")
	}
	if got, err := jira.Comment(t.Context(), jiraTestRef, "Progress\n\nReady for review", provider.WriteOptions{}); err != nil || got.Status != provider.Applied {
		t.Fatalf("comment = %+v, %v", got, err)
	}
	if got, err := jira.AssignToMe(t.Context(), jiraTestRef, provider.WriteOptions{}); err != nil || got.Status != provider.Applied {
		t.Fatalf("assign = %+v, %v", got, err)
	}
	mock.assign = nil
	mock.issue.Fields.Assignee = &atlassian.User{AccountID: "me-123"}
	mock.issue.Fields.Updated = "assigned-revision"
	if got, err := jira.AssignToMe(t.Context(), jiraTestRef, provider.WriteOptions{}); err != nil || got.Status != provider.AlreadyApplied || got.Revision != "assigned-revision" {
		t.Fatalf("repeat assignment = %+v, %v", got, err)
	}
}

func TestJiraTransition(t *testing.T) {
	status := func(name, category string) atlassian.Status {
		s := atlassian.Status{Name: name}
		s.Category.Key = category
		return s
	}
	todo := status("To Do", "new")
	active := status("In Progress", "indeterminate")
	review := status("In Review", "indeterminate")
	done := status("Done", "done")
	flow := &flowmap.Map{Projects: map[string]flowmap.Project{"PLAT": {Order: []string{active.Name, review.Name}}}}
	for _, tc := range []struct {
		name        string
		from        atlassian.Status
		to          atlassian.Status
		destination atlassian.Status
		concurrent  atlassian.Status
		want        provider.WriteStatus
	}{
		{name: "advance", from: todo, to: active, destination: active, want: provider.Applied},
		{name: "already there", from: active, to: active, want: provider.AlreadyApplied},
		{name: "terminal", from: done, to: active},
		{name: "backward category", from: active, to: todo, destination: todo},
		{name: "backward project order", from: review, to: active},
		{name: "missing current status name", from: status("", "new"), to: active},
		{name: "transition label is not destination", from: active, to: review, destination: todo},
		{name: "unknown destination category", from: todo, to: active, destination: status(active.Name, "")},
		{name: "concurrent advance to terminal", from: todo, to: active, destination: active, concurrent: done},
		{name: "concurrent advance to target", from: todo, to: active, destination: active, concurrent: active, want: provider.AlreadyApplied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue := &atlassian.Issue{Key: jiraTestRef.ID}
			issue.Fields.Status = tc.from
			issue.Fields.Updated = "observed-revision"
			applied := false
			mock := &jiraMock{
				issue: issue,
				transitions: func() []atlassian.Transition {
					if tc.destination.Name == "" {
						t.Fatal("listed transitions for a state that should have been handled before lookup")
					}
					if tc.concurrent.Name != "" {
						issue.Fields.Status = tc.concurrent
					}
					return []atlassian.Transition{{ID: "42", Name: tc.to.Name, To: tc.destination}}
				},
				transition: func(key, id string) error {
					if key != jiraTestRef.ID || id != "42" {
						t.Fatalf("transition args = %q, %q", key, id)
					}
					applied = true
					return nil
				},
			}
			jira := NewJira("jira", jiraSite, mock, flow)
			got, err := jira.Transition(t.Context(), jiraTestRef, provider.TaskState{ID: tc.to.Name}, provider.WriteOptions{})
			if tc.want == "" {
				if applied || !errors.Is(err, provider.ErrConflict) {
					t.Fatalf("blocked transition = %+v, %v, wrote = %t", got, err, applied)
				}
				return
			}
			if err != nil || got.Status != tc.want || got.Ref != jiraTestRef || applied != (tc.want == provider.Applied) {
				t.Fatalf("transition = %+v, %v, wrote = %t", got, err, applied)
			}
			if got.Status == provider.AlreadyApplied && got.Revision != "observed-revision" {
				t.Fatalf("unchanged revision = %q", got.Revision)
			}
		})
	}
}
