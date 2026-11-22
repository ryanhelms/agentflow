package githubdevops_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/nscaledev/devflow/internal/gh"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/githubdevops"
)

var repo = provider.RepositoryRef{Host: "github.example.test", Path: "octo/widgets"}

const base = "/repos/octo/widgets"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testAdapter(t *testing.T, handler http.HandlerFunc) *githubdevops.Adapter {
	t.Helper()
	// gh.New uses the default transport. Keep these tests serial and route its
	// requests to httptest without requiring a TCP listener.
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			defer func() { _ = r.Body.Close() }()
		}
		if r.URL.Host != repo.Host {
			t.Errorf("API host = %q, want %q", r.URL.Host, repo.Host)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("API version = %q", got)
		}
		w := httptest.NewRecorder()
		handler(w, r)
		return w.Result(), nil
	})
	client := gh.New("test-token")
	client.API = "https://" + repo.Host
	return githubdevops.New(client)
}

func respond(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func unexpected(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	t.Errorf("unexpected request %s %s", r.Method, r.URL)
	w.WriteHeader(http.StatusBadRequest)
}

func TestChecks(t *testing.T) {
	adapter := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.EscapedPath() != base+"/commits/feature%2Fwidgets/check-runs" ||
			query.Get("filter") != "latest" || query.Get("per_page") != "100" {
			unexpected(t, w, r)
			return
		}
		switch query.Get("page") {
		case "1":
			respond(t, w, map[string]any{"total_count": 3, "check_runs": []map[string]any{
				{"name": "build", "status": "completed", "conclusion": "success", "html_url": "https://github.example.test/build", "details_url": "https://ci.example.test/build"},
				{"name": "integration", "status": "in_progress", "conclusion": nil, "details_url": "https://ci.example.test/integration"},
			}})
		case "2":
			respond(t, w, map[string]any{"total_count": 3, "check_runs": []map[string]any{
				{"name": "lint", "status": "completed", "conclusion": "timed_out"},
			}})
		default:
			unexpected(t, w, r)
		}
	})
	want := []provider.CheckRun{
		{Name: "build", State: "completed", Conclusion: "success", URL: "https://github.example.test/build"},
		{Name: "integration", State: "in_progress", URL: "https://ci.example.test/integration"},
		{Name: "lint", State: "completed", Conclusion: "timed_out"},
	}
	got, err := adapter.Checks(t.Context(), repo, "feature/widgets")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("checks = %+v, %v, want %+v", got, err, want)
	}
}

func TestDeployments(t *testing.T) {
	adapter := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			unexpected(t, w, r)
			return
		}
		query := r.URL.Query()
		switch r.URL.Path {
		case base + "/deployments":
			if query.Get("sha") != "abc123" || query.Get("per_page") != "100" {
				t.Errorf("deployments query = %v", query)
			}
			switch query.Get("page") {
			case "1":
				page := make([]map[string]any, 100)
				page[0] = map[string]any{"id": 105, "sha": "abc123", "ref": "main", "environment": "production"}
				for i := 1; i < 100; i++ {
					page[i] = map[string]any{"id": 104 - i, "sha": "older", "ref": "main", "environment": "production"}
				}
				respond(t, w, page)
			case "2":
				respond(t, w, []map[string]any{
					{"id": 2, "sha": "abc123", "environment": "production", "url": "https://api.example.test/deployments/2"},
					{"id": 1, "sha": "abc123", "environment": "staging", "url": "https://api.example.test/deployments/1"},
				})
			default:
				unexpected(t, w, r)
			}
		case base + "/deployments/105/statuses", base + "/deployments/2/statuses", base + "/deployments/1/statuses":
			if query.Get("per_page") != "1" {
				t.Errorf("deployment status query = %v", query)
			}
			switch r.URL.Path {
			case base + "/deployments/105/statuses":
				respond(t, w, []map[string]any{
					{"state": "inactive", "log_url": "https://ci.example.test/deploy/105", "environment_url": "https://prod.example.test"},
					{"state": "success"},
				})
			case base + "/deployments/2/statuses":
				respond(t, w, []map[string]any{{"state": "failure", "target_url": "https://ci.example.test/deploy/2"}})
			default:
				respond(t, w, []any{})
			}
		default:
			unexpected(t, w, r)
		}
	})
	want := []provider.Deployment{
		{ID: "105", Ref: "main", Environment: "production", Revision: "abc123", State: "inactive", URL: "https://ci.example.test/deploy/105"},
		{ID: "2", Environment: "production", Revision: "abc123", State: "failure", URL: "https://ci.example.test/deploy/2"},
		{ID: "1", Environment: "staging", Revision: "abc123", URL: "https://api.example.test/deployments/1"},
	}
	got, err := adapter.Deployments(t.Context(), repo, "abc123")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("deployments = %+v, %v, want %+v", got, err, want)
	}
}

