package githubissues

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nscaledev/devflow/internal/gh"
	"github.com/nscaledev/devflow/internal/provider"
)

const backlogPath = "/repos/acme/backlog/issues"

var (
	testHosts  sync.Map
	nextHostID atomic.Uint64
)

type testTransport struct{}

func (testTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	value, ok := testHosts.Load(r.URL.Host)
	if !ok {
		return nil, fmt.Errorf("no test handler for %s", r.URL.Host)
	}
	w := httptest.NewRecorder()
	value.(http.HandlerFunc).ServeHTTP(w, r)
	return w.Result(), nil
}

func TestMain(m *testing.M) {
	// gh.Client uses the default transport. Route test hosts to httptest
	// handlers so these tests also work in sandboxes that prohibit sockets.
	http.DefaultTransport = testTransport{}
	os.Exit(m.Run())
}

func testAdapter(t *testing.T, handler http.HandlerFunc) (*Adapter, func(...string)) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	host := fmt.Sprintf("github-%d.test", nextHostID.Add(1))
	testHosts.Store(host, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		handler(w, r)
	}))
	t.Cleanup(func() { testHosts.Delete(host) })
	client := gh.New("")
	client.API = "https://" + host
	a, err := New(client, "github-issues", "Acme/Backlog")
	if err != nil {
		t.Fatal(err)
	}
	return a, func(want ...string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(requests, want) {
			t.Errorf("requests = %v, want %v", requests, want)
		}
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("write JSON: %v", err)
	}
}

func readJSON(t *testing.T, w http.ResponseWriter, r *http.Request, v any) bool {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Errorf("read JSON: %v", err)
		http.Error(w, "invalid input", http.StatusBadRequest)
		return false
	}
	return true
}

func TestGetQualifiedIssues(t *testing.T) {
	a, requests := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case backlogPath + "/42":
			writeJSON(t, w, issue{Number: 42, Title: "Fix it", Body: "  body\n", State: "closed", HTMLURL: "https://example.test/acme/backlog/issues/42", Assignee: account{"alex"}})
		case "/repos/other/bugs/issues/42":
			writeJSON(t, w, issue{Number: 42, State: "open", PullRequest: json.RawMessage(`{}`)})
		default:
			http.NotFound(w, r)
		}
	})
	ref := provider.Ref{Instance: "github-issues", Scope: "ACME/Backlog", ID: "42"}
	got, err := a.Get(t.Context(), ref)
	wantRef := provider.Ref{Instance: ref.Instance, Scope: "acme/backlog", ID: ref.ID}
	if err != nil || got.Ref != wantRef || got.Key != "acme/backlog#42" || got.Title != "Fix it" || got.Description != "  body\n" || got.Assignee != "alex" || got.URL != "https://example.test/acme/backlog/issues/42" || got.State != (provider.TaskState{ID: "closed", Name: "Closed", Terminal: true}) {
		t.Fatalf("get = %+v, %v", got, err)
	}
	ref.Scope = "other/bugs"
	if _, err := a.Get(t.Context(), ref); !errors.Is(err, provider.ErrNotFound) || !strings.Contains(err.Error(), "pull request") {
		t.Fatalf("get PR error = %v", err)
	}
	ref.ID = "43"
	if _, err := a.Get(t.Context(), ref); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("missing issue error = %v", err)
	}
	requests("GET "+backlogPath+"/42", "GET /repos/other/bugs/issues/42", "GET /repos/other/bugs/issues/43")
}

