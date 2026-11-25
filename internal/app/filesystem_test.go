package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/provider"
)

func TestDocsSearchWithFileSystemProvider(t *testing.T) {
	dir := newRepo(t)
	for path, content := range map[string]string{
		".devflow/config.yml": "devflow: 1\nknowledge_base: {provider: filesystem, settings: {root: handbook}}\n",
		"handbook/quota.md":   "---\nversion: 1\nkind: document\ninstance: filesystem\nscope: engineering\nid: quota\ntitle: Quota design\n---\nAn engineering decision.\n",
	} {
		path = filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, err := Open(t.Context(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.DocsSearch(t.Context(), "quota", "engineering")
	if err != nil {
		t.Fatal(err)
	}
	pages := result["pages"].([]provider.Document)
	if len(pages) != 1 || pages[0].Ref.ID != "quota" || pages[0].Title != "Quota design" {
		t.Fatalf("pages = %+v", pages)
	}
}

func TestKnowledgeRolesReadOKFAndRejectConfirmedOverwrite(t *testing.T) {
	for _, name := range []string{"filesystem", "repository"} {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			file := filepath.Join(dir, "handbook", "moved", "guide.md")
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			const raw = "---\ntype: Runbook\ntitle: GPU quota\nx-devflow-ref: {scope: eng, id: quota}\n---\nA nested concept.\n"
			if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			settings := "{root: handbook}"
			var bare, before string
			if name == "repository" {
				docsGit(t, dir, "add", "--", "handbook")
				docsGit(t, dir, "commit", "-m", "seed handbook")
				bare = filepath.Join(t.TempDir(), "handbook.git")
				docsGit(t, dir, "clone", "--bare", dir, bare)
				before = docsGit(t, bare, "show-ref")
				settings = fmt.Sprintf("{directory: handbook, publication: push, repository: %q}", bare)
			}
			if err := os.MkdirAll(filepath.Join(dir, ".devflow"), 0700); err != nil {
				t.Fatal(err)
			}
			cfg := "devflow: 1\nknowledge_base: {provider: " + name + ", settings: " + settings + "}\n"
			if err := os.WriteFile(filepath.Join(dir, ".devflow", "config.yml"), []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			a, err := Open(t.Context(), dir, "test")
			if err != nil {
				t.Fatal(err)
			}
			ref := provider.Ref{Instance: name, Scope: "eng", ID: "quota"}
			result, err := a.DocsSearch(t.Context(), "GPU", "eng")
			if err != nil {
				t.Fatal(err)
			}
			pages := result["pages"].([]provider.Document)
			if len(pages) != 1 || pages[0].Ref != ref {
				t.Fatalf("search = %+v", pages)
			}
			doc, err := a.linkedKnowledgeDocument(t.Context(), ref)
			if err != nil || doc.Body != "A nested concept.\n" || doc.Revision != pages[0].Revision {
				t.Fatalf("exact lookup = %+v, %v", doc, err)
			}
			a.Gate.Autonomy.Local, a.Gate.Autonomy.Remote = config.Confirm, config.Confirm
			in := DocsPublishInput{Ref: ref, Title: "Overwrite", OperationID: "edit", ExpectedRevision: doc.Revision}
			out, err := a.DocsPublish(t.Context(), in, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.DocsPublish(t.Context(), in, out.(*Pending).ConfirmToken); !errors.Is(err, provider.ErrUnsupported) {
				t.Fatalf("confirmed overwrite = %v", err)
			}
			if name == "repository" && docsGit(t, bare, "show-ref") != before {
				t.Fatal("confirmation published a Git ref")
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != raw {
				t.Fatalf("source changed = %q, %v", data, err)
			}
		})
	}
}