func TestGitHubReadErrors(t *testing.T) {
	adapter := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case base + "/commits/missing/check-runs", base + "/releases/tags/missing":
			w.WriteHeader(http.StatusNotFound)
		case base + "/deployments":
			if r.URL.Query().Has("sha") {
				t.Errorf("unfiltered deployments requested sha: %s", r.URL)
			}
			respond(t, w, []map[string]any{{"id": 1, "sha": "abc123", "environment": "staging"}})
		case base + "/deployments/1/statuses":
			http.Error(w, "upstream failure", http.StatusServiceUnavailable)
		default:
			unexpected(t, w, r)
		}
	})
	if _, err := adapter.Checks(t.Context(), repo, "missing"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("missing check revision error = %v", err)
	}
	if _, err := adapter.GetRelease(t.Context(), repo, "missing"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("missing release error = %v", err)
	}
	if got, err := adapter.Deployments(t.Context(), repo, ""); got != nil || err == nil || errors.Is(err, provider.ErrNotFound) {
		t.Errorf("failed status = %v, %v", got, err)
	}
}

func TestPublishRejectsPrecondition(t *testing.T) {
	adapter := testAdapter(t, func(w http.ResponseWriter, r *http.Request) { unexpected(t, w, r) })
	got, err := adapter.PublishRelease(t.Context(), repo, provider.Release{Tag: "v1.2.3"}, provider.WriteOptions{ExpectedRevision: "abc123"})
	if got != (provider.WriteResult{}) || !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("precondition = %v, %v", got, err)
	}
}

const (
	tag         = "cli/v1.2.3"
	commit      = "abcdef0123456789abcdef0123456789abcdef01"
	otherCommit = "9876543210abcdef9876543210abcdef98765432"
	releaseURL  = "https://github.example.test/octo/widgets/releases/tag/cli/v1.2.3"
)

var draft = provider.Release{Tag: tag, Revision: "heads/release", Title: "CLI 1.2.3", Notes: "Release notes\n"}

func releaseResponse(title, notes string) map[string]any {
	return map[string]any{
		"tag_name": tag, "name": title, "body": notes, "html_url": releaseURL,
		"target_commitish": "main", "assets": []map[string]any{{"name": "cli.tar.gz", "browser_download_url": releaseURL + "/cli.tar.gz"}},
	}
}

func requireReleaseResult(t *testing.T, got provider.WriteResult, err error, status provider.WriteStatus) {
	t.Helper()
	want := provider.WriteResult{
		Ref: provider.Ref{Instance: "github", Scope: repo.Path, ID: tag}, Status: status, Revision: commit, URL: releaseURL,
	}
	if err != nil || got != want {
		t.Fatalf("publish = %+v, %v, want %+v", got, err, want)
	}
}

type publishCase struct {
	name          string
	writeCode     int
	initialSHA    string
	createdSHA    string
	alterMetadata string
	wantStatus    provider.WriteStatus
	wantConflict  bool
	wantPosts     int
}

func TestPublishRelease(t *testing.T) {
	for _, tt := range []publishCase{
		{name: "create and replay", writeCode: http.StatusCreated, initialSHA: commit, createdSHA: commit, wantStatus: provider.Applied, wantPosts: 1},
		{name: "reconcile validation race", writeCode: http.StatusUnprocessableEntity, createdSHA: commit, wantStatus: provider.AlreadyApplied, wantPosts: 1},
		{name: "reconcile upstream error", writeCode: http.StatusBadGateway, createdSHA: commit, wantStatus: provider.AlreadyApplied, wantPosts: 1},
		{name: "reconcile empty success response", writeCode: http.StatusNoContent, createdSHA: commit, wantStatus: provider.AlreadyApplied, wantPosts: 1},
		{name: "error without release", writeCode: http.StatusBadGateway, wantPosts: 1},
		{name: "existing tag points elsewhere", initialSHA: otherCommit, wantConflict: true},
		{name: "tag created at a different commit during write", writeCode: http.StatusCreated, createdSHA: otherCommit, wantConflict: true, wantPosts: 1},
		{name: "reconcile release at a different commit", writeCode: http.StatusUnprocessableEntity, createdSHA: otherCommit, wantConflict: true, wantPosts: 1},
		{name: "reconcile release with different notes", writeCode: http.StatusBadGateway, createdSHA: commit, alterMetadata: "body", wantConflict: true, wantPosts: 1},
		{name: "reconcile prerelease", writeCode: http.StatusBadGateway, createdSHA: commit, alterMetadata: "prerelease", wantConflict: true, wantPosts: 1},
	} {
		t.Run(tt.name, func(t *testing.T) { testPublishRelease(t, tt) })
	}
}

