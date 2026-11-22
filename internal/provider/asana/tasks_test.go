package asana

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/provider"
)

const (
	taskURL  = "https://app.asana.com/0/10/100"
	taskJSON = `{"gid":"100","name":"Ship adapter","notes":"Work item","completed":false,
		"permalink_url":"` + taskURL + `","resource_subtype":"default_task","assignee":{"gid":"9"},
		"memberships":[{"project":{"gid":"20"},"section":{"gid":"21","name":"Other"}},
			{"project":{"gid":"10"},"section":{"gid":"11","name":"In progress"}}]}`
	completedJSON = `{"gid":"100","name":"Ship adapter","completed":true,"permalink_url":"` + taskURL + `",
		"memberships":[{"project":{"gid":"10"},"section":{"gid":"11","name":"In progress"}}]}`
)

var taskRef = provider.Ref{Instance: "work", Scope: "10", ID: "100"}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type exchange struct {
	method, path string
	query        url.Values
	body         string
	status       int
	response     string
	err          error
}

func fixture(t *testing.T, steps ...exchange) *Adapter {
	t.Helper()
	call := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if call >= len(steps) {
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
		}
		step := steps[call]
		call++
		if req.Method != step.method || req.URL.Path != "/api/1.0"+step.path ||
			req.URL.Scheme != "https" || req.URL.Host != "app.asana.com" ||
			req.Header.Get("Authorization") != "Bearer mock-token" || req.Header.Get("Accept") != "application/json" {
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
		}
		for key, want := range step.query {
			if got := req.URL.Query()[key]; !slices.Equal(got, want) {
				t.Errorf("query %s = %v, want %v", key, got, want)
			}
		}
		if step.body != "" {
			if req.Header.Get("Content-Type") != "application/json" || req.GetBody != nil {
				t.Fatal("expected JSON write with replay disabled")
			}
			defer req.Body.Close()
			var got, want any
			if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(step.body), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("body = %v, want %v", got, want)
			}
		}
		if step.err != nil {
			return nil, step.err
		}
		recorder := httptest.NewRecorder()
		recorder.WriteHeader(step.status)
		_, _ = recorder.WriteString(step.response)
		return recorder.Result(), nil
	})}
	t.Cleanup(func() {
		if call != len(steps) {
			t.Errorf("made %d requests, want %d", call, len(steps))
		}
	})
	a, err := New(client, "mock-token", Config{Instance: "work", Sections: map[string][]string{
		"10": {"11", "12"},
		"20": {"21"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func getTaskFixture(data string) exchange {
	return exchange{
		method: http.MethodGet, path: "/tasks/100", status: http.StatusOK,
		query:    url.Values{"opt_fields": {taskFields}},
		response: `{"data":` + data + `}`,
	}
}

func TestGetUsesProjectMembership(t *testing.T) {
	a := fixture(t, getTaskFixture(taskJSON), getTaskFixture(taskJSON), getTaskFixture(taskJSON))
	task, err := a.Get(t.Context(), taskRef)
	if err != nil || task.Ref != taskRef || task.Title != "Ship adapter" || task.Description != "Work item" ||
		task.URL != taskURL || task.Type != "default_task" || task.Assignee != "9" ||
		task.State != (provider.TaskState{ID: "11", Name: "In progress"}) {
		t.Fatalf("get = %+v, %v", task, err)
	}
	other := taskRef
	other.Scope = "20"
	task, err = a.Get(t.Context(), other)
	if err != nil || task.Ref != other || task.State.ID != "21" {
		t.Fatalf("get in second project = %+v, %v", task, err)
	}
	other.Scope = "30"
	if _, err := a.Get(t.Context(), other); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("get outside project = %v", err)
	}
}

func TestFindPaginatesAndFilters(t *testing.T) {
	a := fixture(t, exchange{
		method: http.MethodGet, path: "/projects/10/tasks", status: http.StatusOK,
		query: url.Values{"limit": {"1"}, "offset": nil, "completed_since": {"now"}, "opt_fields": {taskFields}},
		response: `{"data":[` + completedJSON + `],"next_page":{
			"offset":"a+/=","uri":"https://elsewhere.invalid/tasks"}}`,
	}, exchange{
		method: http.MethodGet, path: "/projects/10/tasks", status: http.StatusOK,
		query:    url.Values{"limit": {"1"}, "offset": {"a+/="}, "completed_since": {"now"}},
		response: `{"data":[` + taskJSON + `],"next_page":{"offset":"unused"}}`,
	})
	tasks, err := a.Find(t.Context(), provider.TaskQuery{Scope: "10", Text: "WORK ITEM", OpenOnly: true, Limit: 1})
	if err != nil || len(tasks) != 1 || tasks[0].Ref != taskRef || tasks[0].State.ID != "11" {
		t.Fatalf("find = %+v, %v", tasks, err)
	}
}

func TestFindIncludesCompletedAndStopsAtLastPage(t *testing.T) {
	a := fixture(t, exchange{
		method: http.MethodGet, path: "/projects/10/tasks", status: http.StatusOK,
		query:    url.Values{"limit": {"100"}, "completed_since": nil},
		response: `{"data":[` + completedJSON + `],"next_page":null}`,
	})
	tasks, err := a.Find(t.Context(), provider.TaskQuery{Scope: "10"})
	if err != nil || len(tasks) != 1 || !tasks[0].State.Terminal || tasks[0].State.ID != StateCompleted {
		t.Fatalf("find = %+v, %v", tasks, err)
	}
}

func TestCreate(t *testing.T) {
	a := fixture(t, exchange{
		method: http.MethodPost, path: "/tasks", status: http.StatusCreated,
		body:     `{"data":{"name":"Ship adapter","notes":"Work item","projects":["10"]}}`,
		response: `{"data":{"gid":"100","permalink_url":"` + taskURL + `"}}`,
	})
	result, err := a.Create(t.Context(), provider.TaskDraft{Scope: "10", Title: "Ship adapter", Description: "Work item"}, provider.WriteOptions{})
	if err != nil || result != (provider.WriteResult{Ref: taskRef, Status: provider.Applied, URL: taskURL}) {
		t.Fatalf("create = %+v, %v", result, err)
	}
}

func TestTransitionToConfiguredSection(t *testing.T) {
	a := fixture(t, getTaskFixture(taskJSON), exchange{
		method: http.MethodGet, path: "/sections/12", status: http.StatusOK,
		query:    url.Values{"opt_fields": {"gid,project.gid"}},
		response: `{"data":{"gid":"12","project":{"gid":"10"}}}`,
	}, exchange{
		method: http.MethodPost, path: "/sections/12/addTask", status: http.StatusOK,
		body: `{"data":{"task":"100"}}`, response: `{"data":{}}`,
	}, getTaskFixture(strings.ReplaceAll(taskJSON, `"11"`, `"12"`)))
	result, err := a.Transition(t.Context(), taskRef, provider.TaskState{ID: "12"}, provider.WriteOptions{})
	if err != nil || result != (provider.WriteResult{Ref: taskRef, Status: provider.Applied, URL: taskURL}) {
		t.Fatalf("section transition = %+v, %v", result, err)
	}
	result, err = a.Transition(t.Context(), taskRef, provider.TaskState{ID: "12"}, provider.WriteOptions{})
	if err != nil || result.Status != provider.AlreadyApplied {
		t.Fatalf("repeat section transition = %+v, %v", result, err)
	}
}

func TestTransitionRejectsSectionOutsideProject(t *testing.T) {
	a := fixture(t, getTaskFixture(taskJSON), exchange{
		method: http.MethodGet, path: "/sections/12", status: http.StatusOK,
		response: `{"data":{"gid":"12","project":{"gid":"20"}}}`,
	})
	if _, err := a.Transition(t.Context(), taskRef, provider.TaskState{ID: "12"}, provider.WriteOptions{}); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("cross-project section transition = %v", err)
	}
}

func TestTransitionCompletesForwardOnly(t *testing.T) {
	a := fixture(t, getTaskFixture(taskJSON), exchange{
		method: http.MethodPut, path: "/tasks/100", status: http.StatusOK,
		body: `{"data":{"completed":true}}`, response: `{"data":` + completedJSON + `}`,
	}, getTaskFixture(completedJSON), getTaskFixture(completedJSON))
	target := provider.TaskState{ID: StateCompleted, Terminal: true}
	result, err := a.Transition(t.Context(), taskRef, target, provider.WriteOptions{})
	if err != nil || result.Status != provider.Applied {
		t.Fatalf("complete = %+v, %v", result, err)
	}
	result, err = a.Transition(t.Context(), taskRef, target, provider.WriteOptions{})
	if err != nil || result.Status != provider.AlreadyApplied {
		t.Fatalf("repeat complete = %+v, %v", result, err)
	}
	if _, err := a.Transition(t.Context(), taskRef, provider.TaskState{ID: "11"}, provider.WriteOptions{}); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("reopen = %v", err)
	}
}

func TestUnsupportedInputsFailBeforeRequests(t *testing.T) {
	a := fixture(t)
	ctx := t.Context()
	for _, draft := range []provider.TaskDraft{
		{Type: "bug"}, {Priority: "high"}, {Labels: []string{"release"}}, {Links: []provider.Link{{URL: "https://example.com"}}},
	} {
		draft.Scope, draft.Title = "10", "Ship adapter"
		if _, err := a.Create(ctx, draft, provider.WriteOptions{}); !errors.Is(err, provider.ErrUnsupported) {
			t.Errorf("create with %+v = %v", draft, err)
		}
	}
	for _, opts := range []provider.WriteOptions{{OperationID: "op"}, {ExpectedRevision: "rev"}} {
		if _, err := a.Create(ctx, provider.TaskDraft{Scope: "10", Title: "Ship adapter"}, opts); !errors.Is(err, provider.ErrUnsupported) {
			t.Errorf("create with %+v = %v", opts, err)
		}
		if _, err := a.Transition(ctx, taskRef, provider.TaskState{ID: StateCompleted}, opts); !errors.Is(err, provider.ErrUnsupported) {
			t.Errorf("transition with %+v = %v", opts, err)
		}
	}
	for _, target := range []provider.TaskState{{ID: "21"}, {ID: "12", Terminal: true}, {ID: StateOpen}} {
		if _, err := a.Transition(ctx, taskRef, target, provider.WriteOptions{}); !errors.Is(err, provider.ErrUnsupported) {
			t.Errorf("transition to %+v = %v", target, err)
		}
	}
	if _, err := a.Find(ctx, provider.TaskQuery{Scope: "10", AssignedToMe: true}); !errors.Is(err, provider.ErrUnsupported) {
		t.Errorf("search for current user = %v", err)
	}
}

func TestCreateDoesNotRetryUncertainWrite(t *testing.T) {
	a := fixture(t, exchange{
		method: http.MethodPost, path: "/tasks", err: io.ErrUnexpectedEOF,
		body: `{"data":{"name":"Ship adapter","projects":["10"]}}`,
	})
	if _, err := a.Create(t.Context(), provider.TaskDraft{Scope: "10", Title: "Ship adapter"}, provider.WriteOptions{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("create transport error = %v", err)
	}
}

func TestTemplateMetadataAndNativeEdits(t *testing.T) {
	pin := provider.TaskTemplateRef{Name: "work", Revision: "01"}
	link := provider.Ref{Instance: "filesystem", Scope: "design", ID: "capacity"}
	draft := provider.TaskDraft{Title: "Plan", Scope: "10", Description: "## Objective\nEditable", Template: &pin, Links: []provider.Link{{Ref: &link}}}
	notes, err := encodeNotes(draft)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"data": map[string]any{"name": draft.Title, "projects": []string{"10"}, "notes": notes}})
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(notes, "Editable", "Updated in Asana", 1)
	escaped, _ := json.Marshal(changed)
	data := strings.Replace(taskJSON, `"Work item"`, string(escaped), 1)
	a := fixture(t, exchange{method: "POST", path: "/tasks", status: 201, body: string(payload), response: `{"data":{"gid":"100"}}`}, getTaskFixture(data), getTaskFixture(data), getTaskFixture(data))
	if _, err := a.Create(t.Context(), draft, provider.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	task, err := a.Get(t.Context(), taskRef)
	if err != nil || task.Template == nil || *task.Template != pin || !reflect.DeepEqual(task.Links, draft.Links) || task.Description != "## Objective\nUpdated in Asana" {
		t.Fatalf("decoded %+v, %v", task, err)
	}
	target := provider.TaskState{ID: "12"}
	if _, err := a.Transition(t.Context(), taskRef, target, provider.WriteOptions{}); err == nil {
		t.Fatal("accepted template without validation")
	}
	check := func(current provider.Task) error {
		if current.Description != task.Description {
			t.Fatalf("stale task %+v", current)
		}
		return provider.ErrConflict
	}
	if _, err := a.Transition(t.Context(), taskRef, target, provider.WriteOptions{CheckTask: check}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("validation error %v", err)
	}
}

func TestLiteralNotesPrefix(t *testing.T) {
	literal := notesPrefix + "{user text}\n\nMore"
	notes, err := encodeNotes(provider.TaskDraft{Description: literal})
	if err != nil {
		t.Fatal(err)
	}
	body, meta, err := decodeNotes(notes)
	if err != nil || body != literal || meta.Template != nil || len(meta.Links) != 0 {
		t.Fatalf("decoded %q %+v %v", body, meta, err)
	}
}

func TestReadinessChecksAllProjectMemberships(t *testing.T) {
	projectRead := func(id string) exchange {
		return exchange{method: "GET", path: "/projects/" + id + "/tasks", query: url.Values{"limit": {"1"}}, status: 200, response: `{"data":[]}`}
	}
	sectionRead := func(id, project string) exchange {
		return exchange{method: "GET", path: "/sections/" + id, status: 200, response: `{"data":{"gid":"` + id + `","project":{"gid":"` + project + `"}}}`}
	}
	a := fixture(t, projectRead("10"), sectionRead("11", "10"), sectionRead("12", "10"), projectRead("20"), sectionRead("21", "20"))
	if err := a.CheckAccess(t.Context()); err != nil {
		t.Fatal(err)
	}
	a = fixture(t, projectRead("10"), sectionRead("11", "20"))
	if err := a.CheckAccess(t.Context()); !errors.Is(err, provider.ErrNotFound) {
		t.Fatal(err)
	}
}
