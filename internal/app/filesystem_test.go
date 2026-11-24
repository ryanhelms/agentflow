package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/policy"
	"github.com/nscaledev/agentflow/internal/provider"
)

func TestDocsSearchWithFileSystemProvider(t *testing.T) {
	dir := newRepo(t)
	for path, content := range map[string]string{
		".agentflow/config.yml": "agentflow: 1\nknowledge_base: {provider: filesystem, settings: {root: handbook}}\n",
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

func TestKnowledgeRolesReadAndApproveOKFUpdate(t *testing.T) {
	for _, name := range []string{"filesystem", "repository"} {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			file := filepath.Join(dir, "handbook", "moved", "guide.md")
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			const raw = "---\ntype: Runbook\ntags: [quota]\ntitle: GPU quota\nx-agentflow-ref: {scope: eng, id: quota}\n---\nA nested concept.\n"
			if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			settings := "{root: handbook, format: legacy}"
			var bare, before string
			if name == "repository" {
				docsGit(t, dir, "add", "--", "handbook")
				docsGit(t, dir, "commit", "-m", "seed handbook")
				bare = filepath.Join(t.TempDir(), "handbook.git")
				docsGit(t, dir, "clone", "--bare", dir, bare)
				before = docsGit(t, bare, "show-ref")
				settings = fmt.Sprintf("{directory: handbook, publication: push, repository: %q, format: legacy}", bare)
			}
			if err := os.MkdirAll(filepath.Join(dir, ".agentflow"), 0700); err != nil {
				t.Fatal(err)
			}
			cfg := "agentflow: 1\nknowledge_base: {provider: " + name + ", settings: " + settings + "}\n"
			if err := os.WriteFile(filepath.Join(dir, ".agentflow", "config.yml"), []byte(cfg), 0600); err != nil {
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
			doc, err := a.DocsGet(t.Context(), ref)
			if err != nil || doc.Body != "A nested concept.\n" || doc.Revision != pages[0].Revision {
				t.Fatalf("exact lookup = %+v, %v", doc, err)
			}
			a.Gate.Autonomy.Local, a.Gate.Autonomy.Remote = config.Confirm, config.Confirm
			in := DocsPublishInput{Ref: ref, Title: "GPU quota policy", Body: "Updated policy.", Description: "Capacity limits", OperationID: "edit", ExpectedRevision: doc.Revision}
			out, err := a.DocsPublish(t.Context(), in, "")
			if err != nil {
				t.Fatal(err)
			}
			pending := out.(*Pending)
			if pending.Summary.(map[string]any)["format"] != "legacy" {
				t.Fatalf("approval summary = %+v", pending.Summary)
			}
			changed := in
			changed.Tags = []string{} // Clearing existing tags requires its own approval.
			if _, err := a.DocsPublish(t.Context(), changed, pending.ConfirmToken); !errors.Is(err, policy.ErrToken) {
				t.Fatalf("changed tags accepted: %v", err)
			}
			var format *string
			switch s := a.Cfg.Providers.KnowledgeBase.Settings.(type) {
			case *config.FileSystemKnowledgeSettings:
				format = &s.Format
			case *config.RepositoryKnowledgeSettings:
				format = &s.Format
			}
			*format = "okf"
			if _, err := a.DocsPublish(t.Context(), in, pending.ConfirmToken); !errors.Is(err, policy.ErrToken) {
				t.Fatalf("changed format accepted: %v", err)
			}
			if name == "repository" && docsGit(t, bare, "show-ref") != before {
				t.Fatal("rejected request published a Git ref")
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != raw {
				t.Fatalf("source changed before approval = %q, %v", data, err)
			}
			*format = "legacy"
			if _, err := a.DocsPublish(t.Context(), in, pending.ConfirmToken); err != nil {
				t.Fatal(err)
			}
			updated, err := a.DocsGet(t.Context(), ref)
			if err != nil || updated.Description != in.Description || updated.Body != in.Body || updated.Type != "Runbook" ||
				!slices.Equal(updated.Tags, []string{"quota"}) || updated.Revision == doc.Revision {
				t.Fatalf("approved update = %+v, %v", updated, err)
			}
			// A new record in the same store uses the selected legacy format.
			a.Gate.Autonomy.Local, a.Gate.Autonomy.Remote = config.Auto, config.Auto
			create := DocsPublishInput{Ref: provider.Ref{Instance: name, Scope: "eng", ID: "legacy"},
				Title: "Legacy note", Body: "Original record format.", OperationID: "create-legacy"}
			if _, err := a.DocsPublish(t.Context(), create, ""); err != nil {
				t.Fatal(err)
			}
			legacy, err := a.DocsGet(t.Context(), create.Ref)
			if err != nil || legacy.Type != "" || legacy.Ref != create.Ref || legacy.Body != create.Body {
				t.Fatalf("new legacy record = %+v, %v", legacy, err)
			}
		})
	}
}
