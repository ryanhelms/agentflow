package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
)

type jiraNativeFixture struct {
	reads     []native.Call
	writes    []native.Call
	account   string
	issueFrom string
	boundFrom string
	route     string
	missing   string
	writeErr  error
}

func (f *jiraNativeFixture) Prepare(_ context.Context, call native.Call) (native.WriteBinding, error) {
	if call.Tool == f.missing {
		return native.WriteBinding{}, native.ErrUnavailable
	}
	return native.WriteBinding{Provider: call.Provider, Server: call.Server, Tool: call.Tool, Origin: "https://mcp.atlassian.com", Connection: f.route}, nil
}

func (f *jiraNativeFixture) Write(_ context.Context, binding native.WriteBinding, call native.Call) (json.RawMessage, error) {
	if binding.Connection != f.route || binding.Tool != call.Tool {
		return nil, native.ErrDenied
	}
	f.writes = append(f.writes, call)
	if f.writeErr != nil {
		return nil, f.writeErr
	}
	if call.Tool == "createJiraIssue" {
		return json.RawMessage(`{"structuredContent":{"key":"PLAT-20"}}`), nil
	}
	return json.RawMessage(`{"structuredContent":{"success":true}}`), nil
}

func (f *jiraNativeFixture) ReadForWrite(_ context.Context, call native.Call) (json.RawMessage, error) {
	return f.readJira(call, f.issueFrom)
}

func (f *jiraNativeFixture) ReadBound(_ context.Context, binding native.WriteBinding, call native.Call) (json.RawMessage, error) {
	if binding.Connection != f.route {
		return nil, native.ErrDenied
	}
	from := f.issueFrom
	if f.boundFrom != "" {
		from = f.boundFrom
	}
	return f.readJira(call, from)
}

func (f *jiraNativeFixture) Read(_ context.Context, call native.Call) (json.RawMessage, error) {
	return f.readJira(call, f.issueFrom)
}

func (f *jiraNativeFixture) readJira(call native.Call, from string) (json.RawMessage, error) {
	f.reads = append(f.reads, call)
	if call.Provider != "jira" || call.Server != "atlassian" || call.Arguments["cloudId"] != "https://tasks.example" {
		return nil, fmt.Errorf("wrong test route: %+v", call)
	}
	if call.Tool == f.missing {
		return nil, native.ErrUnavailable
	}
	key, _ := call.Arguments["issueIdOrKey"].(string)
	tool := call.Tool
	if tool == "executeRead" {
		tool, _ = call.Arguments["name"].(string)
		inputs, _ := call.Arguments["inputs"].(map[string]any)
		key, _ = inputs["issueIdOrKey"].(string)
	}
	switch tool {
	case "getJiraIssue":
		project, _, _ := strings.Cut(key, "-")
		body := fmt.Sprintf(`{"key":%q,"fields":{"summary":"Fixture ticket","status":{"name":%q,"statusCategory":{"key":"new"}},"project":{"key":%q},"parent":{"key":"OPS-4","fields":{"summary":"Parent"}}}}`, key, from, project)
		return json.RawMessage(body), nil
	case "getJiraCurrentUser":
		return json.RawMessage(fmt.Sprintf(`{"accountId":%q}`, f.account)), nil
	case "listJiraIssueTransitions":
		return json.RawMessage(`{"transitions":[{"id":"29","name":"Send to testers","to":{"name":"QA","statusCategory":{"key":"indeterminate"}}}]}`), nil
	case "listJiraIssueRemoteIssueLinks":
		body := fmt.Sprintf(`{"remoteLinks":[{"object":{"title":%q,"url":%q}},{"object":{"title":"Source","url":"https://github.example/other"}},{"object":{"title":"Unsafe","url":"javascript:/wiki/page"}}]}`, key+" design", "https://docs.example/wiki/"+key)
		return json.RawMessage(body), nil
	default:
		return nil, fmt.Errorf("unexpected jira fixture tool: %s", call.Tool)
	}
}

