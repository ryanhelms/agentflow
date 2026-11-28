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
// contacted), changes to ordinary, secret and non-ASCII paths, and a later
// commit on origin/main that is not the branch's.
func replyApp(t *testing.T, api *replyAPI) *App {
	t.Helper()
	dir := newRepo(t)
	git := func(args ...string) { t.Helper(); docsGit(t, dir, args...) }
	put := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("switch", "-q", "main")
	git("switch", "-q", "-c", "later")
	put("upstream.go", "package upstream\n")
	git("add", ".")
	git("commit", "-q", "-m", "feat: upstream only")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	git("switch", "-q", "PLAT-1-quota")
	put(".env", "DB_HOST=plain-value\n")
	put("café/.env", "DB_HOST=other-plain-value\n")
	put("src/a.go", "package src // changed\n")
	put("src/other.go", "package src // not reviewed\n")
	git("add", "-f", ".")
	git("commit", "-q", "-m", "feat: add source")
	git("remote", "add", "origin", "git@github.com:team/app.git")
	// A person's own diff settings must not change what a draft reads.
	git("config", "diff.noprefix", "true")
	git("config", "diff.mnemonicprefix", "true")
	git("config", "diff.external", "false")
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
	return a
}

func TestReplyDraftContext(t *testing.T) {
	nodes := []string{
		replyNode("T1", "src/a.go", "first"),
		replyNode("T2", "src/a.go", "second"),
		replyNode("T3", ".env", "third"),
		replyNode("T4", "café/.env", "fourth"),
		// A reviewer's path is never a pathspec: magic matches nothing.
		replyNode("T5", ":(glob)**", "fifth"),
		replyNode("T6", "upstream.go", "sixth"),
		replyNode("T7", "", "seventh"),
	}
	api := &replyAPI{threads: nodes}
	a := replyApp(t, api)

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
	var paths []string
	for _, h := range d.Hunks {
		paths = append(paths, h.Path)
		if !strings.HasPrefix(h.Diff, "diff --git a/"+h.Path+" b/"+h.Path+"\n") {
			t.Errorf("%s: header %q: the filter needs a/ b/ prefixes to see secret files", h.Path, strings.SplitN(h.Diff, "\n", 2)[0])
		}
		if strings.Count(h.Diff, "diff --git ") != 1 {
			t.Errorf("%s: another file's diff is in it:\n%s", h.Path, h.Diff)
		}
	}
	if !slices.Equal(paths, []string{"src/a.go", ".env", "café/.env"}) {
		t.Errorf("hunks %v", paths)
	}
	if !strings.Contains(d.Commits, "feat: add source") || strings.Contains(d.Commits, "upstream") ||
		strings.Contains(d.Commits, "dev@example.com") || strings.Contains(d.Commits, "Dev") {
		t.Errorf("commits %q", d.Commits)
	}

	// A thread whose file only changed on the base after the branch left it
	// has no hunks.
	d, err = a.ReplyDraftContext(t.Context(), "T6", 5)
	if err != nil || len(d.Threads) != 1 || d.Threads[0].ID != "T6" || len(d.Hunks) != 0 {
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

// Without a base the draft still has the threads; the diff and the commits
// are left out.
func TestReplyDraftContextWithoutBase(t *testing.T) {
	api := &replyAPI{threads: []string{replyNode("T1", "src/a.go", "first")}}
	a := replyApp(t, api)
	docsGit(t, a.Repo.Dir, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	docsGit(t, a.Repo.Dir, "update-ref", "-d", "refs/remotes/origin/main")
	docsGit(t, a.Repo.Dir, "branch", "-q", "-D", "main")
	d, err := a.ReplyDraftContext(t.Context(), "", 5)
	if err != nil || len(d.Threads) != 1 || len(d.Hunks) != 0 || d.Commits != "" {
		t.Errorf("%+v %v", d, err)
	}
}

// A person's diff.context cannot widen what a draft reads to whole files.
func TestReplyDraftContextKeepsContextShort(t *testing.T) {
	a := replyApp(t, &replyAPI{threads: []string{replyNode("T1", "long.txt", "first")}})
	commit := func(changed string) {
		t.Helper()
		var b strings.Builder
		for i := 1; i <= 40; i++ {
			if i == 20 && changed != "" {
				fmt.Fprintln(&b, changed)
				continue
			}
			fmt.Fprintf(&b, "row-%02d\n", i)
		}
		if err := os.WriteFile(filepath.Join(a.Repo.Dir, "long.txt"), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		docsGit(t, a.Repo.Dir, "add", "long.txt")
		docsGit(t, a.Repo.Dir, "commit", "-q", "-m", "chore: long file")
	}
	commit("")
	docsGit(t, a.Repo.Dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	commit("changed")
	docsGit(t, a.Repo.Dir, "config", "diff.context", "1000")
	docsGit(t, a.Repo.Dir, "config", "diff.interHunkContext", "1000")

	d, err := a.ReplyDraftContext(t.Context(), "", 5)
	if err != nil || len(d.Hunks) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	diff := d.Hunks[0].Diff
	// A context line starts its own line; the hunk header may name a row.
	if !strings.Contains(diff, "\n row-17\n") || !strings.Contains(diff, "\n+changed\n") ||
		strings.Contains(diff, "\n row-16\n") || strings.Contains(diff, "\n row-24\n") {
		t.Errorf("not three lines of context:\n%s", diff)
	}
}
