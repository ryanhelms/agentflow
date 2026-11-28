package app

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
)

func draftRefusal(t *testing.T, err error) string {
	t.Helper()
	var r *aisession.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a refusal", err)
	}
	return r.Code
}

const draftCatalog = `[{"name":"bug","revision":"1","required":["title","description"],
  "sections":[{"heading":"Why"},{"heading":"How to test","body":"Run make check."}]},
 {"name":"story","revision":"1","sections":[{"heading":"Story"}]},
 {"name":"urgent","revision":"1","required":["priority"]}]`

func TestTicketDraftContext(t *testing.T) {
	const withDefault = "  provider: filesystem\n  templates: {catalog: catalog.json, default: {name: bug, revision: '1'}}\n"
	const noDefault = "  provider: filesystem\n  templates: {catalog: catalog.json}\n"
	cases := []struct {
		name, settings string
		ref            tasktemplate.Ref
		template, code string
		fails          bool
	}{
		{name: "no task provider", settings: "  provider: none\n", code: "no_task_provider"},
		{name: "jira has no templates", settings: "  provider: jira\n  settings: {site: https://tasks.example, projects: [PLAT]}\n",
			ref: tasktemplate.Ref{Name: "bug", Revision: "1"}},
		{name: "the default", settings: withDefault, template: "bug"},
		{name: "the selected one", settings: withDefault, ref: tasktemplate.Ref{Name: "story", Revision: "1"}, template: "story"},
		{name: "none configured", settings: "  provider: filesystem\n"},
		{name: "selected without a default", settings: noDefault, ref: tasktemplate.Ref{Name: "bug", Revision: "1"}, template: "bug"},
		{name: "unknown", settings: noDefault, ref: tasktemplate.Ref{Name: "nope", Revision: "1"}, fails: true},
		// github-issues cannot carry a priority: no template support for it.
		{name: "unsupported field", settings: "  provider: github-issues\n  templates: {catalog: catalog.json}\n",
			ref: tasktemplate.Ref{Name: "urgent", Revision: "1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := fileTaskApp(t, c.settings)
			if err := os.WriteFile(filepath.Join(a.Repo.Dir, "catalog.json"), []byte(draftCatalog), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := a.TicketDraftContext(t.Context(), c.ref)
			switch {
			case c.code != "":
				if code := draftRefusal(t, err); code != c.code {
					t.Fatalf("code %s", code)
				}
				return
			case c.fails:
				if err == nil {
					t.Fatalf("no error: %+v", got)
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			name := ""
			if got.Template != nil {
				name = got.Template.Name
			}
			if name != c.template {
				t.Errorf("template %q, want %q", name, c.template)
			}
		})
	}
}

// replyAPI is a fake GitHub for the reply draft's reads: the branch's PR
// and its review threads.
type replyAPI struct {
	t       *testing.T
	noPR    bool
	threads []string // graphql thread nodes
}

func (g *replyAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "GET" && r.URL.Path == "/repos/team/app/pulls":
		if g.noPR {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `[{"number":7,"html_url":"https://github.com/team/app/pull/7","head":{"ref":"PLAT-1-quota","sha":"abc"},"base":{"ref":"main"}}]`)
	case r.Method == "POST" && r.URL.Path == "/graphql":
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "mutation") {
			g.t.Errorf("a draft wrote to GitHub: %s", body)
		}
		fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[%s]}}}}}`, strings.Join(g.threads, ","))
	default:
		g.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}
}

func replyNode(id, path, body string) string {
	return fmt.Sprintf(`{"id":%q,"isResolved":false,"isOutdated":false,"path":%q,"line":3,"comments":{"nodes":[{"body":%q,"author":{"login":"alice"}}]}}`, id, path, body)
}

// replyApp is a branch off main with an origin on GitHub.com (never
// contacted) and a later commit on origin/main that is not the branch's. It
// returns the commit the branch left main at.
func replyApp(t *testing.T, api *replyAPI) (*App, string) {
	t.Helper()
	dir := newRepo(t)
	git := func(args ...string) string { t.Helper(); return docsGit(t, dir, args...) }
	put := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := git("rev-parse", "main")
	git("switch", "-q", "main")
	git("switch", "-q", "-c", "later")
	put("upstream.go")
	git("add", ".")
	git("commit", "-q", "-m", "feat: upstream only")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	git("switch", "-q", "PLAT-1-quota")
	put("a.go")
	git("add", "a.go")
	git("commit", "-q", "-m", "feat: add source")
	git("remote", "add", "origin", "git@github.com:team/app.git")
	// A person's own log settings must not change what a draft reads.
	git("config", "log.showSignature", "true")
	t.Setenv("GH_TOKEN", "fixture")
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	api.t = t
	http.DefaultTransport = docsPRTransport{api}
	a, err := Open(t.Context(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	return a, base
}

func TestReplyDraftContext(t *testing.T) {
	var nodes []string
	for i := 1; i <= 7; i++ {
		nodes = append(nodes, replyNode(fmt.Sprintf("T%d", i), "a.go", "Why?"))
	}
	api := &replyAPI{threads: nodes}
	a, base := replyApp(t, api)

	d, err := a.ReplyDraftContext(t.Context(), "", 5)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, th := range d.Threads {
		ids = append(ids, th.ID)
	}
	if !slices.Equal(ids, []string{"T1", "T2", "T3", "T4", "T5"}) {
		t.Errorf("threads %v: the first five as GitHub lists them", ids)
	}
	if d.Base != base {
		t.Errorf("base %q, want %q: where the branch left main", d.Base, base)
	}
	if !strings.Contains(d.Commits, "feat: add source") || strings.Contains(d.Commits, "upstream") ||
		strings.Contains(d.Commits, "dev@example.com") || strings.Contains(d.Commits, "Dev") {
		t.Errorf("commits %q", d.Commits)
	}

	d, err = a.ReplyDraftContext(t.Context(), "T6", 5)
	if err != nil || len(d.Threads) != 1 || d.Threads[0].ID != "T6" {
		t.Errorf("one thread: %+v %v", d, err)
	}
	if _, err = a.ReplyDraftContext(t.Context(), "T99", 5); draftRefusal(t, err) != "unknown_thread" {
		t.Errorf("unknown thread: %v", err)
	}

	api.threads = nil
	if _, err = a.ReplyDraftContext(t.Context(), "", 5); draftRefusal(t, err) != "no_threads" {
		t.Errorf("no threads: %v", err)
	}
	api.threads, api.noPR = nodes, true
	if _, err = a.ReplyDraftContext(t.Context(), "", 5); draftRefusal(t, err) != "no_threads" {
		t.Errorf("no PR: %v", err)
	}
}

// Without a base the draft still has the threads; there is no diff to read
// and the commits are left out.
func TestReplyDraftContextWithoutBase(t *testing.T) {
	a, _ := replyApp(t, &replyAPI{threads: []string{replyNode("T1", "a.go", "first")}})
	docsGit(t, a.Repo.Dir, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	docsGit(t, a.Repo.Dir, "update-ref", "-d", "refs/remotes/origin/main")
	docsGit(t, a.Repo.Dir, "branch", "-q", "-D", "main")
	d, err := a.ReplyDraftContext(t.Context(), "", 5)
	if err != nil || len(d.Threads) != 1 || d.Base != "" || d.Commits != "" {
		t.Errorf("%+v %v", d, err)
	}
}
