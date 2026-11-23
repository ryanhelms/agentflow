package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gh"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
)

func docsGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

const fixtureOrigin = "git@github.com:docs-team/handbook"

// A local SSH stand-in serves a bare repository while preserving the hosted
// origin inspected by both the approval gate and the GitHub publisher.
func repositoryFixture(t *testing.T, role string) (*App, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := newRepo(t)
	bare := filepath.Join(t.TempDir(), "knowledge.git")
	docsGit(t, dir, "clone", "--quiet", "--bare", "--", dir, bare)
	docsGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	ssh := filepath.Join(t.TempDir(), "fixture-ssh")
	const script = `#!/bin/sh
set -eu
[ "$1" = git@github.com ]
case "$2" in
  "git-upload-pack 'docs-team/handbook'") exec git-upload-pack "$DEVFLOW_FIXTURE_BARE" ;;
  "git-receive-pack 'docs-team/handbook'") exec git-receive-pack "$DEVFLOW_FIXTURE_BARE" ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVFLOW_FIXTURE_SSH", ssh)
	t.Setenv("DEVFLOW_FIXTURE_BARE", bare)
	docsGit(t, dir, "config", "--global", "core.sshCommand", `sh "$DEVFLOW_FIXTURE_SSH"`)
	docsGit(t, dir, "config", "--global", "ssh.variant", "simple")
	docsGit(t, dir, "remote", "add", "origin", "git@github.com:engineers/application.git")
	if err := os.Mkdir(filepath.Join(dir, ".devflow"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := "devflow: 1\n" + role + ": {provider: repository, settings: {repository: " + fixtureOrigin + ", directory: records}}\n"
	if err := os.WriteFile(filepath.Join(dir, ".devflow", "config.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(t.Context(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	return a, bare
}

// Run the GitHub HTTP client against an in-memory handler, without a listener.
type docsPRTransport struct{ http.Handler }

func (api docsPRTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}
	w := httptest.NewRecorder()
	api.ServeHTTP(w, req)
	return w.Result(), nil
}

func TestDocsRepositoryPublishPR(t *testing.T) {
	a, bare := repositoryFixture(t, "knowledge_base")
	ctx := t.Context()
	var head string
	const prURL = "https://github.com/docs-team/handbook/pull/1"
	var posts int
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	http.DefaultTransport = docsPRTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "api.github.com" || r.URL.Path != "/repos/docs-team/handbook/pulls" {
			t.Errorf("PR API called for wrong repository: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			q := r.URL.Query()
			if !strings.HasPrefix(q.Get("head"), "docs-team:devflow/publication/") || q.Get("base") != "main" || q.Get("state") != "all" {
				t.Errorf("unexpected PR lookup: %s", r.URL.RawQuery)
			}
			if head == "" {
				// An identically named branch in a fork is a different change.
				fmt.Fprintf(w, `[{"html_url":"wrong","state":"open","head":{"ref":%q,"repo":{"full_name":"docs-team/fork"}},"base":{"ref":"main"}}]`, strings.TrimPrefix(q.Get("head"), "docs-team:"))
				return
			}
			if q.Get("head") != "docs-team:"+head {
				t.Errorf("reconciliation used a different head: %s", q.Get("head"))
			}
			fmt.Fprintf(w, `[{"html_url":%q,"state":"open","base":{"ref":"main"},"head":{"ref":%q,"repo":{"full_name":"docs-team/handbook"}}}]`, prURL, head)
		case http.MethodPost:
			var in struct {
				Head, Base string
				Draft      *bool
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Base != "main" || in.Draft == nil || *in.Draft || !strings.HasPrefix(in.Head, "devflow/publication/") {
				t.Errorf("expected ordinary PR: %+v, %v", in, err)
			}
			head = in.Head
			posts++
			http.Error(w, "lost creation response", http.StatusBadGateway)
		default:
			t.Errorf("unexpected PR API method %s", r.Method)
		}
	})}
	t.Setenv("GH_TOKEN", "fixture")
	ref := provider.Ref{Instance: "repository", Scope: "engineering", ID: "limits"}
	in := DocsPublishInput{Ref: ref, Title: "GPU limits", Body: "Decision.\n", OperationID: "publish-1"}
	before := docsGit(t, bare, "show-ref")
	publish := func(in DocsPublishInput, token string) any {
		t.Helper()
		out, err := a.DocsPublish(ctx, in, token)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	token := publish(in, "").(*Pending).ConfirmToken
	changed := in
	changed.Ref.Scope = "sales"
	if _, err := a.DocsPublish(ctx, changed, token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("changed scope accepted: %v", err)
	}
	// Changing the default target also invalidates the original approval.
	docsGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/PLAT-1-quota")
	if _, err := a.DocsPublish(ctx, in, token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("changed base accepted: %v", err)
	}
	docsGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	if posts != 0 || docsGit(t, bare, "show-ref") != before {
		t.Fatal("wrote before approval")
	}
	result := publish(in, token).(provider.WriteResult)
	if result.Status != provider.PendingReview || result.Ref != ref || result.URL != prURL || result.Revision == "" || posts != 1 {
		t.Fatalf("publish = %+v, posts = %d", result, posts)
	}
	if _, err := a.DocsPublish(ctx, in, token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("approval reused: %v", err)
	}
	// Replaying after a process restart reuses both the branch and the PR.
	reopened, err := Open(ctx, a.Repo.Dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	a = reopened
	refs := docsGit(t, bare, "show-ref")
	retry := publish(in, publish(in, "").(*Pending).ConfirmToken).(provider.WriteResult)
	if retry != result || posts != 1 || docsGit(t, bare, "show-ref") != refs {
		t.Fatalf("replay = %+v, posts = %d", retry, posts)
	}
	search := func(scope string) []provider.Document {
		t.Helper()
		out, err := a.DocsSearch(ctx, "limits", scope)
		if err != nil {
			t.Fatal(err)
		}
		return out["pages"].([]provider.Document)
	}
	if docs := search(ref.Scope); len(docs) != 0 {
		t.Fatalf("unmerged document appeared on base: %+v", docs)
	}
	if _, err := a.DocsGet(ctx, ref); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("exact lookup of pending document: %v", err)
	}
	// Simulate the maintainer accepting the PR in the bare fixture.
	docsGit(t, bare, "update-ref", "refs/heads/main", docsGit(t, bare, "rev-parse", head))
	docs := search(ref.Scope)
	if len(docs) != 1 || docs[0].Revision != result.Revision || docs[0].Title != in.Title || docs[0].Body != in.Body || len(search("sales")) != 0 {
		t.Fatalf("base search = %+v", docs)
	}
	exact, err := a.DocsGet(ctx, ref)
	if err != nil || exact.Ref != ref || exact.Revision != docs[0].Revision || exact.Body != in.Body {
		t.Fatalf("exact lookup after review: %+v, %v", exact, err)
	}
	in.ExpectedRevision, in.OperationID = "stale", "edit-2"
	a.Gate.Autonomy.Remote = config.Auto
	before = docsGit(t, bare, "show-ref")
	if _, err := a.DocsPublish(ctx, in, ""); !errors.Is(err, provider.ErrConflict) || docsGit(t, bare, "show-ref") != before {
		t.Fatalf("stale update: %v", err)
	}
	audit, err := os.ReadFile(filepath.Join(a.Home, "audit.log"))
	if err != nil || !strings.Contains(string(audit), `"action":"docs_publish"`) || !strings.Contains(string(audit), `"class":"remote"`) || !strings.Contains(string(audit), `"result":"ok"`) {
		t.Fatalf("missing publish audit: %s, %v", audit, err)
	}
}

func TestRepositoryPublicationUsesSignedInGitHubCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses shell fixtures")
	}
	a, bare := repositoryFixture(t, "knowledge_base")
	command := filepath.Join(t.TempDir(), "gh")
	const script = `#!/bin/sh
set -eu
if [ "$1" = "--version" ]; then exit 0; fi
test -z "${GH_TOKEN+x}${GITHUB_TOKEN+x}"
test "$1 $2 $3 $4" = 'api --hostname github.com --method'
for endpoint do :; done
case "$5 $endpoint" in
  'GET user')
    [ "$DEVFLOW_FIXTURE_GH_LOGIN" = 'true' ] || exit 4
    printf 'HTTP/2.0 200 OK\r\n\r\n{"id":42,"login":"fixture"}' ;;
  'GET repos/docs-team/handbook/pulls?'*)
    printf 'HTTP/2.0 200 OK\r\n\r\n[]' ;;
  'POST repos/docs-team/handbook/pulls')
    cat >/dev/null
    printf 'HTTP/2.0 201 Created\r\n\r\n{"html_url":"https://github.com/docs-team/handbook/pull/42"}' ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(command)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("DEVFLOW_FIXTURE_GH_LOGIN", "false")
	in := DocsPublishInput{
		Ref:   provider.Ref{Instance: "repository", Scope: "engineering", ID: "knowledge"},
		Title: "Knowledge", Body: "An OKF concept.", OperationID: "publish-native-cli",
	}
	before := docsGit(t, bare, "show-ref")
	if _, err := a.DocsPublish(t.Context(), in, ""); !errors.Is(err, gh.ErrAuthentication) {
		t.Fatalf("missing native login = %v", err)
	}
	t.Setenv("DEVFLOW_FIXTURE_GH_LOGIN", "true")
	out, err := a.DocsPublish(t.Context(), in, "")
	if err != nil {
		t.Fatal(err)
	}
	approval, ok := out.(*Pending)
	if !ok || approval.ConfirmToken == "" || docsGit(t, bare, "show-ref") != before {
		t.Fatalf("preview = %+v", out)
	}
	out, err = a.DocsPublish(t.Context(), in, approval.ConfirmToken)
	result, ok := out.(provider.WriteResult)
	if err != nil || !ok || result.Status != provider.PendingReview || result.Ref != in.Ref ||
		result.URL != "https://github.com/docs-team/handbook/pull/42" {
		t.Fatalf("approved native CLI publication = %+v, %v", out, err)
	}
}

