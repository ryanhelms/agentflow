package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/gh"
	"github.com/nscaledev/agentflow/internal/gitx"
	"github.com/nscaledev/agentflow/internal/policy"
	"github.com/nscaledev/agentflow/internal/provider"
)

func devOpsApp(t *testing.T, settings string) *App {
	t.Helper()
	dir := newRepo(t)
	r := &gitx.Repo{Dir: dir}
	mustGit(t, r, "switch", "main")
	configDir := filepath.Join(dir, ".agentflow")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yml"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := Open(t.Context(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	a.ghc = gh.New("fixture-token")
	return a
}

func mockDevOpsAPI(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = docsPRTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Errorf("unexpected API identity for %s", r.URL)
		}
		handler.ServeHTTP(w, r)
	})}
}

func TestShipStatusCorrelatesGitHubDeployments(t *testing.T) {
	a := devOpsApp(t, `devops:
  checks: {provider: github, settings: {repository: example/checks}}
  deployments: {provider: github, settings: {repository: example/deployments}}
  releases: {provider: none}
`)
	mustGit(t, a.Repo, "remote", "add", "origin", "https://github.com/example/source.git")
	const head = "5d2aa0f9c6d247a84148d0cbaa1e544492060ade"
	mockDevOpsAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/example/source/pulls":
			fmt.Fprintf(w, `[{"number":42,"head":{"sha":%q}}]`, head)
		case "GET /repos/example/checks/commits/" + head + "/check-runs":
			fmt.Fprint(w, `{"total_count":1,"check_runs":[{"name":"lint","conclusion":"success"}]}`)
		case "POST /graphql":
			fmt.Fprint(w, `{"data":{}}`)
		case "GET /repos/example/deployments/deployments":
			if r.URL.Query().Get("sha") != head {
				t.Errorf("deployment lookup must use the PR head: %s", r.URL)
			}
			fmt.Fprintf(w, `[
  {"id":5,"sha":"unrelated","environment":"production"},
  {"id":4,"sha":%q,"environment":"production","ref":"main","created_at":"2026-11-21T00:00:00Z"},
  {"id":3,"sha":%q,"environment":"production"}
]`, head, head)
		case "GET /repos/example/deployments/deployments/4/statuses":
			fmt.Fprint(w, `[{"state":"success"}]`)
		case "GET /repos/example/deployments/deployments/3/statuses":
			fmt.Fprint(w, `[{"state":"failure"}]`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	out, err := a.ShipStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if checks, ok := out["checks"].(map[string]int); !ok || checks["success"] != 1 {
		t.Fatalf("missing independently selected checks: %+v", out)
	}
	data, err := json.Marshal(out["deployments"])
	const want = `{"production":{"environment":"production","ref":"main","sha":"` + head + `","created_at":"2026-11-21T00:00:00Z","state":"success","id":4}}`
	if err != nil || string(data) != want {
		t.Fatalf("legacy deployment JSON = %s, err = %v", data, err)
	}
	a.Cfg.Providers.DevOps.Deployments.Provider = "none"
	if disabled, err := a.ShipStatus(t.Context()); err != nil || disabled["deployments"] != nil || disabled["checks"] == nil {
		t.Fatalf("disabled deployments with checks enabled = %+v, %v", disabled, err)
	}
}

func TestMixedDevOpsProvidersSharePRHead(t *testing.T) {
	a := devOpsApp(t, `devops:
  checks: {provider: github, settings: {repository: example/checks}}
  deployments: {provider: fixture, settings: {path: .agentflow/deployments.json}}
  releases: {provider: github, settings: {repository: example/releases}}
`)
	mustGit(t, a.Repo, "remote", "add", "origin", "https://github.com/example/source.git")
	const head = "5d2aa0f9c6d247a84148d0cbaa1e544492060ade"
	deployments := fmt.Sprintf(`[
  {"environment":"staging","revision":"unrelated","state":"failure"},
  {"environment":"staging","revision":%q,"state":"success"}
]`, head)
	if err := os.WriteFile(filepath.Join(a.Repo.Dir, ".agentflow", "deployments.json"), []byte(deployments), 0o644); err != nil {
		t.Fatal(err)
	}
	mockDevOpsAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/example/source/pulls":
			fmt.Fprintf(w, `[{"number":42,"head":{"sha":%q}}]`, head)
		case "GET /repos/example/checks/commits/" + head + "/check-runs":
			fmt.Fprint(w, `{"total_count":1,"check_runs":[{"name":"lint","conclusion":"success"}]}`)
		case "POST /graphql":
			fmt.Fprint(w, `{"data":{}}`)
		case "GET /repos/example/releases/releases/tags/v1.2.3":
			fmt.Fprint(w, `{"tag_name":"v1.2.3"}`)
		case "GET /repos/example/releases/git/ref/tags/v1.2.3":
			fmt.Fprintf(w, `{"object":{"type":"commit","sha":%q}}`, head)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	out, err := a.ShipStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if checks, ok := out["checks"].(map[string]int); !ok || checks["success"] != 1 {
		t.Fatalf("checks at PR head = %+v", out)
	}
	records, _ := out["deployments"].(map[string]provider.Deployment)
	deployed := records["staging"]
	if deployed.Revision != head || deployed.State != "success" {
		t.Fatalf("fixture deployment at PR head = %+v", out)
	}
	if release, err := a.ReleaseGet(t.Context(), "v1.2.3"); err != nil || release.Revision != deployed.Revision {
		t.Fatalf("release for deployed revision %s = %+v, %v", deployed.Revision, release, err)
	}
}

func TestReleasePublicationApprovalAndRetry(t *testing.T) {
	a := devOpsApp(t, `code_hosting: {provider: none}
devops:
  checks: {provider: none}
  deployments: {provider: fixture, settings: {path: missing.json}}
  releases: {provider: github, settings: {repository: example/releases}}
`)
	a.Gate.Autonomy.Remote = config.Auto
	const sha = "abcdef0123456789abcdef0123456789abcdef01"
	const wrongSHA = "1111111111111111111111111111111111111111"
	const response = `{"tag_name":"v1.2.3","name":"v1.2.3","body":"notes","html_url":"https://example.test/release","assets":[
  {"name":"cli.tar.gz","browser_download_url":"https://example.test/cli.tar.gz","size":4156,"digest":"sha256:abcd"},
  {"name":"checksums.txt","browser_download_url":"https://example.test/checksums.txt","size":84,"digest":null}
]}`
	var posts, requests int
	mockDevOpsAPI(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/example/releases/commits/" + sha, "GET /repos/example/releases/commits/" + wrongSHA:
			fmt.Fprintf(w, `{"sha":%q}`, sha)
		case "GET /repos/example/releases/git/ref/tags/v1.2.3":
			if posts == 0 {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"object":{"type":"commit","sha":%q}}`, sha)
		case "GET /repos/example/releases/releases/tags/v1.2.3":
			if posts == 0 {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, response)
		case "POST /repos/example/releases/releases":
			posts++
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["target_commitish"] != sha || payload["draft"] != false || payload["prerelease"] != false {
				t.Errorf("unexpected release payload %+v, %v", payload, err)
			}
			// Simulate an ambiguous failure after GitHub created the release.
			w.WriteHeader(http.StatusBadGateway)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})

	in := ReleasePublishInput{Tag: "v1.2.3", Revision: sha, Notes: "notes", DryRun: true}
	if out, err := a.ReleasePublish(t.Context(), in, ""); err != nil || out == nil || requests != 0 {
		t.Fatalf("dry run = %+v, %v, %d remote calls", out, err, requests)
	}
	in.DryRun = false
	approval := func(input ReleasePublishInput) string {
		t.Helper()
		out, err := a.ReleasePublish(t.Context(), input, "")
		pending, ok := out.(*Pending)
		if err != nil || !ok || pending.Status != "needs_confirmation" || pending.ConfirmToken == "" {
			t.Fatalf("approval = %+v, %v", out, err)
		}
		return pending.ConfirmToken
	}
	token := approval(in)
	changed := in
	changed.Notes = "different"
	if _, err := a.ReleasePublish(t.Context(), changed, token); !errors.Is(err, policy.ErrToken) || requests != 0 {
		t.Fatalf("modified request reached provider or changed token result: %v (%d calls)", err, requests)
	}
	if _, err := a.ReleasePublish(t.Context(), ReleasePublishInput{Tag: in.Tag, Revision: "main"}, ""); err == nil || requests != 0 {
		t.Fatalf("mutable revision accepted: %v (%d calls)", err, requests)
	}
	publish := func(tok string) {
		t.Helper()
		out, err := a.ReleasePublish(t.Context(), in, tok)
		result, ok := out.(provider.WriteResult)
		if err != nil || !ok || result.Status != provider.AlreadyApplied || result.Revision != sha || posts != 1 {
			t.Fatalf("release = %+v, %v, %d writes", out, err, posts)
		}
	}
	publish(token)
	wantAssets := []provider.ReleaseAsset{
		{Name: "cli.tar.gz", DownloadURL: "https://example.test/cli.tar.gz", Size: 4156, Digest: "sha256:abcd"},
		{Name: "checksums.txt", DownloadURL: "https://example.test/checksums.txt", Size: 84},
	}
	checkLookup := func() {
		t.Helper()
		got, err := a.ReleaseGet(t.Context(), in.Tag)
		if err != nil || got.Revision != sha || got.Notes != "notes" || !slices.Equal(got.Assets, wantAssets) {
			t.Fatalf("read after publish = %+v, %v", got, err)
		}
	}
	checkLookup()
	// A retry opens a new gate, as a second CLI invocation would.
	gate, err := policy.NewGate(a.Home, a.Gate.Autonomy, "test")
	if err != nil {
		t.Fatal(err)
	}
	a.Gate = gate
	publish(approval(in))
	checkLookup()
	if _, err := a.ReleasePublish(t.Context(), changed, approval(changed)); !errors.Is(err, provider.ErrConflict) || posts != 1 {
		t.Fatalf("conflicting replay = %v, %d writes", err, posts)
	}
	changed.Revision = wrongSHA
	if _, err := a.ReleasePublish(t.Context(), changed, approval(changed)); !errors.Is(err, provider.ErrConflict) || posts != 1 {
		t.Fatalf("provider substituted the pinned revision: %v, %d writes", err, posts)
	}
	entry, err := os.ReadFile(filepath.Join(a.Home, "audit.log"))
	if err != nil || strings.Count(string(entry), `"action":"release_publish"`) != 4 {
		t.Fatalf("unexpected audit records %s, %v", entry, err)
	}
}
