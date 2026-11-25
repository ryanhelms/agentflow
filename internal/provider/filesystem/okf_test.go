package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/provider"
)

func compatibleKnowledge(t *testing.T, instance, dir string) *Knowledge {
	t.Helper()
	k, err := NewOKFCompatibleKnowledge(instance, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	return k
}

func writeBundleFile(t *testing.T, dir, name, data string) string {
	t.Helper()
	file := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

const managedConcept = "---\ntype: Reference\nx-agentflow-ref: {scope: engineering, id: provider-architecture}\ntitle: Architecture\n---\nBody.\n"

func TestOKFBundleDiscovery(t *testing.T) {
	dir := t.TempDir()
	k := compatibleKnowledge(t, instance, dir)
	const minimal = "---\ntype: Any\n---\n# Body\r\n[link](../../outside.md)\n"
	for _, name := range []string{"engineering/sub/100% ready.md", "root.md", "default/root.md",
		"index.md", "log.md", "sub/index.md", "sub/log.md", ".hidden.md", ".git/inside.md",
		"sub/.hidden/inside.md", "sub/.hidden.md", "asset.txt", "upper.MD", "sub/upper.MD"} {
		writeBundleFile(t, dir, name, minimal)
	}
	writeBundleFile(t, dir, "AGENTS.md", "# Ordinary Markdown")
	writeBundleFile(t, dir, "sub/README.md", "---\ntitle: Ordinary\n---\nBody")
	writeBundleFile(t, dir, "sub/asset.bin", "\xff")
	old := writeBundleFile(t, dir, "other/managed.md", managedConcept)
	outside := t.TempDir()
	target := writeBundleFile(t, outside, "outside.md", minimal)
	for src, dst := range map[string]string{target: "escape.md", outside: "linked"} {
		if err := os.Symlink(src, filepath.Join(dir, dst)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	want := []string{"default/root", "engineering/sub/100% ready", "provider-architecture", "root"}
	for range 2 {
		docs, err := k.Find(t.Context(), provider.DocumentQuery{})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, doc := range docs {
			ids = append(ids, doc.Ref.ID)
			if doc.Ref.ID == "provider-architecture" {
				continue
			}
			scope := "default"
			if strings.HasPrefix(doc.Ref.ID, "engineering/") {
				scope = "engineering"
			}
			ref, err := provider.ParseRef(doc.Ref.String())
			if err != nil || ref != doc.Ref || ref.Instance != instance || ref.Scope != scope ||
				doc.Title != filepath.Base(doc.Ref.ID)+".md" || doc.Revision != digest([]byte(minimal)) ||
				doc.Body != "# Body\r\n[link](../../outside.md)\n" {
				t.Fatalf("derived document = %+v, %v", doc, err)
			}
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
	if err := os.Rename(old, filepath.Join(dir, "renamed.md")); err != nil {
		t.Fatal(err)
	}
	for _, runtimeInstance := range []string{"filesystem", "repository"} {
		ref := provider.Ref{Instance: runtimeInstance, Scope: "engineering", ID: "provider-architecture"}
		runtime := compatibleKnowledge(t, runtimeInstance, dir)
		doc, err := runtime.Get(t.Context(), ref)
		if err != nil || doc.Ref != ref || doc.Title != "Architecture" || doc.Revision != digest([]byte(managedConcept)) {
			t.Fatalf("renamed concept = %+v, %v", doc, err)
		}
	}
}

// A frozen v1 receipt for the request below, including the original instance.
// Computing this from today's serializer would miss a compatibility regression.
const legacyPublishHash = "67bf72b1db485dd9aa273c4be94ef04c14494dd156375d6267fbbda654c8a79f"

func TestOKFMixedLegacyPublication(t *testing.T) {
	dir := t.TempDir()
	k := compatibleKnowledge(t, instance, dir)
	ref := provider.Ref{Instance: instance, Scope: "engineering", ID: "provider-architecture"}
	draft := provider.DocumentDraft{Title: "Architecture", Body: "Body.\n"}
	opts := provider.WriteOptions{OperationID: "published"}
	writeBundleFile(t, dir, "other/guide.md", "---\ntype: Topic\n---\n")
	first, err := k.Publish(t.Context(), ref, draft, opts)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "document-"+ref.StorageKey()+".md")
	data, err := os.ReadFile(legacy)
	if err != nil || !strings.Contains(string(data), "published: "+legacyPublishHash) {
		t.Fatalf("v1 publish = %q, %v", data, err)
	}
	// V1 identities survive renames, including names reserved only by OKF.
	renamed := filepath.Join(dir, "index.md")
	if err := os.Rename(legacy, renamed); err != nil {
		t.Fatal(err)
	}
	read, err := k.Get(t.Context(), ref)
	if err != nil || read.Body != draft.Body || read.Revision != first.Revision {
		t.Fatalf("read v1 = %+v, %v", read, err)
	}
	updated, err := k.Publish(t.Context(), ref, provider.DocumentDraft{Title: "Edited"}, provider.WriteOptions{ExpectedRevision: read.Revision})
	if err != nil || updated.Revision == read.Revision {
		t.Fatalf("update v1 = %+v, %v", updated, err)
	}
	if replay, err := k.Publish(t.Context(), ref, draft, opts); err != nil || replay.Status != provider.AlreadyApplied || replay.Revision != updated.Revision {
		t.Fatalf("legacy replay = %+v, %v", replay, err)
	}

	// Stand in for a future migration performed outside of the provider.
	if err := os.Remove(renamed); err != nil {
		t.Fatal(err)
	}
	raw := strings.Replace(managedConcept, "title:", "x-agentflow-operations: {published: "+legacyPublishHash+"}\ntitle:", 1)
	file := writeBundleFile(t, dir, "adopted/renamed.md", raw)
	if replay, err := k.Publish(t.Context(), ref, draft, opts); err != nil || replay.Status != provider.AlreadyApplied || replay.Revision != digest([]byte(raw)) {
		t.Fatalf("migrated receipt replay = %+v, %v", replay, err)
	}
	for _, expected := range []string{"", first.Revision, digest([]byte(raw))} {
		if _, err := k.Publish(t.Context(), ref, draft, provider.WriteOptions{OperationID: "new", ExpectedRevision: expected}); !errors.Is(err, provider.ErrUnsupported) {
			t.Fatalf("OKF replacement = %v", err)
		}
	}
	if _, err := k.Publish(t.Context(), ref, provider.DocumentDraft{Title: "Different"}, opts); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("wrong replay payload = %v", err)
	}
	ref.ID = "new"
	if _, err := k.Publish(t.Context(), ref, draft, opts); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("reused receipt across records = %v", err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != raw || len(files(t, dir)) != 0 {
		t.Fatalf("changed bundle: %q, %v", got, err)
	}
}

func TestOKFAmbiguousAndInvalidIdentity(t *testing.T) {
	for _, tc := range []struct{ name, data string }{
		{"managed vs derived", "---\ntype: Article\nx-agentflow-ref: {scope: eng, id: eng/a}\n---\n"},
		{"v1 vs derived", "---\nversion: 1\nkind: document\ninstance: local\nscope: eng\nid: eng/a\ntitle: Legacy\n---\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeBundleFile(t, dir, "eng/a.md", "---\ntype: Article\n---\n")
			writeBundleFile(t, dir, "b.md", tc.data)
			k := compatibleKnowledge(t, instance, dir)
			if _, err := k.Find(t.Context(), provider.DocumentQuery{Limit: 1}); !errors.Is(err, provider.ErrConflict) {
				t.Fatalf("ambiguous Find = %v", err)
			}
			ref := provider.Ref{Instance: instance, Scope: "eng", ID: "eng/a"}
			if _, err := k.Publish(t.Context(), ref, provider.DocumentDraft{Title: "Replacement"}, provider.WriteOptions{}); !errors.Is(err, provider.ErrConflict) {
				t.Fatalf("ambiguous publish = %v", err)
			}
		})
	}
	for _, ext := range []string{"x-agentflow-ref: []", "x-agentflow-ref: {scope: a}", "x-agentflow-ref: {scope: a, id: 1}",
		"x-agentflow-ref: {scope: a, id: b, instance: spoof}", "x-agentflow-operations: []", "x-agentflow-operations: {op: broken}"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			writeBundleFile(t, dir, "broken.md", "---\ntype: A\n"+ext+"\n---\n")
			k := compatibleKnowledge(t, instance, dir)
			if _, err := k.Find(t.Context(), provider.DocumentQuery{}); err == nil || !strings.Contains(err.Error(), "broken.md") {
				t.Fatalf("bad extension accepted: %v", err)
			}
		})
	}
}

func TestOKFMixedPreservesDamagedV1(t *testing.T) {
	dir := t.TempDir()
	k := compatibleKnowledge(t, instance, dir)
	raw := "---\nversion: 1\nkind: document\ninstance: local\nscope: eng\nid: doc\ntitle: Doc\noperations: {saved: broken}\n---\n"
	name := writeBundleFile(t, dir, "renamed.MD", raw)
	ref := provider.Ref{Instance: instance, Scope: "eng", ID: "doc"}
	if _, err := k.Publish(t.Context(), ref, provider.DocumentDraft{Title: "New"}, provider.WriteOptions{}); err == nil || !strings.Contains(err.Error(), "invalid operation receipt") {
		t.Fatalf("publish over damaged v1 = %v", err)
	}
	if got, err := os.ReadFile(name); err != nil || string(got) != raw || len(files(t, dir)) > 1 {
		t.Fatalf("damaged v1 changed: %q, %v", got, err)
	}
}

func TestTaskStoreStaysFlatAndTypedTasksStayTasks(t *testing.T) {
	dir := t.TempDir()
	writeBundleFile(t, dir, "nested/guide.md", managedConcept)
	tasks := tasksAt(t, dir)
	created, err := tasks.Create(t.Context(), provider.TaskDraft{Scope: "eng", Title: "Plan", Type: "story"}, provider.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Get(t.Context(), created.Ref)
	if err != nil || task.Type != "story" {
		t.Fatalf("flat task = %+v, %v", task, err)
	}
	docs, err := compatibleKnowledge(t, instance, dir).Find(t.Context(), provider.DocumentQuery{})
	if err != nil || len(docs) != 1 || docs[0].Ref.ID != "provider-architecture" {
		t.Fatalf("mixed task and knowledge = %+v, %v", docs, err)
	}
}

func TestOKFNewLegacyPublishRespectsOccupiedFilename(t *testing.T) {
	dir := t.TempDir()
	ref := provider.Ref{Instance: instance, Scope: "eng", ID: "doc"}
	const raw = "# An unrelated Markdown file.\n"
	file := writeBundleFile(t, dir, "document-"+ref.StorageKey()+".md", raw)
	k := compatibleKnowledge(t, instance, dir)
	if docs, err := k.Find(t.Context(), provider.DocumentQuery{}); err != nil || len(docs) != 0 {
		t.Fatalf("ordinary Markdown appeared in search: %+v, %v", docs, err)
	}
	if _, err := k.Publish(t.Context(), ref, provider.DocumentDraft{Title: "Publish"}, provider.WriteOptions{}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("occupied v1 filename = %v", err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != raw {
		t.Fatalf("occupied file changed: %q, %v", got, err)
	}
}
