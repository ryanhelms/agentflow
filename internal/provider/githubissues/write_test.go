package githubissues

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/provider"
)

func TestCreateReconcilesAndPreservesBody(t *testing.T) {
	userBody := markerPrefix + strings.Repeat("a", 64) + markerSuffix + markerGap + "\r\n  user text\r\n```go\n"
	for _, tc := range []struct {
		name        string
		description string
		postReply   func(http.ResponseWriter)
		want        provider.WriteStatus
	}{
		{"success", userBody, nil, provider.Applied},
		{"title only", "", nil, provider.Applied},
		{"server error", userBody, func(w http.ResponseWriter) { http.Error(w, "upstream failed", http.StatusBadGateway) }, provider.AlreadyApplied},
		{"invalid success body", userBody, func(w http.ResponseWriter) { http.Error(w, "truncated JSON", http.StatusCreated) }, provider.AlreadyApplied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operationID := "operation --> \n with uncommon characters"
			description := tc.description
			var saved issue
			a, requests := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET " + backlogPath:
					if r.URL.Query().Get("state") != "all" {
						t.Errorf("create lookup should include closed issues: %s", r.URL)
					}
					items := []issue{}
					if saved.Number != 0 {
						pull := saved
						pull.Number = 26
						pull.PullRequest = json.RawMessage(`{}`)
						items = append(items, pull, saved)
					}
					writeJSON(t, w, items)
				case "POST " + backlogPath:
					var in struct {
						Title  string   `json:"title"`
						Body   string   `json:"body"`
						Labels []string `json:"labels"`
					}
					if !readJSON(t, w, r, &in) {
						return
					}
					if in.Title != "Create test" || !slices.Equal(in.Labels, []string{"bug", "docs"}) || !strings.HasPrefix(in.Body, markerPrefix) || !strings.HasSuffix(in.Body, description) || strings.Contains(in.Body, operationID) || in.Body == description {
						t.Errorf("unexpected create body: %+v", in)
					}
					saved = issue{Number: 27, Title: in.Title, Body: in.Body, State: "open", HTMLURL: "https://example.test/issues/27"}
					if tc.postReply != nil {
						tc.postReply(w)
						return
					}
					w.WriteHeader(http.StatusCreated)
					writeJSON(t, w, saved)
				case "GET " + backlogPath + "/27":
					writeJSON(t, w, saved)
				default:
					http.NotFound(w, r)
				}
			})
			draft := provider.TaskDraft{Title: "Create test", Description: description, Labels: []string{"bug", "docs"}}
			options := provider.WriteOptions{OperationID: operationID}
			created, err := a.Create(t.Context(), draft, options)
			if err != nil || created.Status != tc.want || created.Ref != (provider.Ref{Instance: a.instance, Scope: "acme/backlog", ID: "27"}) || created.URL != "https://example.test/issues/27" {
				t.Fatalf("create = %+v, %v", created, err)
			}
			// Explicit repository selection uses the same operation namespace as the default.
			draft.Scope = "ACME/Backlog"
			replay, err := a.Create(t.Context(), draft, options)
			if err != nil || replay.Status != provider.AlreadyApplied || replay.Ref != created.Ref {
				t.Fatalf("replay = %+v, %v", replay, err)
			}
			got, err := a.Get(t.Context(), created.Ref)
			if err != nil || got.Description != description {
				t.Fatalf("get description = %q, %v", got.Description, err)
			}
			wantRequests := []string{"GET " + backlogPath, "POST " + backlogPath}
			if tc.postReply != nil {
				wantRequests = append(wantRequests, "GET "+backlogPath)
			}
			wantRequests = append(wantRequests, "GET "+backlogPath, "GET "+backlogPath+"/27")
			requests(wantRequests...)
		})
	}
}

func TestCreateFailsWithoutRetryingPost(t *testing.T) {
	for _, tc := range []struct {
		name       string
		listStatus int
		postStatus int
		requests   []string
		want       string
	}{
		{"preflight unavailable", http.StatusBadGateway, 0, []string{"GET " + backlogPath}, "502"},
		{"preflight empty response", http.StatusNoContent, 0, []string{"GET " + backlogPath}, "no items array"},
		{"definite rejection", http.StatusOK, http.StatusUnprocessableEntity, []string{"GET " + backlogPath, "POST " + backlogPath}, "422"},
		{"unresolved write", http.StatusOK, http.StatusBadGateway, []string{"GET " + backlogPath, "POST " + backlogPath, "GET " + backlogPath}, "outcome unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, requests := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && tc.postStatus != 0 {
					http.Error(w, "create failed", tc.postStatus)
					return
				}
				w.WriteHeader(tc.listStatus)
				if tc.listStatus == http.StatusOK {
					writeJSON(t, w, []issue{})
				}
			})
			_, err := a.Create(t.Context(), provider.TaskDraft{Title: "Test"}, provider.WriteOptions{OperationID: "stable"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("create error = %v, want %q", err, tc.want)
			}
			requests(tc.requests...)
		})
	}
}

func TestCreateWithoutOperationID(t *testing.T) {
	const path = "/repos/another/bugs/issues"
	for _, body := range []string{"  ordinary body\n\n", bodyEscape + "user content", markerPrefix + strings.Repeat("a", 64) + markerSuffix + markerGap + "user content"} {
		var saved issue
		a, requests := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "POST " + path:
				var in map[string]string
				if !readJSON(t, w, r, &in) {
					return
				}
				if !strings.HasSuffix(in["body"], body) || (!strings.HasPrefix(body, "<!--") && in["body"] != body) {
					t.Errorf("unexpected user body on wire: %q", in["body"])
				}
				saved = issue{Number: 42, State: "open", Body: in["body"]}
				writeJSON(t, w, saved)
			case "GET " + path + "/42":
				writeJSON(t, w, saved)
			default:
				http.NotFound(w, r)
			}
		})
		created, err := a.Create(t.Context(), provider.TaskDraft{Scope: "Another/Bugs", Title: "Test", Description: body}, provider.WriteOptions{})
		if err != nil || created.Status != provider.Applied {
			t.Fatalf("create without marker = %+v, %v", created, err)
		}
		got, err := a.Get(t.Context(), created.Ref)
		if err != nil || got.Description != body {
			t.Fatalf("user body = %q, %v, want %q", got.Description, err, body)
		}
		requests("POST "+path, "GET "+path+"/42")
	}
}

