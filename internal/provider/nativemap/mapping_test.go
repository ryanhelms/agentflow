package nativemap

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/provider"
)

const fixtureSite = "https://fixture.atlassian.net"

func TestJiraIssue(t *testing.T) {
	ref := provider.Ref{Instance: "work", Scope: "plat", ID: "plat-24"}
	read, err := JiraIssue(ref, fixtureSite+"/")
	if err != nil {
		t.Fatal(err)
	}
	assertCall(t, read.Call(), Jira, GetTask, "getJiraIssue", ref.Instance, `{
		"cloudId":"https://fixture.atlassian.net","issueIdOrKey":"plat-24",
		"fields":["summary","description","status","issuetype","priority","assignee","labels","parent","updated"]
	}`)
	task, err := read.DecodeText(`{
		"key":"PLAT-24","fields":{"summary":"Migrate workers","description":"- [ ] Roll out",
		"status":{"name":"Done","statusCategory":{"key":"done"}},"issuetype":{"name":"Story"},
		"priority":{"name":"High"},"assignee":{"displayName":"Fixture","emailAddress":"ignored@example.test"},
		"labels":["migration"],"updated":"2026-11-21T00:00:00Z",
		"parent":{"key":"OPS-1","fields":{"summary":"Parent"}}}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if task.Ref != (provider.Ref{Instance: "work", Scope: "PLAT", ID: "PLAT-24"}) ||
		task.Key != "PLAT-24" || task.URL != fixtureSite+"/browse/PLAT-24" || task.Title != "Migrate workers" ||
		task.Description != "- [ ] Roll out" || task.State.Name != "Done" || !task.State.Terminal ||
		task.Type != "Story" || task.Priority != "High" || task.Assignee != "Fixture" ||
		task.Revision != "2026-11-21T00:00:00Z" || !reflect.DeepEqual(task.Labels, []string{"migration"}) ||
		len(task.Links) != 1 || task.Links[0].Ref == nil || task.Links[0].Ref.ID != "OPS-1" {
		t.Fatalf("unexpected task: %+v", task)
	}
	assertDecodeError(t, read, `{"key":"PLAT-25","fields":{"summary":"Moved"}}`, provider.ErrConflict)
	assertDecodeError(t, read, `{"key":"PLAT-24","fields":{"summary":"Missing status"}}`, ErrInvalidResult)
	assertDecodeError(t, read, strings.Repeat(" ", MaxResultBytes+1), ErrInvalidResult)
	if _, err := JiraIssue(provider.Ref{Instance: "work", Scope: "OPS", ID: "PLAT-24"}, fixtureSite); err == nil {
		t.Fatal("accepted an issue outside its requested project")
	}
	if _, err := JiraIssue(ref, fixtureSite+"/rest"); err == nil {
		t.Fatal("accepted an origin with a path")
	}
}

func TestConfluencePage(t *testing.T) {
	ref := provider.Ref{Instance: "wiki", Scope: "41", ID: "123456"}
	read, err := ConfluencePage(ref, fixtureSite)
	if err != nil {
		t.Fatal(err)
	}
	assertCall(t, read.Call(), Confluence, GetDocument, "getConfluenceContent", ref.Instance, `{
		"cloudId":"https://fixture.atlassian.net","content_id":"123456","content_format":"markdown",
		"detail":"full","include_metadata":true
	}`)
	document, err := read.Decode(json.RawMessage(`{
		"contentId":"123456","contentType":"page","title":"Runbook","snapshotToken":"v:2",
		"status":"current","metadata":{"spaceId":"41"},"body":{"format":"markdown","value":"# Rollout"}
	}`))
	if err != nil || !reflect.DeepEqual(document, provider.Document{
		Ref: ref, Title: "Runbook", Body: "# Rollout", Revision: "v:2",
		URL: fixtureSite + "/wiki/pages/viewpage.action?pageId=123456",
	}) {
		t.Fatalf("document = %+v, %v", document, err)
	}
	assertDecodeError(t, read, `{"id":"123456","spaceId":"42"}`, provider.ErrConflict)
	assertDecodeError(t, read, `{"id":"123456","spaceId":"41","type":"page","title":"Summary only","excerpt":"not a body"}`, ErrInvalidResult)
	assertDecodeError(t, read, `{"id":"123456","spaceId":"41","type":"blogpost","title":"Blog"}`, provider.ErrUnsupported)
	assertDecodeError(t, read, `{"id":"123456","spaceId":"41","type":"page","title":"Runbook",
		"body":{"format":"markdown","value":"# Rollout","lostFeatures":["time"]}}`, provider.ErrUnsupported)
	keyRef := provider.Ref{Instance: ref.Instance, Scope: "OPS", ID: ref.ID}
	bySpaceKey, err := ConfluencePage(keyRef, fixtureSite)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := bySpaceKey.DecodeText(`{"id":"123456","type":"page","title":"Empty",
		"snapshotToken":"v:3","version":{"number":3},"metadata":{"spaceKey":"OPS","spaceId":"41"},
		"body":{"format":"markdown","value":""}}`)
	if err != nil || doc.Ref != keyRef || doc.Revision != "3" || doc.Body != "" {
		t.Fatalf("key-scoped empty page = %+v, %v", doc, err)
	}

}

func TestAsanaTask(t *testing.T) {
	ref := provider.Ref{Instance: "tasks", Scope: "40", ID: "42"}
	read, err := AsanaTask(ref)
	if err != nil {
		t.Fatal(err)
	}
	assertCall(t, read.Call(), Asana, GetTask, "get_task", ref.Instance, `{
		"task_gid":"42","include_comments":false,"include_subtasks":false
	}`)
	const fixture = `{
		"gid":"42","name":"Run pilot","notes":"Check results","completed":false,
		"resource_subtype":"default_task","assignee":{"gid":"101"},"modified_at":"2026-11-21T00:00:00Z",
		"permalink_url":"https://app.asana.com/0/40/42",
		"memberships":[{"project":{"gid":"99"},"section":{"gid":"6","name":"Other"}},
			{"project":{"gid":"40"},"section":{"gid":"7","name":"In Progress"}}]
	}`
	for _, raw := range []string{fixture, `{"data":` + fixture + `}`} {
		task, err := read.Decode(json.RawMessage(raw))
		if err != nil || task.Ref != ref || task.Title != "Run pilot" || task.Description != "Check results" ||
			task.State != (provider.TaskState{ID: "7", Name: "In Progress"}) || task.Assignee != "101" ||
			task.URL != "https://app.asana.com/0/40/42" || task.Revision == "" {
			t.Fatalf("task = %+v, %v", task, err)
		}
	}
	assertDecodeError(t, read, `{"gid":"42","name":"Outside","completed":true,"memberships":[]}`, provider.ErrNotFound)
	assertDecodeError(t, read, `{"gid":"42","name":"Missing status","memberships":[]}`, ErrInvalidResult)
	assertDecodeError(t, read, `{"gid":"43","name":"Different task"}`, provider.ErrConflict)
}

func TestGitHubIssue(t *testing.T) {
	ref := provider.Ref{Instance: "forge", Scope: "Example/Widget", ID: "68"}
	read, err := GitHubIssue(ref)
	if err != nil {
		t.Fatal(err)
	}
	assertCall(t, read.Call(), GitHub, GetTask, "issue_read", ref.Instance, `{
		"method":"get","owner":"Example","repo":"Widget","issue_number":68
	}`)
	const fixture = `{
		"number":68,"title":"Resolve issue","body":"Some details","state":"closed",
		"html_url":"https://github.com/Example/Widget/issues/68",
		"issue_type":"Bug","labels":["triage"],"assignees":["first","second"],
		"updated_at":"2026-11-21T00:00:00Z"
	}`
	for _, raw := range []string{fixture, `{"method":"get","issue":` + fixture + `}`} {
		task, err := read.Decode(json.RawMessage(raw))
		if err != nil || task.Ref.Scope != "example/widget" || task.Ref.ID != "68" ||
			task.Key != "example/widget#68" || task.Type != "Bug" || !task.State.Terminal ||
			task.Assignee != "first" || task.Description != "Some details" || task.Revision == "" {
			t.Fatalf("task = %+v, %v", task, err)
		}
	}
	assertDecodeError(t, read, strings.Replace(fixture, "/issues/", "/pull/", 1), provider.ErrNotFound)
	assertDecodeError(t, read, strings.Replace(fixture, "Example/Widget", "example/other", 1), provider.ErrConflict)
	assertDecodeError(t, read, `{"method":"get_parent","issue":`+fixture+`}`, ErrInvalidResult)
}

func TestSlackThread(t *testing.T) {
	ref := provider.Ref{Instance: "team", Scope: "C0123456789", ID: "1790771415.332219"}
	read, err := SlackThread(ref)
	if err != nil {
		t.Fatal(err)
	}
	assertCall(t, read.Call(), Slack, ReadSlackThread, "slack_read_thread", ref.Instance, `{
		"channel_id":"C0123456789","message_ts":"1790771415.332219","limit":20,"response_format":"detailed"
	}`)
	const fixture = `{"channel_id":"C0123456789","messages":[
		{"ts":"1790771415.332219","user":"U0123456789","text":"Pilot update"},
		{"ts":"1790771416.012345","thread_ts":"1790771415.332219","text":"Looks good"}
	],"next_cursor":"opaque"}`
	page, err := read.DecodeText(fixture)
	if err != nil || page.Ref != ref || len(page.Messages) != 2 || !page.More ||
		page.Messages[0].Text != "Pilot update" || page.Messages[1].Text != "Looks good" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	page, err = read.DecodeText("Thread in #fixture:\nPilot update")
	if err != nil || page.Presentation == "" || page.Messages != nil || page.Ref != ref {
		t.Fatalf("presentation = %+v, %v", page, err)
	}
	assertDecodeError(t, read, strings.Replace(fixture, ref.Scope, "C0123456788", 1), provider.ErrConflict)
	assertDecodeError(t, read, `{"messages":[]}`, ErrInvalidResult)
	assertDecodeError(t, read, strings.Replace(fixture, `"thread_ts":"1790771415.332219"`, `"thread_ts":"1790771415.332210"`, 1), ErrInvalidResult)
	if _, err := read.DecodeText(`{"messages":`); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("malformed object text = %v", err)
	}

}

func TestUnmappedCapabilities(t *testing.T) {
	for _, tc := range []struct {
		kind Kind
		op   Operation
	}{
		{Jira, "task.transition"}, {Jira, "task.find"}, {Confluence, "document.publish"},
		{Asana, "task.create"}, {Slack, GetDocument}, {GitHub, "code.change.open"},
		{GitHub, GetDocument}, {GitHub, DeliverToSlack}, {"webhook", DeliverToSlack},
	} {
		if capability, err := CapabilityFor(tc.kind, tc.op); !errors.Is(err, provider.ErrUnsupported) || capability.Tool != "" {
			t.Fatalf("%s %s: capability = %+v, %v", tc.kind, tc.op, capability, err)
		}
	}
}

func assertCall(t *testing.T, call Call, kind Kind, op Operation, tool, instance, args string) {
	t.Helper()
	want := Capability{Kind: kind, Operation: op, Tool: tool, Write: op == DeliverToSlack}
	if call.Capability() != want || call.Instance() != instance {
		t.Fatalf("call = %+v", call)
	}
	var gotJSON, wantJSON any
	gotErr := json.Unmarshal(call.Arguments(), &gotJSON)
	wantErr := json.Unmarshal([]byte(args), &wantJSON)
	if gotErr != nil || wantErr != nil || !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("args = %s, want %s (errors %v %v)", call.Arguments(), args, gotErr, wantErr)
	}
}

func assertDecodeError[T any](t *testing.T, read Read[T], raw string, want error) {
	t.Helper()
	if _, err := read.Decode(json.RawMessage(raw)); !errors.Is(err, want) {
		t.Fatalf("decode = %v, want %v", err, want)
	}
}
