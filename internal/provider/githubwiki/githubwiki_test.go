package githubwiki

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/provider"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{
		"-c", "user.name=test", "-c", "user.email=test@example.org",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.DevNull,
	}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", args[0], err, out)
	}
	return strings.TrimSpace(string(out))
}

func newWiki(t *testing.T, origin string) *Knowledge {
	t.Helper()
	wiki, err := NewKnowledge(Config{Instance: "wiki", Repository: "Example-org/Runbooks", Origin: origin})
	if err != nil {
		t.Fatal(err)
	}
	return wiki
}

func TestWikiPublishAndRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := filepath.Join(root, "runbooks.wiki.git")
	seed := filepath.Join(root, "seed")
	const branch = "wiki/current"
	const home = "# Home\n\nAn existing ordinary Markdown page.\n"
	git(t, root, "init", "--bare", "--initial-branch="+branch, origin)
	git(t, root, "init", "--initial-branch="+branch, seed)
	if err := os.WriteFile(filepath.Join(seed, "Home.md"), []byte(home), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(seed, directory, "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "okf.md"), []byte("---\ntype: Article\n---\nordinary Markdown"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", "--", "Home.md", directory)
	git(t, seed, "commit", "-m", "initialize wiki")
	git(t, seed, "push", origin, "HEAD:refs/heads/"+branch)
	git(t, origin, "branch", "main", branch)
	before := git(t, origin, "rev-parse", "main")

	ctx := t.Context()
	wiki := newWiki(t, origin)
	ref := provider.Ref{Instance: "wiki", Scope: "SRE", ID: "Deploy / #1"}
	if _, err := wiki.Get(ctx, ref); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("missing managed document: %v", err)
	}
	draft := provider.DocumentDraft{Title: "Deploy", Body: "Deploy safely.\r\n", Links: []provider.Link{{Title: "Home"}}}
	opts := provider.WriteOptions{OperationID: "create"}
	first, err := wiki.Publish(ctx, ref, draft, opts)
	slug := "document-" + ref.StorageKey()
	wantURL := "https://github.com/Example-org/Runbooks/wiki/" + slug
	if err != nil || first.Status != provider.Applied || first.Ref != ref || first.Revision == "" || first.URL != wantURL {
		t.Fatalf("publish: %+v, %v", first, err)
	}
	if paths := git(t, origin, "diff", "--name-only", branch+"^", branch); paths != "agentflow-pages/"+slug+".md" {
		t.Fatalf("changed paths: %q", paths)
	}

	other := newWiki(t, origin)
	doc, err := other.Get(ctx, ref)
	if err != nil || doc.Ref != ref || doc.Title != draft.Title || doc.URL != wantURL ||
		doc.Revision != first.Revision || doc.Body != draft.Body || len(doc.Links) != 1 || doc.Links[0] != draft.Links[0] {
		t.Fatalf("get: %+v, %v", doc, err)
	}
	docs, err := other.Find(ctx, provider.DocumentQuery{Scope: "SRE", Text: "SAFELY", Limit: 1})
	if err != nil || len(docs) != 1 || docs[0].Ref != ref || docs[0].URL != wantURL {
		t.Fatalf("find managed page: %+v, %v", docs, err)
	}
	docs, err = other.Find(ctx, provider.DocumentQuery{Text: "ordinary Markdown"})
	if err != nil || len(docs) != 0 {
		t.Fatalf("find ordinary page: %+v, %v", docs, err)
	}

	update := provider.DocumentDraft{Title: "Deploy again", Body: "Updated procedure."}
	second, err := other.Publish(ctx, ref, update, provider.WriteOptions{OperationID: "update", ExpectedRevision: first.Revision})
	if err != nil || second.Status != provider.Applied || second.Revision == first.Revision || second.URL != wantURL {
		t.Fatalf("update: %+v, %v", second, err)
	}
	if _, err := wiki.Publish(ctx, ref, draft, provider.WriteOptions{OperationID: "stale", ExpectedRevision: first.Revision}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
	replay, err := wiki.Publish(ctx, ref, draft, opts)
	if err != nil || replay.Status != provider.AlreadyApplied || replay.Revision != second.Revision || replay.URL != wantURL {
		t.Fatalf("replay: %+v, %v", replay, err)
	}
	if _, err := wiki.Publish(ctx, ref, update, opts); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("reused operation ID: %v", err)
	}
	if got := git(t, origin, "rev-list", "--count", branch); got != "3" {
		t.Fatalf("wiki history contains %s commits, want 3", got)
	}
	if git(t, origin, "rev-parse", "main") != before || git(t, origin, "show", branch+":Home.md") != strings.TrimSpace(home) {
		t.Fatal("publish changed main or the ordinary Home page")
	}
}

func TestWikiUnavailable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := filepath.Join(root, "wiki.git")
	git(t, root, "init", "--bare", "--initial-branch=wiki", origin)
	for _, tc := range []struct {
		name   string
		origin string
		want   string
	}{
		{"missing", filepath.Join(root, "missing.git"), "check Git credentials"},
		{"empty", origin, "no initialized default branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wiki := newWiki(t, tc.origin)
			ref := provider.Ref{Instance: "wiki", Scope: "SRE", ID: "runbook"}
			_, err := wiki.Publish(t.Context(), ref, provider.DocumentDraft{Title: "Runbook"}, provider.WriteOptions{})
			for _, msg := range []string{tc.want, "enable Wikis", "create the first page", "https://github.com/Example-org/Runbooks/wiki"} {
				if err == nil || !strings.Contains(err.Error(), msg) {
					t.Fatalf("error %v should mention %q", err, msg)
				}
			}
		})
	}
	if refs := git(t, origin, "for-each-ref"); refs != "" {
		t.Fatalf("empty wiki gained refs: %s", refs)
	}
}

func TestWikiConfiguration(t *testing.T) {
	t.Parallel()
	wiki := newWiki(t, "")
	if wiki.origin != "https://github.com/Example-org/Runbooks.wiki.git" {
		t.Fatalf("canonical Git remote: %q", wiki.origin)
	}
	if got := wiki.pageURL("Guide #1/100%/café?"); got != "https://github.com/Example-org/Runbooks/wiki/Guide%20%231%2F100%25%2Fcaf%C3%A9%3F" {
		t.Fatalf("escaped URL: %q", got)
	}
	for _, repo := range []string{
		"repo", "owner/", "/repo", "owner/repo/nested", "https://github.com/owner/repo", "owner/.", "owner/..",
		"-owner/repo", "owner-/repo", "owner--name/repo", "owner/repo?x", "owner/repo#frag", "owner/repo%2fpath", "owner/repo\n",
	} {
		if _, err := NewKnowledge(Config{Instance: "wiki", Repository: repo}); err == nil {
			t.Errorf("accepted malformed repository %q", repo)
		}
	}
	if _, err := NewKnowledge(Config{Instance: "wiki", Repository: "owner/repo", Origin: "../wiki.git"}); err == nil {
		t.Error("accepted relative override")
	}
	if _, err := NewKnowledge(Config{Repository: "owner/repo"}); err == nil {
		t.Error("accepted empty instance")
	}
}
