package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/flowmap"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
)

type taskWriteFixture struct {
	connection   string
	prepareErr   error
	t            *testing.T
	read         func(native.Call) (json.RawMessage, error)
	write        func(native.Call) (json.RawMessage, error)
	prepared     native.Call
	preparations int
	writes       int
}

func (f *taskWriteFixture) Prepare(_ context.Context, call native.Call) (native.WriteBinding, error) {
	f.preparations++
	f.prepared = call
	return f.binding(), f.prepareErr
}

func (f *taskWriteFixture) binding() native.WriteBinding {
	return native.WriteBinding{Provider: f.prepared.Provider, Server: f.prepared.Server, Tool: f.prepared.Tool,
		Origin: "https://provider.example.test", Connection: f.connection}
}

func (f *taskWriteFixture) ReadBound(_ context.Context, binding native.WriteBinding, call native.Call) (json.RawMessage, error) {
	f.t.Helper()
	if binding != f.binding() || call.Provider != binding.Provider || call.Server != binding.Server || f.read == nil {
		f.t.Fatalf("unexpected bound read %+v on %+v", call, binding)
	}
	return f.read(call)
}

func (f *taskWriteFixture) Write(_ context.Context, binding native.WriteBinding, call native.Call) (json.RawMessage, error) {
	f.t.Helper()
	if binding != f.binding() || !reflect.DeepEqual(call, f.prepared) || f.write == nil {
		f.t.Fatalf("unexpected write %+v on %+v", call, binding)
	}
	f.writes++
	return f.write(call)
}