func testPublishRelease(t *testing.T, tc publishCase) {
	t.Helper()
	posts, sha := 0, tc.initialSHA
	adapter := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET " + base + "/commits/heads%2Frelease":
			respond(t, w, map[string]string{"sha": commit})
		case "GET " + base + "/commits/tags%2Fcli%2Fv1.2.3":
			if sha == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			respond(t, w, map[string]string{"sha": sha})
		case "GET " + base + "/releases/tags/cli%2Fv1.2.3":
			if posts == 0 || tc.createdSHA == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			response := releaseResponse(draft.Title, draft.Notes)
			switch tc.alterMetadata {
			case "body":
				response["body"] = "notes from a different publish"
			case "prerelease":
				response["prerelease"] = true
			}
			respond(t, w, response)
		case "POST " + base + "/releases":
			posts++
			if posts > tc.wantPosts {
				t.Errorf("unexpected release creation attempt %d", posts)
			}
			var got map[string]any
			want := map[string]any{"tag_name": tag, "target_commitish": commit, "name": draft.Title, "body": draft.Notes, "draft": false, "prerelease": false}
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("create body = %v, %v, want %v", got, err, want)
			}
			sha = tc.createdSHA
			w.WriteHeader(tc.writeCode)
			if tc.writeCode == http.StatusCreated {
				respond(t, w, releaseResponse(draft.Title, draft.Notes))
			}
		default:
			unexpected(t, w, r)
		}
	})
	opts := provider.WriteOptions{OperationID: "ship-cli"}
	got, err := adapter.PublishRelease(t.Context(), repo, draft, opts)
	if posts != tc.wantPosts {
		t.Errorf("release POST count = %d, want %d", posts, tc.wantPosts)
	}
	switch {
	case tc.wantConflict:
		if got != (provider.WriteResult{}) || !errors.Is(err, provider.ErrConflict) {
			t.Fatalf("publish conflict = %+v, %v", got, err)
		}
	case tc.wantStatus == "":
		if got != (provider.WriteResult{}) || err == nil || errors.Is(err, provider.ErrNotFound) {
			t.Fatalf("missing after write = %+v, %v", got, err)
		}
	default:
		requireReleaseResult(t, got, err, tc.wantStatus)
		got, err := adapter.PublishRelease(t.Context(), repo, draft, opts)
		requireReleaseResult(t, got, err, provider.AlreadyApplied)
		observed, err := adapter.GetRelease(t.Context(), repo, tag)
		want := provider.Release{Tag: tag, Revision: commit, Title: draft.Title, Notes: draft.Notes, URL: releaseURL}
		if err != nil || observed != want {
			t.Fatalf("get release = %+v, %v, want %+v", observed, err, want)
		}
		changed := draft
		changed.Title += " revised"
		if _, err := adapter.PublishRelease(t.Context(), repo, changed, opts); !errors.Is(err, provider.ErrConflict) {
			t.Errorf("publish with different title error = %v", err)
		}
	}
}

func TestPublishReleaseWithNoTarget(t *testing.T) {
	adapter := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET " + base + "/releases/tags/cli%2Fv1.2.3":
			w.WriteHeader(http.StatusNotFound)
		case "POST " + base + "/releases":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode release: %v", err)
			}
			if _, ok := request["target_commitish"]; ok {
				t.Errorf("unexpected target: %v", request)
			}
			w.WriteHeader(http.StatusCreated)
			respond(t, w, releaseResponse("", ""))
		case "GET " + base + "/commits/tags%2Fcli%2Fv1.2.3":
			respond(t, w, map[string]string{"sha": commit})
		default:
			unexpected(t, w, r)
		}
	})
	got, err := adapter.PublishRelease(t.Context(), repo, provider.Release{Tag: tag}, provider.WriteOptions{})
	requireReleaseResult(t, got, err, provider.Applied)
}