func jiraNativeApp(t *testing.T) (*App, *jiraNativeFixture, context.Context) {
	t.Helper()
	a := legacyApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "tasks.example" {
			t.Errorf("unexpected legacy Jira call %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	f := &jiraNativeFixture{route: "fixture-session-1", account: "fixture-account", issueFrom: "Open"}
	return a, f, native.WithWriteHost(native.WithHost(t.Context(), f), f)
}

func TestJiraNativeContextFindsIssueAndParentRemoteLinks(t *testing.T) {
	a, fixture, ctx := jiraNativeApp(t)
	fixture.missing = "listJiraIssueRemoteIssueLinks"
	out, err := a.Context(ctx, "PLAT-1")
	if err != nil || len(out.Docs) != 2 || out.Docs[0].Title != "OPS-4 design" || out.Docs[1].Title != "PLAT-1 design" {
		t.Fatalf("context = %+v, error = %v", out, err)
	}
	if len(fixture.writes) != 0 {
		t.Fatalf("context dispatched writes: %v", fixture.writes)
	}
}

func TestJiraNativeCommentBindsPayloadAndConnection(t *testing.T) {
	a, fixture, ctx := jiraNativeApp(t)
	const body = "<script>\n**literal**"
	preview, err := a.Comment(ctx, "PLAT-1", body, "")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, preview)
	if _, err := a.Comment(ctx, "PLAT-1", "different text", token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("changed payload error = %v", err)
	}
	fixture.route = "fixture-session-2"
	if _, err := a.Comment(ctx, "PLAT-1", body, token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("changed connection error = %v", err)
	}
	fixture.route = "fixture-session-1"
	if len(fixture.writes) != 0 {
		t.Fatalf("preview dispatched writes: %v", fixture.writes)
	}
	if _, err := a.Comment(ctx, "PLAT-1", body, token); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Comment(ctx, "PLAT-1", body, token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("replay error = %v", err)
	}
	if len(fixture.writes) != 1 || fixture.writes[0].Tool != "addOrEditJiraIssueComment" ||
		fixture.writes[0].Arguments["commentBody"] != "<p>&lt;script&gt;<br>**literal**</p>" {
		t.Fatalf("writes = %+v", fixture.writes)
	}
}

func TestJiraNativeTransitionRechecksForwardState(t *testing.T) {
	a, fixture, ctx := jiraNativeApp(t)
	out, err := a.Transition(ctx, "PLAT-1", "pr.merged", true, "")
	if err != nil || out.(TransitionResult).To != "QA" || len(fixture.writes) != 0 {
		t.Fatalf("dry run = %+v, error = %v, writes = %v", out, err, fixture.writes)
	}
	preview, err := a.Transition(ctx, "PLAT-1", "pr.merged", false, "")
	if err != nil {
		t.Fatal(err)
	}
	fixture.boundFrom = "Review"
	if _, err := a.Transition(ctx, "PLAT-1", "pr.merged", false, confirmation(t, preview)); !errors.Is(err, provider.ErrConflict) || len(fixture.writes) != 0 {
		t.Fatalf("changed source status error = %v, writes = %v", err, fixture.writes)
	}
}

func TestJiraNativeStartWritesInOrder(t *testing.T) {
	a, fixture, ctx := jiraNativeApp(t)
	if err := os.WriteFile(filepath.Join(a.Repo.Dir, "flow.yml"), []byte("projects:\n  PLAT:\n    branch.created: QA\n    order: [Open, QA]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := a.Start(ctx, "PLAT-1", false, "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Start(ctx, "PLAT-1", false, confirmation(t, preview))
	if err != nil {
		t.Fatal(err)
	}
	result := out.(StartResult)
	if !result.Assigned || result.Transition == nil || !result.Transition.Applied {
		t.Fatalf("start = %+v", result)
	}
	if len(fixture.writes) != 2 {
		t.Fatalf("writes = %+v", fixture.writes)
	}
	if fixture.writes[0].Tool != "editJiraIssue" || fixture.writes[0].Arguments["assignee"] != fixture.account ||
		fixture.writes[1].Tool != "transitionJiraIssue" || fixture.writes[1].Arguments["transitionId"] != "29" {
		t.Fatalf("writes = %+v", fixture.writes)
	}
}

func TestJiraNativeCreateAndRemoteLink(t *testing.T) {
	a, fixture, ctx := jiraNativeApp(t)
	if _, err := a.Repo.Git(ctx, "switch", "main"); err != nil {
		t.Fatal(err)
	}
	preview, err := a.CreateTicket(ctx, "PLAT", "Task", "Follow up", "Details", "", "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.CreateTicket(ctx, "PLAT", "Task", "Follow up", "Details", "", confirmation(t, preview))
	if err != nil || out.(map[string]string)["key"] != "PLAT-20" {
		t.Fatalf("create = %+v, error = %v", out, err)
	}
	if len(fixture.writes) != 1 || fixture.writes[0].Tool != "createJiraIssue" ||
		!reflect.DeepEqual(fixture.writes[0].Arguments["labels"], []string{"devflow"}) {
		t.Fatalf("writes = %+v", fixture.writes)
	}

	const linkURL = "https://docs.example/wiki/spaces/DOC/pages/1"
	preview, err = a.LinkJiraRemote(ctx, "PLAT-1", linkURL, "Runbook", "")
	if err != nil {
		t.Fatal(err)
	}
	out, err = a.LinkJiraRemote(ctx, "PLAT-1", linkURL, "Runbook", confirmation(t, preview))
	if err != nil || out.(map[string]any)["linked"] != true {
		t.Fatalf("link = %+v, error = %v", out, err)
	}
	if len(fixture.writes) != 2 || fixture.writes[1].Tool != "createJiraIssueRemoteIssueLink" ||
		fixture.writes[1].Arguments["url"] != linkURL || fixture.writes[1].Arguments["title"] != "Runbook" {
		t.Fatalf("writes = %+v", fixture.writes)
	}
}

func TestJiraNativeEODStopsOnUncertainDispatch(t *testing.T) {
	a, fixture, ctx := jiraNativeApp(t)
	notes := map[string]string{"PLAT-1": "One", "OPS-4": "Two"}
	preview, err := a.PostEOD(ctx, notes, "")
	if err != nil {
		t.Fatal(err)
	}
	fixture.writeErr = native.ErrWriteUncertain
	out, err := a.PostEOD(ctx, notes, confirmation(t, preview))
	if !errors.Is(err, native.ErrWriteUncertain) || !reflect.DeepEqual(out, map[string]bool{"OPS-4": false}) {
		t.Fatalf("eod = %+v, error = %v", out, err)
	}
	if len(fixture.writes) != 1 || fixture.writes[0].Arguments["issueIdOrKey"] != "OPS-4" {
		t.Fatalf("writes = %+v", fixture.writes)
	}
}

func TestJiraLinkedCreationKeepsLegacyOrdering(t *testing.T) {
	var writes []string
	a := legacyApp(t, func(w http.ResponseWriter, r *http.Request) {
		writes = append(writes, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/rest/api/3/issue":
			fmt.Fprint(w, `{"key":"PLAT-20"}`)
		case "/rest/api/3/issueLink":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Jira request: %s", r.URL.Path)
		}
	})
	fixture := &jiraNativeFixture{route: "fixture-session"}
	ctx := native.WithWriteHost(t.Context(), fixture)
	preview, err := a.CreateTicket(ctx, "PLAT", "Task", "Follow up", "Details", "PLAT-1", "")
	if err != nil || len(writes) != 0 {
		t.Fatalf("preview = %+v, error = %v, writes = %v", preview, err, writes)
	}
	out, err := a.CreateTicket(ctx, "PLAT", "Task", "Follow up", "Details", "PLAT-1", confirmation(t, preview))
	if err != nil || out.(map[string]string)["key"] != "PLAT-20" || len(fixture.writes) != 0 ||
		!reflect.DeepEqual(writes, []string{"POST /rest/api/3/issue", "POST /rest/api/3/issueLink"}) {
		t.Fatalf("create = %+v, error = %v, legacy writes = %v, native writes = %v", out, err, writes, fixture.writes)
	}
}
