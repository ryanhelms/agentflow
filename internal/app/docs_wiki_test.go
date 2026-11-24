package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/provider"
)

func TestDocsWikiSearchAndRewrittenPushTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	dir := newRepo(t)
	const origin = "https://github.com/Docs-Team/Runbooks.wiki.git"
	const branch = "wiki/current"
	ref := provider.Ref{Instance: "github-wiki", Scope: "engineering", ID: "quota"}
	slug := "document-" + ref.StorageKey()
	records := filepath.Join(dir, "agentflow-pages")
	if err := os.Mkdir(records, 0755); err != nil {
		t.Fatal(err)
	}
	record := "---\nversion: 1\nkind: document\ninstance: github-wiki\nscope: engineering\nid: quota\ntitle: Quota\n---\nAdmission controls.\n"
	if err := os.WriteFile(filepath.Join(records, slug+".md"), []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	docsGit(t, dir, "switch", "-c", branch)
	docsGit(t, dir, "add", "agentflow-pages")
	docsGit(t, dir, "commit", "-m", "seed wiki")
	bare := filepath.Join(t.TempDir(), "runbooks.wiki.git")
	docsGit(t, dir, "clone", "--bare", "--", dir, bare)
	// Any unexpected HTTPS destination also resolves locally and fails fast.
	docsGit(t, dir, "config", "--global", "url."+bare+".insteadOf", origin)
	docsGit(t, dir, "config", "--global", "url."+bare+"-unexpected/.insteadOf", "https://")
	docsGit(t, dir, "remote", "add", "origin", "git@github.com:fork/runbooks.git")
	docsGit(t, dir, "remote", "add", "upstream", "git@github.com:upstream/runbooks.git")
	docsGit(t, dir, "remote", "add", "handbook", "ssh://git@ssh.github.com:443/Docs-Team/Runbooks.git")
	if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := "agentflow: 1\ncode_hosting: {settings: {target_remote: handbook}}\nknowledge_base: {provider: github-wiki}\n"
	if err := os.WriteFile(filepath.Join(dir, ".agentflow", "config.yml"), []byte(cfg), 0600); err != nil {
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
	in := DocsPublishInput{Ref: ref, Title: "Quota update", Body: "New text", OperationID: "edit-quota", ExpectedRevision: pages[0].Revision}
	withTags := in
	withTags.Tags = []string{"capacity"}
	if out, err := a.DocsPublish(ctx, withTags, ""); out != nil || !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("wiki metadata reached Git destination resolution: %v, %v", out, err)
	}
	if _, err := a.DocsPublish(ctx, in, ""); err == nil || !strings.Contains(err.Error(), "git rewrites the repository push destination") {
		t.Fatalf("expected rejection before approval for the rewritten wiki URL: %v", err)
	}
	if refs := docsGit(t, bare, "show-ref"); refs != before {
		t.Fatalf("wiki refs changed from %q to %q", before, refs)
	}
}

func TestDocsWikiRequiresGitHubDotComTarget(t *testing.T) {
	dir := newRepo(t)
	if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := "agentflow: 1\nknowledge_base: {provider: github-wiki}\n"
	if err := os.WriteFile(filepath.Join(dir, ".agentflow", "config.yml"), []byte(cfg), 0600); err != nil {
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
	cfg = "agentflow: 1\nknowledge_base: {provider: github-wiki, settings: {repository: org/handbook}}\n"
	if err := os.WriteFile(filepath.Join(dir, ".agentflow", "config.yml"), []byte(cfg), 0600); err != nil {
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