func TestInvalidInputMakesNoRequests(t *testing.T) {
	a, requests := testAdapter(t, http.NotFound)
	for _, ref := range []provider.Ref{
		{Instance: "another", Scope: "acme/backlog", ID: "42"},
		{Instance: a.instance, ID: "42"},
		{Instance: a.instance, Scope: "acme/..", ID: "42"},
		{Instance: a.instance, Scope: "acme/bug?x=y", ID: "42"},
		{Instance: a.instance, Scope: "acme/bug/other", ID: "42"},
		{Instance: a.instance, Scope: "acme/backlog", ID: "#42"},
		{Instance: a.instance, Scope: "acme/backlog", ID: "42/comments"},
		{Instance: a.instance, Scope: "acme/backlog", ID: "0"},
	} {
		if _, err := a.Get(t.Context(), ref); err == nil {
			t.Errorf("accepted reference %+v", ref)
		}
	}
	withoutDefault, err := New(a.client, a.instance, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(a.client, a.instance, "https://example.test/acme/backlog"); err == nil {
		t.Error("accepted an invalid default repository")
	}
	if _, err := withoutDefault.Find(t.Context(), provider.TaskQuery{}); err == nil {
		t.Error("found issues without an explicit repository or default")
	}
	if _, err := withoutDefault.Create(t.Context(), provider.TaskDraft{Title: "Missing scope"}, provider.WriteOptions{}); err == nil {
		t.Error("created an issue without an explicit repository or default")
	}
	ref := provider.Ref{Instance: a.instance, Scope: "acme/backlog", ID: "42"}
	if _, err := a.Transition(t.Context(), ref, provider.TaskState{ID: "done"}, provider.WriteOptions{}); !errors.Is(err, provider.ErrUnsupported) {
		t.Errorf("unsupported transition error = %v", err)
	}
	options := provider.WriteOptions{ExpectedRevision: "123"}
	_, createErr := a.Create(t.Context(), provider.TaskDraft{Title: "A task"}, options)
	_, transitionErr := a.Transition(t.Context(), ref, provider.TaskState{ID: "closed"}, options)
	_, commentErr := a.Comment(t.Context(), ref, "A comment", options)
	_, assignErr := a.AssignToMe(t.Context(), ref, options)
	for _, err := range []error{createErr, transitionErr, commentErr, assignErr} {
		if !errors.Is(err, provider.ErrUnsupported) {
			t.Errorf("expected revision precondition error, got %v", err)
		}
	}
	requests()
}

func TestFindFiltersAcrossPages(t *testing.T) {
	a, requests := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			writeJSON(t, w, account{"Alex"})
			return
		}
		q := r.URL.Query()
		if r.URL.Path != backlogPath || q.Get("state") != "open" || q.Get("assignee") != "Alex" || q.Get("per_page") != "100" {
			t.Errorf("unexpected list request %v", r.URL)
			http.NotFound(w, r)
			return
		}
		switch q.Get("page") {
		case "1":
			items := make([]issue, 100)
			for n := range items {
				items[n] = issue{Number: int64(n + 1), Title: "match", State: "open", Assignee: account{"Alex"}, PullRequest: json.RawMessage(`{}`)}
			}
			items[0] = issue{Number: 1, Title: "MATCH", State: "open", Assignees: []account{{"someone"}, {"alex"}}}
			items[1] = issue{Number: 2, Title: "match", State: "closed", Assignee: account{"Alex"}}
			items[2] = issue{Number: 3, Title: "match", State: "open", Assignee: account{"someone"}}
			items[3] = issue{Number: 4, Title: "irrelevant", State: "open", Assignee: account{"Alex"}}
			writeJSON(t, w, items)
		case "2":
			writeJSON(t, w, []issue{
				{Number: 101, Body: "Another Match", State: "open", Assignee: account{"Alex"}},
				{Number: 102, Title: "match", State: "open", Assignee: account{"Alex"}},
			})
		default:
			t.Errorf("unexpected list page: %s", q.Get("page"))
			http.NotFound(w, r)
		}
	})
	tasks, err := a.Find(t.Context(), provider.TaskQuery{AssignedToMe: true, OpenOnly: true, Text: "match", Limit: 2})
	if err != nil || len(tasks) != 2 || tasks[0].Ref.ID != "1" || tasks[1].Ref.ID != "101" || tasks[0].State != (provider.TaskState{ID: "open", Name: "Open"}) {
		t.Fatalf("find = %+v, %v", tasks, err)
	}
	requests("GET /user", "GET "+backlogPath, "GET "+backlogPath)
}

func TestFindLimitsInExplicitRepository(t *testing.T) {
	const path = "/repos/other/project/issues"
	a, requests := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, err := strconv.Atoi(q.Get("page"))
		if r.URL.Path != path || q.Get("state") != "all" || err != nil || page < 1 || page > 2 {
			t.Errorf("unexpected list request %v", r.URL)
			http.NotFound(w, r)
			return
		}
		items := make([]issue, 100)
		for n := range items {
			items[n] = issue{Number: int64((page-1)*100 + n + 1), State: "closed"}
		}
		writeJSON(t, w, items)
	})
	for _, tc := range []struct{ limit, want int }{{0, 30}, {101, 101}} {
		tasks, err := a.Find(t.Context(), provider.TaskQuery{Scope: "Other/Project", Limit: tc.limit})
		if err != nil || len(tasks) != tc.want || tasks[tc.want-1].Key != fmt.Sprintf("other/project#%d", tc.want) {
			t.Fatalf("find with limit %d returned %d tasks, %v", tc.limit, len(tasks), err)
		}
	}
	requests("GET "+path, "GET "+path, "GET "+path)
}