func TestTransitionReadsBeforeWriting(t *testing.T) {
	const path = backlogPath + "/42"
	current := issue{Number: 42, State: "open"}
	a, requests := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPatch {
			var in map[string]string
			if !readJSON(t, w, r, &in) {
				return
			}
			current.State = in["state"]
		}
		writeJSON(t, w, current)
	})
	ref := provider.Ref{Instance: a.instance, Scope: "acme/backlog", ID: "42"}
	for _, tc := range []struct {
		target provider.TaskState
		want   provider.WriteStatus
	}{
		{provider.TaskState{ID: "closed"}, provider.Applied},
		{provider.TaskState{Name: "Closed", Terminal: true}, provider.AlreadyApplied},
		{provider.TaskState{ID: "open"}, provider.Applied},
		{provider.TaskState{ID: "open"}, provider.AlreadyApplied},
	} {
		result, err := a.Transition(t.Context(), ref, tc.target, provider.WriteOptions{})
		if err != nil || result.Ref != ref || result.Status != tc.want {
			t.Fatalf("transition to %+v = %+v, %v", tc.target, result, err)
		}
	}
	requests("GET "+path, "PATCH "+path, "GET "+path, "GET "+path, "PATCH "+path, "GET "+path)
}

func TestWritesRejectPullRequests(t *testing.T) {
	const path = backlogPath + "/42"
	a, requests := testAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, issue{Number: 42, State: "open", PullRequest: json.RawMessage(`{}`)})
	})
	ref := provider.Ref{Instance: a.instance, Scope: "acme/backlog", ID: "42"}
	_, transitionErr := a.Transition(t.Context(), ref, provider.TaskState{ID: "closed"}, provider.WriteOptions{})
	_, commentErr := a.Comment(t.Context(), ref, "hello", provider.WriteOptions{})
	_, assignErr := a.AssignToMe(t.Context(), ref, provider.WriteOptions{})
	for _, err := range []error{transitionErr, commentErr, assignErr} {
		if !errors.Is(err, provider.ErrNotFound) {
			t.Errorf("write to a PR error = %v", err)
		}
	}
	requests("GET "+path, "GET "+path, "GET "+path)
}

func TestCommentReconcilesAmbiguousWrite(t *testing.T) {
	const path = backlogPath + "/42"
	const commentPath = path + "/comments"
	var saved []comment
	a, requests := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET " + path:
			writeJSON(t, w, issue{Number: 42, State: "open"})
		case "GET " + commentPath:
			writeJSON(t, w, append([]comment{}, saved...))
		case "POST " + commentPath:
			var in map[string]string
			if !readJSON(t, w, r, &in) {
				return
			}
			if plainBody(in["body"]) != "  hello\n" || !strings.HasPrefix(in["body"], markerPrefix) {
				t.Errorf("unexpected comment body: %q", in["body"])
			}
			saved = append(saved, comment{ID: 7, Body: in["body"], HTMLURL: "https://example.test/issues/42#issuecomment-7"})
			http.Error(w, "outcome unknown", http.StatusGatewayTimeout)
		default:
			http.NotFound(w, r)
		}
	})
	ref := provider.Ref{Instance: a.instance, Scope: "acme/backlog", ID: "42"}
	for range 2 {
		result, err := a.Comment(t.Context(), ref, "  hello\n", provider.WriteOptions{OperationID: "comment-1"})
		if err != nil || result.Ref != ref || result.Status != provider.AlreadyApplied || result.URL != "https://example.test/issues/42#issuecomment-7" {
			t.Fatalf("comment = %+v, %v", result, err)
		}
	}
	requests("GET "+path, "GET "+commentPath, "POST "+commentPath, "GET "+commentPath, "GET "+path, "GET "+commentPath)
}

func TestAssignToMePreservesOtherAssignees(t *testing.T) {
	const path = backlogPath + "/42"
	current := issue{Number: 42, State: "open", Assignee: account{"Sam"}, Assignees: []account{{"Sam"}}}
	a, requests := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /user":
			writeJSON(t, w, account{"Alex"})
		case "GET " + path:
			writeJSON(t, w, current)
		case "POST " + path + "/assignees":
			var in map[string][]string
			if !readJSON(t, w, r, &in) {
				return
			}
			if !slices.Equal(in["assignees"], []string{"Alex"}) {
				t.Errorf("assignees request = %v", in)
			}
			current.Assignees = append(current.Assignees, account{"alex"})
			writeJSON(t, w, current)
		default:
			http.NotFound(w, r)
		}
	})
	ref := provider.Ref{Instance: a.instance, Scope: "acme/backlog", ID: "42"}
	for _, want := range []provider.WriteStatus{provider.Applied, provider.AlreadyApplied} {
		result, err := a.AssignToMe(t.Context(), ref, provider.WriteOptions{})
		if err != nil || result.Ref != ref || result.Status != want {
			t.Fatalf("assign to me = %+v, %v", result, err)
		}
	}
	requests("GET "+path, "GET /user", "POST "+path+"/assignees", "GET "+path, "GET /user")
}