func taskWriteContext(t *testing.T, f *taskWriteFixture) context.Context {
	t.Helper()
	t.Setenv("GITHUB_RUN_ID", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("DEVFLOW_OPERATION_ID", "")
	f.t = t
	f.connection = "fixture-connection"
	return native.WithWriteHost(t.Context(), f)
}

func TestNativeGitHubCreateBindsRepositoryPayloadAndConnection(t *testing.T) {
	a, fallback := issueApp(t)
	f := &taskWriteFixture{
		read: func(call native.Call) (json.RawMessage, error) {
			if call.Tool != "search_repositories" || call.Arguments["query"] != `"repo" in:name user:other repo:other/repo` {
				t.Fatalf("repository probe %+v", call)
			}
			return json.RawMessage(`{"items":[{"full_name":"Other/Repo","id":55}]}`), nil
		},
		write: func(call native.Call) (json.RawMessage, error) {
			want := map[string]any{"owner": "other", "repo": "repo", "method": "create", "title": "Pilot", "body": "Description", "labels": []string{"triage"}}
			if call.Tool != "issue_write" || !reflect.DeepEqual(call.Arguments, want) {
				t.Fatalf("write %+v, want %+v", call, want)
			}
			return json.RawMessage(`{"structuredContent":{"id":"1234","url":"https://github.com/Other/Repo/issues/99"}}`), nil
		},
	}
	ctx := taskWriteContext(t, f)
	create := func(description, token string) (any, error) {
		return a.CreateTicket(ctx, "Other/Repo", "", "Pilot", description, "", token)
	}
	out, err := create("Description", "")
	if err != nil || f.writes != 0 {
		t.Fatalf("preview %+v, %v, writes %d", out, err, f.writes)
	}
	token := confirmation(t, out)
	if _, err := create("Changed", token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("edited payload matched approval: %v", err)
	}
	f.connection = "different-connection"
	if _, err := create("Description", token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("changed connection matched approval: %v", err)
	}
	f.connection = "fixture-connection"
	out, err = create("Description", token)
	if err != nil || !reflect.DeepEqual(out, map[string]string{"key": "other/repo#99"}) || f.writes != 1 || len(fallback.writes) != 0 {
		t.Fatalf("create %+v, %v, native writes %d, fallback writes %v", out, err, f.writes, fallback.writes)
	}
	if _, err := create("Description", token); !errors.Is(err, policy.ErrToken) || f.writes != 1 {
		t.Fatalf("replay %v, writes %d", err, f.writes)
	}
	reopened, err := Open(ctx, a.Repo.Dir, a.Client)
	if err != nil {
		t.Fatal(err)
	}
	a = reopened
	if _, err := create("Description", token); !errors.Is(err, policy.ErrToken) || f.writes != 1 {
		t.Fatalf("replay after reopening %v, writes %d", err, f.writes)
	}
}

func TestNativeGitHubCommentFallbackAndUncertainWrite(t *testing.T) {
	a, fallback := issueApp(t)
	a.Gate.Autonomy.Remote = config.Auto
	var readErr error
	f := &taskWriteFixture{
		read: func(call native.Call) (json.RawMessage, error) {
			if readErr != nil {
				return nil, readErr
			}
			return json.RawMessage(`{"method":"get","issue":{"number":42,"title":"Pilot","state":"open","html_url":"https://github.com/org/repo/issues/42"}}`), nil
		},
		write: func(call native.Call) (json.RawMessage, error) {
			if call.Tool != "add_issue_comment" || call.Arguments["issue_number"] != int64(42) || !strings.HasPrefix(call.Arguments["body"].(string), "Status") {
				t.Fatalf("comment %+v", call)
			}
			return nil, errors.New("uncertain provider response")
		},
	}
	ctx := taskWriteContext(t, f)
	comment := func(ctx context.Context, token string) (any, error) { return a.Comment(ctx, "#42", "Status", token) }
	out, err := comment(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, out)
	readErr = native.ErrUnavailable
	if _, err := comment(ctx, token); !errors.Is(err, policy.ErrToken) || len(fallback.writes) != 0 {
		t.Fatalf("fallback changed approval: %v, writes %v", err, fallback.writes)
	}
	readErr = nil
	f.prepareErr = native.ErrDenied
	if _, err := comment(ctx, ""); !errors.Is(err, native.ErrDenied) || len(fallback.writes) != 0 {
		t.Fatalf("denial fell back: %v, writes %v", err, fallback.writes)
	}
	f.prepareErr = nil
	if _, err := comment(ctx, token); err == nil || !strings.Contains(err.Error(), "uncertain") || f.writes != 1 || len(fallback.writes) != 0 {
		t.Fatalf("uncertain comment %v, native writes %d, fallback writes %v", err, f.writes, fallback.writes)
	}
	if _, err := comment(ctx, token); !errors.Is(err, policy.ErrToken) || f.writes != 1 {
		t.Fatalf("replayed comment: %v, writes %d", err, f.writes)
	}
	f.prepareErr = native.ErrUnavailable
	if _, err := comment(ctx, ""); err != nil || f.writes != 1 || len(fallback.writes) != 1 {
		t.Fatalf("missing capability fallback %v, native writes %d, fallback writes %v", err, f.writes, fallback.writes)
	}
	f.prepareErr = nil
	t.Setenv("DEVFLOW_OPERATION_ID", "fixture-op")
	preparations := f.preparations
	if _, err := comment(ctx, ""); err != nil || f.preparations != preparations || len(fallback.writes) != 2 || fallback.writes[1] == "" {
		t.Fatalf("automation path %v, preparations %d, fallback writes %v", err, f.preparations, fallback.writes)
	}
}

func TestNativeGitHubCloseChecksExactIssueAndRemainsIdempotent(t *testing.T) {
	a, fallback := issueApp(t)
	state, path := "open", "pull"
	f := &taskWriteFixture{
		read: func(call native.Call) (json.RawMessage, error) {
			return json.RawMessage(`{"number":42,"title":"Pilot","state":"` + state + `","html_url":"https://github.com/org/repo/` + path + `/42"}`), nil
		},
		write: func(call native.Call) (json.RawMessage, error) {
			if call.Tool != "issue_write" || call.Arguments["method"] != "update" || call.Arguments["state"] != "closed" || len(call.Arguments) != 5 {
				t.Fatalf("close %+v", call)
			}
			state = "closed"
			fallback.task.State = provider.TaskState{ID: "closed", Name: "Closed", Terminal: true}
			return json.RawMessage(`{"id":"900","url":"https://github.com/org/repo/issues/42"}`), nil
		},
	}
	ctx := taskWriteContext(t, f)
	closeIssue := func(token string) (any, error) { return a.Transition(ctx, "#42", flowmap.PRMerged, false, token) }
	if _, err := closeIssue(""); !errors.Is(err, provider.ErrNotFound) || f.writes != 0 {
		t.Fatalf("accepted a pull request: %v", err)
	}
	path = "issues"
	out, err := closeIssue("")
	if err != nil {
		t.Fatal(err)
	}
	out, err = closeIssue(confirmation(t, out))
	if err != nil || !out.(TransitionResult).Applied || f.writes != 1 || len(fallback.writes) != 0 {
		t.Fatalf("close %+v, %v, writes %d, fallback %v", out, err, f.writes, fallback.writes)
	}
	out, err = closeIssue("")
	if err != nil || out.(TransitionResult).To != "" || f.writes != 1 {
		t.Fatalf("replay %+v, %v, writes %d", out, err, f.writes)
	}
}

func TestNativeAsanaWritesCheckProjectAndAcknowledgement(t *testing.T) {
	a := fileTaskApp(t, `  provider: asana
  settings:
    project: "10"
    transitions: {"10": {"#done": completed}, "20": {"#done": completed}}
`)
	fallback := &fakeIssues{}
	a.tasks = fallback
	project, completed := "10", "false"
	f := &taskWriteFixture{
		read: func(call native.Call) (json.RawMessage, error) {
			if call.Tool != "get_task" || call.Arguments["task_gid"] != "101" {
				t.Fatalf("task probe %+v", call)
			}
			return json.RawMessage(`{"data":{"gid":"101","name":"Design","notes":"Review","completed":` + completed + `,"memberships":[{"project":{"gid":"` + project + `"}}]}}`), nil
		},
		write: func(call native.Call) (json.RawMessage, error) {
			switch call.Tool {
			case "create_tasks":
				want := map[string]any{"default_project": "10", "tasks": []any{map[string]any{"project_id": "10", "name": "Design", "notes": "Review"}}}
				if !reflect.DeepEqual(call.Arguments, want) {
					t.Fatalf("create %+v", call)
				}
				return json.RawMessage(`{"succeeded":[{"gid":"101"}],"failed":[]}`), nil
			case "update_tasks":
				completed = "true"
				return json.RawMessage(`{"succeeded":[{"task_id":"101","completed":true}],"failed":[]}`), nil
			case "add_comment":
				return json.RawMessage(`{"gid":"103","target":{"gid":"999"},"resource_subtype":"comment_added"}`), nil
			default:
				t.Fatalf("unexpected write %+v", call)
				return nil, nil
			}
		},
	}
	ctx := taskWriteContext(t, f)
	create := func(token string) (any, error) { return a.CreateTicket(ctx, "", "", "Design", "Review", "", token) }
	out, err := create("")
	if err != nil {
		t.Fatal(err)
	}
	out, err = create(confirmation(t, out))
	if err != nil || !reflect.DeepEqual(out, map[string]string{"key": "asana/10/101", "url": ""}) {
		t.Fatalf("create %+v, %v", out, err)
	}
	complete := func(key, token string) (any, error) { return a.Transition(ctx, key, "#done", false, token) }
	if _, err := complete("asana/20/101", ""); !errors.Is(err, provider.ErrNotFound) || f.writes != 1 {
		t.Fatalf("wrong project %v, writes %d", err, f.writes)
	}
	out, err = complete("101", "")
	if err != nil {
		t.Fatal(err)
	}
	out, err = complete("101", confirmation(t, out))
	if err != nil || !out.(TransitionResult).Applied || f.writes != 2 {
		t.Fatalf("complete %+v, %v, writes %d", out, err, f.writes)
	}
	out, err = a.Comment(ctx, "101", "Ready", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Comment(ctx, "101", "Ready", confirmation(t, out))
	if !errors.Is(err, nativemap.ErrInvalidResult) || f.writes != 3 || len(fallback.writes) != 0 {
		t.Fatalf("cross-task acknowledgement %v, native writes %d, fallback writes %v", err, f.writes, fallback.writes)
	}
}
