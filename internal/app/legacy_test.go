package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gh"
	"github.com/nscaledev/devflow/internal/policy"
)

func legacyApp(t *testing.T, handler http.HandlerFunc) *App {
	t.Helper()
	dir := newRepo(t)
	const cfg = `jira: {site: https://old.example, projects: [OLD]}
confluence: {site: https://old-wiki.example}
task_management: {provider: jira, settings: {site: https://tasks.example, projects: [PLAT, OPS]}}
knowledge_base: {provider: confluence, settings: {site: https://docs.example}}
flow_map: flow.yml
`
	if err := os.Mkdir(filepath.Join(dir, ".devflow"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".devflow/config.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "flow.yml"), []byte("projects:\n  PLAT:\n    pr.merged: QA\n    order: [Open, QA]\n    deploy.production: null\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "fixture")
	t.Setenv("JIRA_USER", "jira-user")
	t.Setenv("JIRA_TOKEN", "jira-fixture")
	t.Setenv("CONFLUENCE_USER", "wiki-user")
	t.Setenv("CONFLUENCE_TOKEN", "wiki-fixture")
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	http.DefaultTransport = docsPRTransport{handler}
	a, err := Open(t.Context(), dir, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	a.Gate.Autonomy.Remote = config.Confirm
	return a
}

const fixtureIssue = `{"key":"PLAT-1","fields":{"summary":"Reach QA","status":{"name":"Open","statusCategory":{"key":"new"}},"priority":{"name":"High"},"issuetype":{"name":"Story"},"assignee":{"displayName":"Dev"},"parent":{"key":"OPS-4","fields":{"summary":"Parent"}}}}`

func TestLegacyProviderReads(t *testing.T) {
	queries := 0
	a := legacyApp(t, func(w http.ResponseWriter, r *http.Request) {
		user, token, _ := r.BasicAuth()
		if r.URL.Host == "docs.example" {
			if user != "wiki-user" || token != "wiki-fixture" || r.URL.Path != "/wiki/rest/api/search" ||
				r.URL.Query().Get("expand") != "content.space,content.version" ||
				r.URL.Query().Get("cql") != `type=page AND text ~ "quota" AND space = "ENG"` {
				t.Fatalf("wrong knowledge request at %s for %q", r.URL, user)
			}
			fmt.Fprint(w, `{"results":[{"content":{"id":"42","space":{"key":"ENG"},"title":"Design","version":{"number":7}},"url":"/design","excerpt":"@@@hl@@@Quota@@@endhl@@@"}]}`)
			return
		}
		if r.URL.Host != "tasks.example" || user != "jira-user" || token != "jira-fixture" {
			t.Fatalf("wrong task request at %s for %q", r.URL, user)
		}
		switch r.URL.Path {
		case "/rest/api/3/search/jql":
			var in struct {
				JQL        string
				MaxResults int
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			want := "project in (PLAT,OPS) AND assignee = currentUser() AND statusCategory != Done"
			if queries == 0 {
				want += " AND sprint in openSprints()"
			}
			if in.JQL != want+" ORDER BY priority DESC, updated DESC" || in.MaxResults != 30 {
				t.Fatalf("search %+v", in)
			}
			queries++
			if queries == 1 {
				fmt.Fprint(w, `{"issues":[]}`)
				return
			}
			fmt.Fprintf(w, `{"issues":[%s]}`, fixtureIssue)
		case "/rest/api/3/issue/PLAT-1":
			fmt.Fprint(w, fixtureIssue)
		case "/rest/api/3/issue/OPS-4/remotelink":
			fmt.Fprint(w, `[{"object":{"url":"https://docs.example/wiki/design","title":"Design"}}]`)
		case "/rest/api/3/issue/PLAT-1/remotelink":
			w.WriteHeader(http.StatusForbidden) // optional enrichment must preserve the rest of the context
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
	})
	ctx := t.Context()
	work, err := a.MyWork(ctx)
	if err != nil || queries != 2 || !reflect.DeepEqual(work, []WorkItem{{Key: "PLAT-1", Summary: "Reach QA", Status: "Open", Type: "Story", Priority: "High"}}) {
		t.Fatalf("work = %+v, %v", work, err)
	}
	tc, err := a.Context(ctx, "")
	if err != nil || tc.Key != "PLAT-1" || tc.Assignee != "Dev" || tc.Epic != "OPS-4 Parent" || len(tc.Docs) != 1 || tc.FlowNext["pr.merged"] != "QA" || !strings.Contains(tc.Text, untrustedNote) {
		t.Fatalf("context = %+v, %v", tc, err)
	}
	a.Cfg.Providers.TaskManagement = config.Selection{Provider: "none"}
	pages, err := a.DocsSearch(ctx, "quota", "ENG")
	want := []atlassian.Page{{ID: "42", Space: "ENG", Title: "Design", URL: "https://docs.example/wiki/design", Excerpt: "Quota", Revision: "7"}}
	if err != nil || !reflect.DeepEqual(pages["pages"], want) || pages["note"] == "" {
		t.Fatalf("docs = %+v, %v", pages, err)
	}
}

func TestLegacyJiraApprovals(t *testing.T) {
	writes := 0
	a := legacyApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "tasks.example" {
			t.Fatalf("unexpected host %s", r.URL.Host)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /rest/api/3/issue/PLAT-1":
			fmt.Fprint(w, fixtureIssue)
		case "GET /rest/api/3/issue/PLAT-1/transitions":
			fmt.Fprint(w, `{"transitions":[{"id":"17","name":"Send for test","to":{"name":"QA","statusCategory":{"key":"indeterminate"}}}]}`)
		case "POST /rest/api/3/issue/PLAT-1/transitions":
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != `{"transition":{"id":"17"}}` {
				t.Fatalf("bad transition %s, %v", body, err)
			}
			writes++
			w.WriteHeader(http.StatusNoContent)
		case "POST /rest/api/3/issue":
			writes++
			fmt.Fprint(w, `{"key":"PLAT-2"}`)
		case "POST /rest/api/3/issueLink":
			http.Error(w, "link rejected", http.StatusBadRequest)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
	})
	ctx := t.Context()
	if _, err := a.Comment(ctx, "../bad", "note", ""); err == nil {
		t.Fatal("approved invalid comment target")
	}
	if _, err := a.CreateTicket(ctx, "PLAT", "Task", "Title", "", "bad", ""); err == nil {
		t.Fatal("approved invalid linked issue")
	}
	v, err := a.Comment(ctx, "PLAT-1", "note", "")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	settings := a.Cfg.Providers.TaskManagement.Settings.(*config.JiraSettings)
	settings.Site = "https://other.example"
	if _, err := a.Comment(ctx, "PLAT-1", "note", token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("retargeted comment: %v", err)
	}
	settings.Site = "https://tasks.example"
	v, err = a.Transition(ctx, "PLAT-1", "deploy.production", false, "")
	if err != nil || v.(TransitionResult).To != "" {
		t.Fatalf("null mapping: %v, %v", v, err)
	}
	v, err = a.Transition(ctx, "PLAT-1", "pr.merged", false, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err = a.Transition(ctx, "PLAT-1", "pr.merged", false, confirmation(t, v))
	if err != nil || !v.(TransitionResult).Applied || v.(TransitionResult).To != "QA" || writes != 1 {
		t.Fatalf("transition: %v, %v, writes %d", v, err, writes)
	}
	v, err = a.CreateTicket(ctx, "PLAT", "Task", "Follow up", "", "PLAT-1", "")
	if err != nil {
		t.Fatal(err)
	}
	v, err = a.CreateTicket(ctx, "PLAT", "Task", "Follow up", "", "PLAT-1", confirmation(t, v))
	if err == nil || v.(map[string]string)["key"] != "PLAT-2" || writes != 2 {
		t.Fatalf("partial create: %v, %v, writes %d", v, err, writes)
	}
}

func TestLegacyGitHubReviewTarget(t *testing.T) {
	threadRepo := "team/app"
	replies := 0
	hasPR := true
	a := legacyApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "api.github.com" {
			t.Fatalf("wrong host %s", r.URL)
		}
		switch r.URL.Path {
		case "/repos/team/app/pulls":
			if r.URL.Query().Get("head") != "fork:PLAT-1-quota" {
				t.Fatalf("wrong head %s", r.URL)
			}
			if !hasPR {
				fmt.Fprint(w, "[]")
				return
			}
			fmt.Fprint(w, `[{"number":17,"html_url":"pr-url","head":{"sha":"abc"}}]`)
		case "/graphql":
			var in struct {
				Query     string
				Variables map[string]any
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.Contains(in.Query, "reviewThreads("):
				if in.Variables["owner"] != "team" || in.Variables["name"] != "app" || in.Variables["n"] != 17.0 {
					t.Fatalf("review target: %+v", in.Variables)
				}
				fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"thread","path":"main.go","line":3,"isOutdated":true,"comments":{"nodes":[{"body":"note","author":{"login":"dev"}}]}}]}}}}}`)
			case strings.Contains(in.Query, "node(id:"):
				if in.Variables["id"] != "thread" {
					t.Fatalf("wrong thread: %+v", in.Variables)
				}
				fmt.Fprintf(w, `{"data":{"node":{"pullRequest":{"number":17,"repository":{"nameWithOwner":%q}}}}}`, threadRepo)
			case strings.Contains(in.Query, "mutation"):
				replies++
				fmt.Fprint(w, `{"data":{}}`)
			default:
				t.Fatalf("unexpected query: %s", in.Query)
			}
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
	})
	ctx := t.Context()
	mustGit(t, a.Repo, "remote", "add", "origin", "git@github.com:fork/app.git")
	mustGit(t, a.Repo, "remote", "add", "upstream", "git@github.com:team/app.git")
	out, err := a.ReviewThreads(ctx)
	want := []gh.Thread{{ID: "thread", Path: "main.go", Line: 3, Author: "dev", Body: "note", Outdated: true}}
	if err != nil || out["number"] != 17 || out["pr"] != "pr-url" || !reflect.DeepEqual(out["threads"], want) {
		t.Fatalf("reviews: %+v, %v", out, err)
	}
	hasPR = false
	out, err = a.ReviewThreads(ctx)
	if err != nil || out["pr"] != nil || !reflect.DeepEqual(out["threads"], []gh.Thread{}) {
		t.Fatalf("no PR: %+v, %v", out, err)
	}
	v, err := a.ReviewReply(ctx, "thread", "reply", "")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	s := v.(*Pending).Summary.(map[string]any)
	if s["repo"] != threadRepo || s["number"] != 17 {
		t.Fatalf("reply summary: %+v", s)
	}
	threadRepo = "other/app"
	if _, err := a.ReviewReply(ctx, "thread", "reply", token); err == nil || replies != 0 {
		t.Fatalf("replied to foreign thread: %v", err)
	}
	threadRepo = "team/app"
	v, err = a.ReviewReply(ctx, "thread", "reply", token)
	if err != nil || !v.(map[string]bool)["replied"] || replies != 1 {
		t.Fatalf("reply: %v, %v", v, err)
	}
	mustGit(t, a.Repo, "remote", "set-url", "--push", "origin", "https://github.com/other/app")
	if _, err := a.PROpen(ctx, PROpenInput{}, ""); err == nil || !strings.Contains(err.Error(), "push destination") {
		t.Fatalf("accepted a different push target: %v", err)
	}
}

func TestLegacyPROpenApprovedDestination(t *testing.T) {
	const branch = "PLAT-1-quota"
	prs := 0
	a := legacyApp(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Host + r.URL.Path {
		case "tasks.example/rest/api/3/issue/PLAT-1":
			fmt.Fprint(w, fixtureIssue)
		case "api.github.com/repos/team/app/pulls":
			var draft struct {
				Title, Body, Head, Base string
				Draft                   bool
			}
			if err := json.NewDecoder(r.Body).Decode(&draft); err != nil {
				t.Fatal(err)
			}
			if r.Method != "POST" || draft.Head != "fork:"+branch || draft.Base != "release" || draft.Body != "Changes" || !draft.Draft {
				t.Fatalf("unexpected PR write: %+v", draft)
			}
			prs++
			fmt.Fprint(w, `{"number":23,"html_url":"pr-url","draft":true}`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
	})
	ctx := t.Context()
	mustGit(t, a.Repo, "remote", "add", "origin", "git@github.com:fork/app.git")
	mustGit(t, a.Repo, "remote", "add", "upstream", "git@github.com:team/app.git")
	mustGit(t, a.Repo, "symbolic-ref", "refs/remotes/upstream/HEAD", "refs/remotes/upstream/release")
	// Record the approved push argv without performing any network or Git write.
	bin := t.TempDir()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FIXTURE_GIT", git)
	log := filepath.Join(bin, "push")
	t.Setenv("FIXTURE_PUSH", log)
	const script = `#!/bin/sh
