package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
)

func TestDocsWikiSearchAndApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	dir := newRepo(t)
	const origin = "https://github.com/Docs-Team/Runbooks.wiki.git"
	const branch = "wiki/current"
	ref := provider.Ref{Instance: "github-wiki", Scope: "engineering", ID: "quota"}
	slug := "document-" + ref.StorageKey()
	records := filepath.Join(dir, "devflow-pages")
	if err := os.Mkdir(records, 0755); err != nil {
		t.Fatal(err)
	}
	record := "---\nversion: 1\nkind: document\ninstance: github-wiki\nscope: engineering\nid: quota\ntitle: Quota\n---\nAdmission controls.\n"
	if err := os.WriteFile(filepath.Join(records, slug+".md"), []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	docsGit(t, dir, "switch", "-c", branch)
	docsGit(t, dir, "add", "devflow-pages")
	docsGit(t, dir, "commit", "-m", "seed wiki")
	docsGit(t, dir, "branch", "wiki/other")
	bare := filepath.Join(t.TempDir(), "runbooks.wiki.git")
	docsGit(t, dir, "clone", "--bare", "--", dir, bare)
	// Any unexpected HTTPS destination also resolves locally and fails fast.
	docsGit(t, dir, "config", "--global", "url."+bare+".insteadOf", origin)
	docsGit(t, dir, "config", "--global", "url."+bare+"-unexpected/.insteadOf", "https://")
	docsGit(t, dir, "remote", "add", "origin", "git@github.com:fork/runbooks.git")
	docsGit(t, dir, "remote", "add", "upstream", "git@github.com:upstream/runbooks.git")
	docsGit(t, dir, "remote", "add", "handbook", "ssh://git@ssh.github.com:443/Docs-Team/Runbooks.git")
	if err := os.Mkdir(filepath.Join(dir, ".devflow"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := "devflow: 1\ncode_hosting: {settings: {target_remote: handbook}}\nknowledge_base: {provider: github-wiki}\n"
	if err := os.WriteFile(filepath.Join(dir, ".devflow", "config.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	a, err := Open(ctx, dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	search := func(scope string) []provider.Document {
		t.Helper()
		out, err := a.DocsSearch(ctx, "admission", scope)
		if err != nil {
			t.Fatal(err)
		}
		return out["pages"].([]provider.Document)
	}
	pages := search(ref.Scope)
	url := "https://github.com/Docs-Team/Runbooks/wiki/" + slug
	if len(pages) != 1 || pages[0].Ref != ref || pages[0].URL != url || pages[0].Revision == "" || len(search("sales")) != 0 {
		t.Fatalf("wiki search: %+v", pages)
	}

	before := docsGit(t, bare, "show-ref")
	in := DocsPublishInput{Ref: ref, Title: "Quota update", Body: "New text", OperationID: "edit-quota", ExpectedRevision: "stale"}
	out, err := a.DocsPublish(ctx, in, "")
	pending, ok := out.(*Pending)
	if err != nil || !ok || pending.Status != "needs_confirmation" || pending.ConfirmToken == "" {
		t.Fatalf("wiki publication plan: %+v, %v", out, err)
	}
	summary := pending.Summary.(map[string]any)
	if summary["origin"] != origin || summary["directory"] != "devflow-pages" ||
		summary["publication"] != "push" || summary["base"] != branch || summary["target"] != branch {
		t.Fatalf("wiki publication destination: %+v", summary)
	}
	docsGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/wiki/other")
	if _, err := a.DocsPublish(ctx, in, pending.ConfirmToken); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("approval accepted after wiki target changed: %v", err)
	}
	docsGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	if _, err := a.DocsPublish(ctx, in, ""); err == nil || !strings.Contains(err.Error(), `protected branch "main"`) {
		t.Fatalf("wiki publication to protected branch: %v", err)
	}
	docsGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	if _, err := a.DocsPublish(ctx, in, pending.ConfirmToken); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("approved wiki write with stale revision: %v", err)
	}
	if refs := docsGit(t, bare, "show-ref"); refs != before {
		t.Fatalf("wiki refs changed from %q to %q", before, refs)
	}
}

func TestDocsWikiRequiresGitHubDotComTarget(t *testing.T) {
	dir := newRepo(t)
	if err := os.Mkdir(filepath.Join(dir, ".devflow"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := "devflow: 1\nknowledge_base: {provider: github-wiki}\n"
	if err := os.WriteFile(filepath.Join(dir, ".devflow", "config.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(t.Context(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	docsGit(t, dir, "remote", "add", "origin", filepath.Join(dir, "missing.git"))
	for _, remote := range []string{
		filepath.Join(dir, "missing.git"),
		"git@gitlab.com:org/repo.git",
		"git@corp.ghe.com:org/repo.git",
		"https://github.com:1234/org/repo.git",
	} {
		docsGit(t, dir, "remote", "set-url", "origin", remote)
		if _, err := a.knowledgeWiki(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "knowledge_base.settings.repository") {
			t.Fatalf("%s: expected a configuration hint, got %v", remote, err)
		}
	}
	cfg = "devflow: 1\nknowledge_base: {provider: github-wiki, settings: {repository: org/handbook}}\n"
	if err := os.WriteFile(filepath.Join(dir, ".devflow", "config.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	a, err = Open(t.Context(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	wiki, err := a.knowledgeWiki(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if origin, _ := wiki.Destination(); origin != "https://github.com/org/handbook.wiki.git" {
		t.Fatalf("explicit wiki destination: %s", origin)
	}
}
