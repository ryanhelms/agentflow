package twg

import (
	"errors"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
)

const createMetadata = `{"command":"jira.space.issue-types","request":{"site":"https://example.atlassian.net","idOrKey":"ENG"},"data":{"space":{"key":"ENG","id":"100"},"issueTypes":[{"name":"Task","id":"101","subtask":false}]}}`

func TestJiraCreateWithEmptyDescription(t *testing.T) {
	j := newTestJira(t, "ENG")
	jiraWriteTarget(t, createMetadata)
	draft := provider.TaskDraft{Scope: "ENG", Type: "Task", Title: "Fixture", Labels: []string{"devflow"}}
	p, err := j.PrepareCreate(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	// Mutation inputs are frozen before approval.
	draft.Labels[0] = "changed"
	const receipt = `{"command":"jira.workitem.create","request":{"input":{"site":"https://example.atlassian.net","projectRef":"100","issueTypeRef":"101","summary":"Fixture","labels":["devflow"]}},"data":{"success":true,"issue":{"key":"ENG-42","id":"42","self":"https://example.atlassian.net/rest/api/3/issue/42"}}}`
	const issue = `{"data":[{"id":"42","key":"ENG-42","self":"https://example.atlassian.net/rest/api/3/issue/42","url":"https://example.atlassian.net/browse/ENG-42","summary":"Fixture","description":null,"labels":["devflow"],"creator":{"accountId":"fixture-user"},"project":{"key":"ENG","id":"100"},"issuetype":{"id":"101","name":"Task"}}]}`
	fakeTWG(t, `if [ "$3" = create ]; then
for arg do
  case "$arg" in --description* ) exit 2 ;; esac
done
`+printJSON(receipt)+"\nelse\n"+printJSON(issue)+"\nfi")
	ref, err := p.Apply(t.Context())
	if err != nil || ref.ID != "ENG-42" {
		t.Fatal(ref, err)
	}
	fakeTWG(t, printJSON(`{"error":{"code":"ACCESS_DENIED"}}`)+"\nexit 1")
	if _, err := p.Apply(t.Context()); !errors.Is(err, native.ErrWriteUncertain) || !errors.Is(err, ErrAccessDenied) {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := p.Apply(t.Context()); !errors.Is(err, native.ErrWriteNotDispatched) {
		t.Fatal(err)
	}
}

func TestJiraCreateRejectsUnrepresentableDrafts(t *testing.T) {
	j := newTestJira(t, "ENG")
	jiraWriteTarget(t, createMetadata)
	for _, change := range []func(*provider.TaskDraft){
		func(d *provider.TaskDraft) { d.Description = " padded" },
		func(d *provider.TaskDraft) { d.Title = "NUL\x00" },
		func(d *provider.TaskDraft) { d.Description = "[~accountId:123456789012345678901234]" },
		func(d *provider.TaskDraft) { d.Labels = []string{"two,labels"} },
		func(d *provider.TaskDraft) { d.Links = []provider.Link{{URL: "https://example.invalid"}} },
	} {
		d := provider.TaskDraft{Scope: "ENG", Type: "Task", Title: "Fixture"}
		change(&d)
		if p, err := j.PrepareCreate(t.Context(), d); err == nil || p != nil {
			t.Fatalf("accepted %+v", d)
		}
	}
	for _, metadata := range []string{
		strings.Replace(createMetadata, `"key":"ENG"`, `"key":"OTHER"`, 1),
		strings.Replace(createMetadata, testSite, "https://other.example", 1),
		strings.Replace(createMetadata, `"issueTypes":[`, `"issueTypes":[{"name":"Task","id":"102","subtask":false},`, 1),
		strings.Replace(createMetadata, `"subtask":false`, `"subtask":null`, 1),
	} {
		jiraWriteTarget(t, metadata)
		if _, err := j.PrepareCreate(t.Context(), provider.TaskDraft{Scope: "ENG", Type: "Task", Title: "Fixture"}); !errors.Is(err, ErrAccessDenied) {
			t.Fatal(metadata, err)
		}
	}
}
