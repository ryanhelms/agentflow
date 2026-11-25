package repository

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/provider"
)

func TestOKFRepositoryMixedWrites(t *testing.T) {
	f := setup(t)
	cfg := f.cfg
	cfg.Instance = "repository"
	k, err := NewOKFCompatibleKnowledge(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.engineer, cfg.Directory)
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	const concept = "---\ntype: Article\n---\nAn OKF page.\n"
	put(t, filepath.Join(dir, "nested", "page.md"), concept)
	const v1 = "---\nversion: 1\nkind: document\ninstance: repository\nscope: eng\nid: old\ntitle: Old\n---\n"
	put(t, filepath.Join(dir, "renamed.MD"), v1)
	ref := provider.Ref{Instance: "repository", Scope: "eng", ID: "occupied"}
	occupied := cfg.Directory + "/document-" + ref.StorageKey() + ".md"
	git(t, f.engineer, "add", "--", cfg.Directory)
	git(t, f.engineer, "update-index", "--chmod=+x", "--", cfg.Directory+"/renamed.MD")
	blob := git(t, f.engineer, "hash-object", "-w", "notes.txt")
	git(t, f.engineer, "update-index", "--add", "--cacheinfo", "120000", blob, occupied)
	git(t, f.engineer, "commit", "-m", "seed mixed directory")
	git(t, f.engineer, "push", f.origin, "HEAD:refs/heads/trunk")

	before := git(t, f.origin, "show-ref")
	if _, err := k.Publish(t.Context(), ref, provider.DocumentDraft{Title: "New"}, key("new")); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("create over skipped symlink = %v", err)
	}
	if git(t, f.origin, "show-ref") != before {
		t.Fatal("create over symlink changed Git refs")
	}
	ref.ID = "old"
	doc, err := k.Get(t.Context(), ref)
	if err != nil || doc.Title != "Old" {
		t.Fatalf("renamed v1 = %+v, %v", doc, err)
	}
	opts := provider.WriteOptions{ExpectedRevision: doc.Revision, OperationID: "edit"}
	draft := provider.DocumentDraft{Title: "Edited", Body: "Updated.\n"}
	first, err := k.Publish(t.Context(), ref, draft, opts)
	if err != nil || first.Status != provider.Applied {
		t.Fatalf("edit v1 = %+v, %v", first, err)
	}
	if replay, err := k.Publish(t.Context(), ref, draft, opts); err != nil || replay.Status != provider.AlreadyApplied || replay.Revision != first.Revision {
		t.Fatalf("replay v1 = %+v, %v", replay, err)
	}
	path := cfg.Directory + "/renamed.MD"
	if changed := git(t, f.origin, "diff", "--name-only", "trunk^", "trunk"); changed != path {
		t.Fatalf("expected single v1 edit, got %q", changed)
	}
	if mode := git(t, f.origin, "ls-tree", "trunk", "--", path); !strings.HasPrefix(mode, "100755 ") {
		t.Fatalf("v1 mode changed: %s", mode)
	}
}

// A review can have a valid v1 receipt on its head while the base now contains
// an OKF concept under that reference. Replaying must not republish that review.
func TestOKFRepositoryRejectsObsoleteV1Review(t *testing.T) {
	f := setup(t)
	f.cfg.Publication = "pull-request"
	k, err := NewOKFCompatibleKnowledge(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ref := provider.Ref{Instance: f.cfg.Instance, Scope: "eng", ID: "page"}
	draft := provider.DocumentDraft{Title: "Proposed v1"}
	opts := key("proposal")
	if pending, err := k.Publish(t.Context(), ref, draft, opts); err != nil || pending.Status != provider.PendingReview {
		t.Fatalf("v1 review = %+v, %v", pending, err)
	}
	dir := filepath.Join(f.engineer, f.cfg.Directory, "moved")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(dir, "article.md"), "---\ntype: Reference\nx-agentflow-ref: {scope: eng, id: page}\n---\n")
	git(t, f.engineer, "add", "--", f.cfg.Directory)
	git(t, f.engineer, "commit", "-m", "add OKF article")
	git(t, f.engineer, "push", f.origin, "HEAD:refs/heads/trunk")
	before := git(t, f.origin, "show-ref")
	k.a.Publisher = reviewPublisher(func(Publication) (string, error) {
		t.Fatal("reconciled obsolete review")
		return "", nil
	})
	if _, err := k.Publish(t.Context(), ref, draft, opts); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("obsolete review = %v", err)
	}
	if git(t, f.origin, "show-ref") != before {
		t.Fatal("obsolete review changed Git refs")
	}
}
