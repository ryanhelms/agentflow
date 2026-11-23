package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/okf"
)

func repositoryWriter(t *testing.T, cfg Config, format string) *Knowledge {
	t.Helper()
	k, err := NewOKFKnowledge(cfg, format)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestWriterMigratedGitBlob(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.cfg.Instance = "local" // same instance as the historical v1 fingerprint
	cfg := f.cfg
	if _, err := NewOKFKnowledge(cfg, "markdown"); err == nil {
		t.Fatal("accepted unknown format")
	}
	dir := filepath.Join(f.engineer, cfg.Directory, "imports", "archive")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const hash = "67bf72b1db485dd9aa273c4be94ef04c14494dd156375d6267fbbda654c8a79f"
	const body = "## Source\n[linked](../../linked.md)"
	put(t, filepath.Join(dir, "moved.md"), "---\ntype: Reference\ntitle: Manual\ntags: [team]\n"+
		"x-devflow-ref: {scope: engineering, id: provider-architecture}\n"+
		"x-devflow-links: [{title: Related, url: 'urn:related'}]\n"+
		"verified: [{by: 'human:alice', x-team: {rating: '4'}}]\n"+
		"x-devflow-operations: {old: "+hash+"}\n---\n"+body)
	path := cfg.Directory + "/imports/archive/moved.md"
	git(t, f.engineer, "add", "--", path)
	git(t, f.engineer, "update-index", "--chmod=+x", "--", path)
	git(t, f.engineer, "commit", "-m", "seed migrated receipt")
	git(t, f.engineer, "push", f.origin, "HEAD:refs/heads/trunk")

	ref := provider.Ref{Instance: cfg.Instance, Scope: "engineering", ID: "provider-architecture"}
	legacy := repositoryWriter(t, cfg, "legacy")
	oldDraft := provider.DocumentDraft{Title: "Architecture", Body: "Body.\n"}
	first, err := legacy.Publish(t.Context(), ref, oldDraft, key("old"))
	if err != nil || first.Status != provider.AlreadyApplied {
		t.Fatalf("migrated replay = %+v, %v", first, err)
	}
	different := oldDraft
	different.Tags = []string{}
	if _, err := legacy.Publish(t.Context(), ref, different, key("old")); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("replayed v1 with metadata: %v", err)
	}
	draft := provider.DocumentDraft{Type: "ADR", Description: "decision"}
	opts := provider.WriteOptions{OperationID: "amend", ExpectedRevision: first.Revision}
	changed, err := legacy.Publish(t.Context(), ref, draft, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.origin, "diff", "--name-only", "trunk^", "trunk"); got != path {
		t.Fatalf("changed paths = %s", got)
	}
	if mode := git(t, f.origin, "ls-tree", "trunk", "--", path); !strings.HasPrefix(mode, "100755 ") {
		t.Fatalf("changed Git mode: %s", mode)
	}
	raw := git(t, f.origin, "show", "trunk:"+path)
	if !strings.Contains(raw, "rating: '4'") {
		t.Fatalf("lost nested extension: %s", raw)
	}
	k := repositoryWriter(t, cfg, "okf")
	got, err := k.Get(t.Context(), ref)
	if err != nil || got.Revision != changed.Revision || got.Title != "Manual" || got.Type != "ADR" || got.Description != "decision" ||
		!slices.Equal(got.Tags, []string{"team"}) || len(got.Links) != 1 || got.Links[0].URL != "urn:related" || got.Body != body {
		t.Fatalf("updated Git document = %+v, %v", got, err)
	}
	before := git(t, f.origin, "show-ref")
	if _, err := k.Publish(t.Context(), ref, draft, provider.WriteOptions{ExpectedRevision: first.Revision}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("stale Git revision = %v", err)
	}
	if replay, err := k.Publish(t.Context(), ref, draft, opts); err != nil || replay.Status != provider.AlreadyApplied || replay.Revision != changed.Revision {
		t.Fatalf("stale retry = %+v, %v", replay, err)
	}
	if replay, err := k.Publish(t.Context(), ref, oldDraft, key("old")); err != nil || replay.Status != provider.AlreadyApplied || replay.Revision != changed.Revision {
		t.Fatalf("old retry after update = %+v, %v", replay, err)
	}
	if git(t, f.origin, "show-ref") != before {
		t.Fatal("replay or stale update changed Git refs")
	}
	ref.ID = "new-legacy"
	v1 := provider.DocumentDraft{Title: "Legacy"}
	created, err := legacy.Publish(t.Context(), ref, v1, key("create-legacy"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Publish(t.Context(), ref, provider.DocumentDraft{Title: "Revised"}, provider.WriteOptions{ExpectedRevision: created.Revision}); err != nil {
		t.Fatal(err)
	}
	v1Path := cfg.Directory + "/document-" + ref.StorageKey() + ".md"
	if raw := git(t, f.origin, "show", "trunk:"+v1Path); !strings.Contains(raw, "kind: document") || strings.Contains(raw, "x-devflow-") {
		t.Fatalf("legacy encoding changed: %s", raw)
	}
}

func TestWriterOKFReviewRecovery(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.cfg.Publication = "pull-request"
	k := repositoryWriter(t, f.cfg, "okf")
	ref := provider.Ref{Instance: f.cfg.Instance, Scope: "docs", ID: "how-to"}
	draft := provider.DocumentDraft{Title: "Guide", Description: "Start here", Body: "Steps", Links: []provider.Link{{Ref: &ref}}}
	base := git(t, f.origin, "rev-parse", "trunk")
	pushes := 0
	k.a.push = func(c checkout, ctx context.Context, commit, branch string) error {
		pushes++
		if err := c.push(ctx, commit, branch); err != nil {
			t.Fatal(err)
		}
		return errors.New("lost push acknowledgement")
	}
	calls := 0
	lost := errors.New("lost PR acknowledgement")
	k.a.Publisher = reviewPublisher(func(Publication) (string, error) {
		calls++
		return "", lost
	})
	pending, err := k.Publish(t.Context(), ref, draft, key("review"))
	if !errors.Is(err, lost) || pending.Status != provider.PendingReview || pushes != 1 || calls != 1 {
		t.Fatalf("interrupted review = %+v, %v (%d pushes, %d calls)", pending, err, pushes, calls)
	}
	head := git(t, f.origin, "rev-parse", pending.Branch)
	path := git(t, f.origin, "diff", "--name-only", base, head)
	raw := git(t, f.origin, "show", head+":"+path)
	c, err := okf.Parse([]byte(raw))
	if err != nil || c.Type != "Document" || c.Body != draft.Body || c.Description != draft.Description || !strings.Contains(raw, "x-devflow-links:") {
		t.Fatalf("review blob = %s, %v", raw, err)
	}
	f.cfg.Publisher = reviewPublisher(func(p Publication) (string, error) {
		if p.Head != pending.Branch || p.Base != "trunk" || p.Ref != ref {
			t.Fatalf("reconciled another review: %+v", p)
		}
		return "https://example.invalid/pr/1", nil
	})
	restarted := repositoryWriter(t, f.cfg, "legacy")
	replay, err := restarted.Publish(t.Context(), ref, draft, key("review"))
	if err != nil || replay.Status != provider.PendingReview || replay.URL == "" || replay.Revision != pending.Revision {
		t.Fatalf("recovered PR = %+v, %v", replay, err)
	}
	different := draft
	different.Description = "Another request"
	if _, err := restarted.Publish(t.Context(), ref, different, key("review")); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("changed review payload = %v", err)
	}
	if git(t, f.origin, "rev-parse", "trunk") != base || git(t, f.origin, "rev-parse", pending.Branch) != head {
		t.Fatal("PR replay altered base or head")
	}
	git(t, f.origin, "update-ref", "refs/heads/trunk", head, base)
	replay, err = restarted.Publish(t.Context(), ref, draft, key("review"))
	if err != nil || replay.Status != provider.AlreadyApplied || replay.Revision != pending.Revision {
		t.Fatalf("accepted PR replay = %+v, %v", replay, err)
	}
}

func TestWriterOccupiedGitDestination(t *testing.T) {
	t.Parallel()
	f := setup(t)
	ref := provider.Ref{Instance: f.cfg.Instance, Scope: "team", ID: "../opaque"}
	draft := provider.DocumentDraft{Title: "Guide"}
	// Resolve the writer's chosen name in a private proposal.
	cfg := f.cfg
	cfg.Publication = "pull-request"
	pending, err := repositoryWriter(t, cfg, "okf").Publish(t.Context(), ref, draft, key("probe"))
	if err != nil {
		t.Fatal(err)
	}
	path := git(t, f.origin, "diff", "--name-only", "trunk", pending.Branch)
	// Model a symlink in the tree and keep unrelated engineer bytes at exactly
	// that path in the working directory.
	blob := git(t, f.engineer, "hash-object", "-w", "notes.txt")
	git(t, f.engineer, "update-index", "--add", "--cacheinfo", "120000", blob, path)
	git(t, f.engineer, "commit", "-m", "reserve destination")
	git(t, f.engineer, "push", f.origin, "HEAD:refs/heads/trunk")
	local := filepath.Join(f.engineer, path)
	if err := os.MkdirAll(filepath.Dir(local), 0700); err != nil {
		t.Fatal(err)
	}
	put(t, local, "local engineer content")
	before := git(t, f.origin, "show-ref")
	if _, err := repositoryWriter(t, f.cfg, "okf").Publish(t.Context(), ref, draft, key("new")); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("publish over skipped Git symlink = %v", err)
	}
	if raw, err := os.ReadFile(local); err != nil || string(raw) != "local engineer content" || git(t, f.origin, "show-ref") != before {
		t.Fatalf("occupied destination changed: %s, %v", raw, err)
	}
}
