package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/okf"
)

func writer(t *testing.T, dir, format string) *Knowledge {
	t.Helper()
	k, err := NewOKFKnowledge(instance, dir, format)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	return k
}

func TestWriterFormatAndReplay(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewOKFKnowledge(instance, dir, ""); err == nil {
		t.Fatal("accepted empty format")
	}
	k, legacy := writer(t, dir, "okf"), writer(t, dir, "legacy")
	ref := provider.Ref{Instance: instance, Scope: "docs", ID: "guide"}
	opts := provider.WriteOptions{OperationID: "create"}
	draft := provider.DocumentDraft{Title: "New guide", Description: "Read me", Tags: []string{"2026", "true"},
		Links: []provider.Link{{Title: "Other", Ref: &ref, URL: "https://example.invalid"}}, Body: "a\r\nb\n"}
	created, err := k.Publish(t.Context(), ref, draft, opts)
	if err != nil {
		t.Fatal(err)
	}
	path := onlyFile(t, dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := okf.Parse(data)
	if err != nil || c.Type != "Document" || c.Body != draft.Body || c.Description != draft.Description || !slices.Equal(c.Tags, draft.Tags) {
		t.Fatalf("new OKF = %+v, %v", c, err)
	}
	doc, err := compatibleKnowledge(t, instance, dir).Get(t.Context(), ref)
	if err != nil || doc.Revision != digest(data) || doc.Revision != created.Revision || !reflect.DeepEqual(doc.Links, draft.Links) {
		t.Fatalf("compatible Get = %+v, %v", doc, err)
	}
	if replay, err := legacy.Publish(t.Context(), ref, draft, opts); err != nil || replay.Status != provider.AlreadyApplied {
		t.Fatalf("retry after format switch = %+v, %v", replay, err)
	}
	for _, mutate := range []func(*provider.DocumentDraft){
		func(d *provider.DocumentDraft) { d.Type = "Document" },
		func(d *provider.DocumentDraft) { d.Description = "Changed" },
		func(d *provider.DocumentDraft) { d.Tags = nil },
		func(d *provider.DocumentDraft) { d.Links = nil },
	} {
		changed := draft
		mutate(&changed)
		if _, err := k.Publish(t.Context(), ref, changed, opts); !errors.Is(err, provider.ErrConflict) {
			t.Fatalf("changed request %+v: %v", changed, err)
		}
	}
	clear := provider.DocumentDraft{Tags: []string{}, Links: []provider.Link{}}
	edit := provider.WriteOptions{OperationID: "clear", ExpectedRevision: doc.Revision}
	if _, err := legacy.Publish(t.Context(), ref, clear, edit); err != nil {
		t.Fatal(err)
	}
	for _, omitted := range []provider.DocumentDraft{{Tags: []string{}}, {Links: []provider.Link{}}} {
		if _, err := k.Publish(t.Context(), ref, omitted, edit); !errors.Is(err, provider.ErrConflict) {
			t.Fatalf("cleared vs omitted fields: %v", err)
		}
	}
	doc, err = k.Get(t.Context(), ref)
	if err != nil || doc.Type != "Document" || len(doc.Tags) != 0 || len(doc.Links) != 0 || doc.Body != draft.Body || doc.Description != draft.Description {
		t.Fatalf("read after clear = %+v, %v", doc, err)
	}
	ref.Scope = "other"
	first, err := legacy.Publish(t.Context(), ref, provider.DocumentDraft{Title: "V1"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	rev := provider.WriteOptions{ExpectedRevision: first.Revision}
	if _, err := k.Publish(t.Context(), ref, provider.DocumentDraft{Title: "Still V1", Type: "Playbook"}, rev); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("unsupported v1 metadata = %v", err)
	}
	if _, err := k.Publish(t.Context(), ref, provider.DocumentDraft{Title: "Still V1"}, rev); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "document-"+ref.StorageKey()+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if v1, err := parse(raw); err != nil || v1.Ref != ref || v1.Title != "Still V1" {
		t.Fatalf("legacy = %+v, %v", v1, err)
	}
}

func TestWriterNestedMigratedReceipt(t *testing.T) {
	dir := t.TempDir()
	k := writer(t, dir, "legacy")
	ref := provider.Ref{Instance: instance, Scope: "engineering", ID: "provider-architecture"}
	oldDraft := provider.DocumentDraft{Title: "Architecture", Body: "Body.\n"}
	const body = "# Manual\r\n\r\n[other](../other.md)"
	raw := "---\ntype: Reference\ntitle: &title Manual\ndescription: Kept\ntags: [eng]\n" +
		"x-agentflow-ref: {scope: engineering, id: provider-architecture}\n" +
		"x-agentflow-links: [{url: 'urn:previous', ref: {instance: tasks, scope: eng, id: work}}]\n" +
		"x-agentflow-operations: {published: " + legacyPublishHash + "}\n" +
		"sources: [{title: *title, extension: !vendor 'provenance'}] # comment\n---\n" + body
	path := writeBundleFile(t, dir, "moved/nested/article.md", raw)
	if err := os.Chmod(path, 0750); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := provider.WriteOptions{OperationID: "published", ExpectedRevision: "pre-migration"}
	if r, err := k.Publish(t.Context(), ref, oldDraft, oldKey); err != nil || r.Status != provider.AlreadyApplied || r.Revision != digest([]byte(raw)) {
		t.Fatalf("older v1 receipt = %+v, %v", r, err)
	}
	withMetadata := oldDraft
	withMetadata.Description = "Kept"
	if _, err := k.Publish(t.Context(), ref, withMetadata, oldKey); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("metadata with legacy receipt = %v", err)
	}
	opts := provider.WriteOptions{OperationID: "edit", ExpectedRevision: digest([]byte(raw))}
	edit := provider.DocumentDraft{Title: "Updated", Type: "Guide"}
	updated, err := k.Publish(t.Context(), ref, edit, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Publish(t.Context(), ref, edit, provider.WriteOptions{ExpectedRevision: opts.ExpectedRevision}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("stale update = %v", err)
	}
	doc, err := k.Get(t.Context(), ref)
	if err != nil || doc.Title != edit.Title || doc.Type != edit.Type || doc.Description != "Kept" || doc.Body != body ||
		!slices.Equal(doc.Tags, []string{"eng"}) || len(doc.Links) != 1 || doc.Links[0].Ref.ID != "work" || doc.Revision != updated.Revision {
		t.Fatalf("nested read = %+v, %v", doc, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || digest(data) != updated.Revision || !strings.Contains(string(data), "!vendor 'provenance'") || !strings.Contains(string(data), "# comment") {
		t.Fatalf("nested bytes = %s, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != original.Mode().Perm() {
		t.Fatalf("updated mode = %v, %v", info, err)
	}
	if r, err := k.Publish(t.Context(), ref, oldDraft, oldKey); err != nil || r.Status != provider.AlreadyApplied || r.Revision != updated.Revision {
		t.Fatalf("v1 replay after edit = %+v, %v", r, err)
	}
}

func TestWriterExclusiveOKFDestination(t *testing.T) {
	ref := provider.Ref{Instance: instance, Scope: "docs", ID: "log"}
	draft := provider.DocumentDraft{Title: "Document"}
	// Discover the chosen name without coupling this fixture to its spelling.
	first := t.TempDir()
	if _, err := writer(t, first, "okf").Publish(t.Context(), ref, draft, provider.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(onlyFile(t, first))
	for _, kind := range []string{"symlink", "markdown", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, name)
			switch kind {
			case "symlink":
				if err := os.Symlink(filepath.Join(dir, "missing"), file); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "markdown":
				writeBundleFile(t, dir, name, "# user file")
			case "directory":
				if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(file)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer(t, dir, "okf").Publish(t.Context(), ref, draft, provider.WriteOptions{}); !errors.Is(err, provider.ErrConflict) {
				t.Fatalf("occupied %s = %v", kind, err)
			}
			after, err := os.Lstat(file)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("occupied entry replaced: %v", err)
			}
		})
	}
}

func TestWriterAdoptsDerivedRefAndPinsOriginalFile(t *testing.T) {
	dir := t.TempDir()
	k := writer(t, dir, "okf")
	const raw = "---\ntype: Guide\n---\nbody"
	path := writeBundleFile(t, dir, "team/imported.md", raw)
	ref := provider.Ref{Instance: instance, Scope: "team", ID: "team/imported"}
	res, err := k.Publish(t.Context(), ref, provider.DocumentDraft{Description: "Imported"}, provider.WriteOptions{ExpectedRevision: digest([]byte(raw))})
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(dir, "team", "renamed.md")
	if err := os.Rename(path, newPath); err != nil {
		t.Fatal(err)
	}
	doc, err := k.Get(t.Context(), ref)
	if err != nil || doc.Ref != ref || doc.Description != "Imported" || doc.Body != "body" || doc.Revision != res.Revision {
		t.Fatalf("adopted concept = %+v, %v", doc, err)
	}

	// Swap a byte-identical file between the initial scan and final install.
	// A matching content digest alone would authorize replacing another inode.
	r, err := k.get(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newPath, filepath.Join(dir, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, bytes, r.mode); err != nil {
		t.Fatal(err)
	}
	lock, err := k.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := k.install(t.Context(), r, []byte("replaced"), lock); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("replaced a different inode: %v", err)
	}
	if after, err := os.ReadFile(newPath); err != nil || string(after) != string(bytes) {
		t.Fatalf("different file changed: %s, %v", after, err)
	}
}

func TestWriterRejectsSymlinkParentAtInstall(t *testing.T) {
	dir := t.TempDir()
	k := writer(t, dir, "okf")
	file := writeBundleFile(t, dir, "team/guides/item.md", managedConcept)
	r, err := k.read("team/guides/item.md")
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, "elsewhere")
	if err := os.Rename(filepath.Dir(file), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, filepath.Dir(file)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	lock, err := k.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := k.install(t.Context(), r, []byte("wrong destination"), lock); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("accepted redirected parent: %v", err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != managedConcept {
		t.Fatalf("redirected original = %s, %v", data, err)
	}
}

func TestWriterRejectsInvalidMetadataBeforeReplay(t *testing.T) {
	dir := t.TempDir()
	k := writer(t, dir, "okf")
	ref := provider.Ref{Instance: instance, Scope: "eng", ID: "example"}
	opts := provider.WriteOptions{OperationID: "create"}
	draft := provider.DocumentDraft{Title: "Valid", Description: "\uFFFD"}
	first, err := k.Publish(t.Context(), ref, draft, opts)
	if err != nil {
		t.Fatal(err)
	}
	draft.Description = "\xff"
	if _, err := k.Publish(t.Context(), ref, draft, opts); err == nil {
		t.Fatal("invalid metadata matched a valid receipt")
	}
	draft.Description = ""
	draft.Tags = []string{"\xff"}
	if _, err := k.Publish(t.Context(), ref, draft, provider.WriteOptions{ExpectedRevision: first.Revision}); err == nil {
		t.Fatal("wrote binary tag")
	}
	got, err := k.Get(t.Context(), ref)
	if err != nil || got.Revision != first.Revision {
		t.Fatalf("changed on invalid input: %+v, %v", got, err)
	}
}