if [ "$1" = push ]; then
	printf '%s\n' "$@" > "$FIXTURE_PUSH"
	exit 0
fi
exec "$FIXTURE_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	draft := true
	in := PROpenInput{Title: "feat(PLAT-1): add quota", Body: "Changes", Draft: &draft}
	v, err := a.PROpen(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	s := v.(*Pending).Summary.(map[string]any)
	if s["repo"] != "team/app" || s["source_repo"] != "fork/app" || s["head"] != "fork:"+branch || s["base"] != "release" {
		t.Fatalf("approval: %+v", s)
	}
	mustGit(t, a.Repo, "remote", "set-url", "upstream", "git@github.com:someone/app.git")
	if _, err := a.PROpen(ctx, in, token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("changed PR target: %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) || prs != 0 {
		t.Fatal("pushed or opened before approval")
	}
	mustGit(t, a.Repo, "remote", "set-url", "upstream", "git@github.com:team/app.git")
	v, err = a.PROpen(ctx, in, token)
	if err != nil || !reflect.DeepEqual(v, map[string]any{"number": 23, "url": "pr-url", "draft": true, "status": "opened"}) || prs != 1 {
		t.Fatalf("opened: %v, %v", v, err)
	}
	b, err := os.ReadFile(log)
	if err != nil || string(b) != "push\n--set-upstream\n--\norigin\nrefs/heads/"+branch+":refs/heads/"+branch+"\n" {
		t.Fatalf("push args: %q, %v", b, err)
	}
}

func TestLegacyConfigWithoutProviderSections(t *testing.T) {
	ctx := t.Context()
	comments := 0
	a := legacyApp(t, func(w http.ResponseWriter, r *http.Request) {
		user, token, _ := r.BasicAuth()
		if r.URL.Host != "shared.example" || user != "jira-user" || token != "jira-fixture" {
			t.Fatalf("unexpected legacy site or identity at %s", r.URL)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /wiki/rest/api/search":
			fmt.Fprint(w, `{"results":[]}`)
		case "POST /rest/api/3/issue/PLAT-1/comment":
			comments++
			w.WriteHeader(http.StatusCreated)
		case "GET /rest/api/3/issue/PLAT-1":
			fmt.Fprint(w, fixtureIssue)
		case "GET /rest/api/3/myself":
			fmt.Fprint(w, `{"accountId":"developer"}`)
		case "PUT /rest/api/3/issue/PLAT-1/assignee":
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != `{"accountId":"developer"}` {
				t.Fatalf("assignment: %s, %v", body, err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
	})
	t.Setenv("CONFLUENCE_USER", "")
	t.Setenv("CONFLUENCE_TOKEN", "")
	dir := a.Repo.Dir
	if err := os.WriteFile(filepath.Join(dir, ".devflow/config.yml"), []byte("jira: {site: https://shared.example}\nflow_map: flow.yml\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, a.Repo, "add", ".devflow/config.yml", "flow.yml")
	mustGit(t, a.Repo, "commit", "-qm", "chore(PLAT-1): add fixture")
	a, err := Open(ctx, dir, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	a.Gate.Autonomy.Remote = config.Confirm
	out, err := a.DocsSearch(ctx, "quota", "")
	if err != nil || !reflect.DeepEqual(out["pages"], []atlassian.Page{}) {
		t.Fatalf("empty legacy search: %+v, %v", out, err)
	}
	v, err := a.Start(ctx, "PLAT-1", false, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err = a.Start(ctx, "PLAT-1", false, confirmation(t, v))
	if err != nil {
		t.Fatal(err)
	}
	start := v.(StartResult)
	if !start.Assigned || start.Key != "PLAT-1" || start.Branch != "PLAT-1-reach-qa" || start.Transition != nil {
		t.Fatalf("start = %+v", start)
	}
	v, err = a.Comment(ctx, "", "note", "")
	if err != nil {
		t.Fatal(err)
	}
	v, err = a.Comment(ctx, "", "note", confirmation(t, v))
	if err != nil || !reflect.DeepEqual(v, map[string]any{"key": "PLAT-1", "commented": true}) || comments != 1 {
		t.Fatalf("comment: %+v, %v", v, err)
	}
}