func TestDocsPublicationPreflight(t *testing.T) {
	a, bare := repositoryFixture(t, "knowledge_base")
	// Exercise the implicit origin and HEAD of a local repository.
	docsGit(t, a.Repo.Dir, "remote", "set-url", "origin", bare)
	cfg := a.Cfg.Providers.KnowledgeBase.Settings.(*config.RepositoryKnowledgeSettings)
	slug := "docs-team/handbook"
	cfg.Repository = &slug
	if dest, err := a.knowledgeRepository(t.Context()); err != nil || dest.Origin != "https://github.com/"+slug {
		t.Fatalf("knowledge origin for repository slug: %+v, %v", dest, err)
	}
	cfg.Repository = nil
	a.Gate.Autonomy.Remote = config.Auto
	in := DocsPublishInput{Ref: provider.Ref{Instance: "repository", Scope: "docs", ID: "one"}, Title: "one", OperationID: "create"}
	before := docsGit(t, bare, "show-ref")
	checkDenied := func(want string) {
		t.Helper()
		_, err := a.DocsPublish(t.Context(), in, "")
		if err == nil || !strings.Contains(err.Error(), want) || docsGit(t, bare, "show-ref") != before {
			t.Fatalf("wanted %q without a remote write, got %v", want, err)
		}
	}
	checkDenied("requires a hosted GitHub origin")
	cfg.Publication = "push"
	checkDenied("protected branch")
	branch := "release/q4"
	cfg.Branch = &branch
	checkDenied("protected branch")
	branch = "PLAT-1-quota"
	cfg.Publication = "pull-request"
	origin := fixtureOrigin
	cfg.Repository = &origin
	a.ghc = gh.New("")
	checkDenied("authentication")
	cfg.Publication = "push"
	a.Gate.Autonomy.Remote = config.Suggest
	out, err := a.DocsPublish(t.Context(), in, "")
	if err != nil || out.(*Pending).Status != "suggest" || docsGit(t, bare, "show-ref") != before {
		t.Fatalf("suggest: %+v, %v", out, err)
	}
	a.Gate.Autonomy.Remote = config.Auto
	out, err = a.DocsPublish(t.Context(), in, "")
	if err != nil || out.(provider.WriteResult).Status != provider.Applied {
		t.Fatalf("create: %+v, %v", out, err)
	}
	in.ExpectedRevision = out.(provider.WriteResult).Revision
	in.Body, in.OperationID = "Updated.\n", "edit"
	out, err = a.DocsPublish(t.Context(), in, "")
	if err != nil || out.(provider.WriteResult).Revision == in.ExpectedRevision {
		t.Fatalf("update: %+v, %v", out, err)
	}
	out, err = a.DocsPublish(t.Context(), in, "")
	if err != nil || out.(provider.WriteResult).Status != provider.AlreadyApplied {
		t.Fatalf("update replay: %+v, %v", out, err)
	}
}
