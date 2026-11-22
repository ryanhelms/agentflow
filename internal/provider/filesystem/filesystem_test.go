package filesystem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nscaledev/devflow/internal/provider"
)

const instance = "local"

func tasksAt(t *testing.T, dir string) *Tasks {
	t.Helper()
	s, err := NewTasks(instance, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func docsAt(t *testing.T, dir string) *Knowledge {
	t.Helper()
	s, err := NewKnowledge(instance, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func onlyFile(t *testing.T, dir string) string {
	t.Helper()
	found := files(t, dir)
	if len(found) != 1 {
		t.Fatalf("expected one record, found %v", found)
	}
	return found[0]
}

func TestTaskLifecycle(t *testing.T) {
	dir := t.TempDir()
	a, b := tasksAt(t, dir), tasksAt(t, dir)
	ctx := t.Context()
	draft := provider.TaskDraft{
		Scope: "infra/ops", Title: "Schedule rollout", Description: "Start here\r\n---\nEnd",
		Type: "story", Priority: "high", Labels: []string{"deploy"},
		Links: []provider.Link{{Title: "context", Ref: &provider.Ref{Instance: "wiki", Scope: "runbooks", ID: "1"}}},
	}
	createOpts := provider.WriteOptions{OperationID: "issue/123"}
	created, err := a.Create(ctx, draft, createOpts)
	if err != nil || created.Status != provider.Applied {
		t.Fatalf("create = %+v, %v", created, err)
	}
	newName := filepath.Join(dir, "rollout.md")
	if err := os.Rename(onlyFile(t, dir), newName); err != nil {
		t.Fatal(err)
	}
	got, err := b.Get(ctx, created.Ref)
	if err != nil || got.Title != draft.Title || got.Description != draft.Description || got.Type != draft.Type ||
		got.Priority != draft.Priority || !reflect.DeepEqual(got.Links, draft.Links) || got.State.ID != "open" || got.Revision != created.Revision {
		t.Fatalf("get renamed task = %+v, %v", got, err)
	}
	found, err := a.Find(ctx, provider.TaskQuery{Text: "START", Scope: draft.Scope, OpenOnly: true, Limit: 1})
	if err != nil || len(found) != 1 || found[0].Ref != created.Ref {
		t.Fatalf("find = %+v, %v", found, err)
	}
	done := provider.TaskState{ID: "done", Name: "Done", Terminal: true}
	changeOpts := provider.WriteOptions{OperationID: "finish", ExpectedRevision: created.Revision}
	changed, err := b.Transition(ctx, created.Ref, done, changeOpts)
	if err != nil || changed.Revision == created.Revision {
		t.Fatalf("transition = %+v, %v", changed, err)
	}
	if _, err := a.Transition(ctx, created.Ref, got.State, provider.WriteOptions{ExpectedRevision: created.Revision}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("stale transition = %v", err)
	}
	if found, err := b.Find(ctx, provider.TaskQuery{OpenOnly: true}); err != nil || len(found) != 0 {
		t.Fatalf("find open = %v, %v", found, err)
	}
	if replay, err := a.Create(ctx, draft, createOpts); err != nil || replay.Status != provider.AlreadyApplied || replay.Ref != created.Ref || replay.Revision != changed.Revision {
		t.Fatalf("create retry = %+v, %v", replay, err)
	}
	if _, err := a.Transition(ctx, created.Ref, got.State, provider.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if replay, err := b.Transition(ctx, created.Ref, done, changeOpts); err != nil || replay.Status != provider.AlreadyApplied {
		t.Fatalf("transition retry = %+v, %v", replay, err)
	}
	now, err := b.Get(ctx, created.Ref)
	if err != nil || now.State != got.State {
		t.Fatalf("retry changed state: %+v, %v", now, err)
	}
	draft.Title = "different"
	if _, err := b.Create(ctx, draft, createOpts); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("reused key = %v", err)
	}
	raw, err := os.ReadFile(newName)
	if err != nil || !bytes.Contains(raw, []byte("deploy")) || onlyFile(t, dir) != newName {
		t.Fatalf("updated task at renamed path = %s, %v", raw, err)
	}
	if _, err := a.Find(ctx, provider.TaskQuery{AssignedToMe: true}); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("assigned filter = %v", err)
	}
}

func TestPublishRenameRevisionAndRetry(t *testing.T) {
	dir := t.TempDir()
	a, b := docsAt(t, dir), docsAt(t, dir)
	ctx := t.Context()
	ref := provider.Ref{Instance: instance, Scope: "org/../../ops", ID: "../design?%"}
	draft := provider.DocumentDraft{Title: "Architecture", Body: "---\n# Diagram\r\n", Links: []provider.Link{{URL: "urn:other"}}}
	opts := provider.WriteOptions{OperationID: "publish-one"}
	if _, err := a.Publish(ctx, ref, draft, provider.WriteOptions{ExpectedRevision: "missing"}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("missing expected target = %v", err)
	}
	created, err := a.Publish(ctx, ref, draft, opts)
	if err != nil || created.Status != provider.Applied {
		t.Fatalf("publish = %+v, %v", created, err)
	}
	name := filepath.Join(dir, "diagram.md")
	if err := os.Rename(onlyFile(t, dir), name); err != nil {
		t.Fatal(err)
	}
	got, err := b.Get(ctx, ref)
	if err != nil || got.Ref != ref || got.Body != draft.Body || !reflect.DeepEqual(got.Links, draft.Links) || got.Revision != created.Revision {
		t.Fatalf("renamed document = %+v, %v", got, err)
	}
	next := provider.DocumentDraft{Title: "New design", Body: "Second edition"}
	if _, err := b.Publish(ctx, ref, next, provider.WriteOptions{}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("unconditional replacement = %v", err)
	}
	if _, err := b.Publish(ctx, ref, next, opts); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("reused operation = %v", err)
	}
	updateOpts := provider.WriteOptions{OperationID: "revise", ExpectedRevision: got.Revision}
	updated, err := b.Publish(ctx, ref, next, updateOpts)
	if err != nil || updated.Revision == got.Revision {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if replay, err := a.Publish(ctx, ref, next, updateOpts); err != nil || replay.Status != provider.AlreadyApplied || replay.Revision != updated.Revision {
		t.Fatalf("update retry = %+v, %v", replay, err)
	}
	if replay, err := a.Publish(ctx, ref, draft, opts); err != nil || replay.Status != provider.AlreadyApplied {
		t.Fatalf("original retry = %+v, %v", replay, err)
	}
	if _, err := a.Publish(ctx, ref, draft, provider.WriteOptions{ExpectedRevision: got.Revision}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("stale publish = %v", err)
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, append(raw, []byte("\nmanual edit")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Publish(ctx, ref, draft, provider.WriteOptions{ExpectedRevision: updated.Revision}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("stale after human edit = %v", err)
	}
	found, err := a.Find(ctx, provider.DocumentQuery{Scope: ref.Scope, Text: "MANUAL", Limit: 1})
	if err != nil || len(found) != 1 || found[0].Body != next.Body+"\nmanual edit" || found[0].Revision == updated.Revision {
		t.Fatalf("manual edit = %+v, %v", found, err)
	}
}

func TestScopedSharedDirectory(t *testing.T) {
	dir := t.TempDir()
	ctx := t.Context()
	ts, ks := tasksAt(t, dir), docsAt(t, dir)
	created, err := ts.Create(ctx, provider.TaskDraft{Title: "Task", Scope: "alpha"}, provider.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ref := created.Ref
	for _, scope := range []string{"alpha", "beta"} {
		ref.Scope = scope
		if _, err := ks.Publish(ctx, ref, provider.DocumentDraft{Title: "Doc in " + scope}, provider.WriteOptions{OperationID: "one"}); err != nil {
			t.Fatal(err)
		}
	}
	if docs, err := ks.Find(ctx, provider.DocumentQuery{Scope: "beta"}); err != nil || len(docs) != 1 || docs[0].Ref != ref {
		t.Fatalf("scoped documents = %v, %v", docs, err)
	}
	if docs, err := ks.Find(ctx, provider.DocumentQuery{}); err != nil || len(docs) != 2 {
		t.Fatalf("all documents = %v, %v", docs, err)
	}
	if _, err := ts.Get(ctx, ref); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("wrong scope = %v", err)
	}
	ref.ID = "different"
	if _, err := ks.Publish(ctx, ref, provider.DocumentDraft{Title: "Other"}, provider.WriteOptions{OperationID: "one"}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("same key new target = %v", err)
	}
	other, err := NewKnowledge("other", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if found, err := other.Find(ctx, provider.DocumentQuery{}); err != nil || len(found) != 0 {
		t.Fatalf("other instance = %v, %v", found, err)
	}
}

func TestMalformedInputDoesNotOverwrite(t *testing.T) {
	ctx := t.Context()
	ref := provider.Ref{Instance: instance, Scope: "scope", ID: "42"}
	good := "---\r\nversion: 1\r\nkind: document\r\ninstance: local\r\nscope: scope\r\nid: '42'\r\ntitle: Test\r\n---\r\nbody"
	for name, raw := range map[string]string{
		"no front matter":      "# user file\n",
		"unterminated":         "---\nversion: 1\n",
		"invalid YAML":         strings.Replace(good, "version: 1", "version: [", 1),
		"duplicate key":        strings.Replace(good, "title: Test", "title: first\ntitle: last", 1),
		"unknown field":        strings.Replace(good, "title: Test", "owner: me\ntitle: Test", 1),
		"missing id":           strings.Replace(good, "id: '42'", "id: ''", 1),
		"unsupported version":  strings.Replace(good, "version: 1", "version: 2", 1),
		"invalid body":         good + "\xff",
		"task pin on document": strings.Replace(good, "title: Test", "title: Test\ntemplate: {name: work, revision: '01'}", 1),
		"broken receipt":       strings.Replace(good, "title: Test", "title: Test\noperations: {op: wrong}", 1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s := docsAt(t, dir)
			file := filepath.Join(dir, "bad.md")
			if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := s.Publish(ctx, ref, provider.DocumentDraft{Title: "New"}, provider.WriteOptions{})
			if err == nil || !strings.Contains(err.Error(), "bad.md") {
				t.Fatalf("expected filename in error, got %v", err)
			}
			got, err := os.ReadFile(file)
			if err != nil || string(got) != raw || onlyFile(t, dir) != file {
				t.Fatalf("malformed file changed: %q, %v", got, err)
			}
		})
	}
	dir := t.TempDir()
	s := docsAt(t, dir)
	// Occupy the intended filename with a valid but unrelated record.
	path := filepath.Join(dir, "document-"+ref.StorageKey()+".md")
	if err := os.WriteFile(path, []byte(strings.Replace(good, "'42'", "other", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(ctx, ref, provider.DocumentDraft{Title: "Test"}, provider.WriteOptions{}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("destination collision = %v", err)
	}
	other := ref
	other.ID = "other"
	if doc, err := s.Get(ctx, other); err != nil || doc.Body != "body" {
		t.Fatalf("unrelated CRLF record = %+v, %v", doc, err)
	}
	for _, name := range []string{"one.md", "two.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(good), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Find(ctx, provider.DocumentQuery{Limit: 1}); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("duplicate ref = %v", err)
	}
}

func TestPathConfinement(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s := docsAt(t, dir)
	ctx := t.Context()
	ref := provider.Ref{Instance: instance, Scope: "../", ID: "../../outside"}
	draft := provider.DocumentDraft{Title: "Doc"}
	for _, root := range []string{"", filepath.Join(parent, "missing")} {
		if _, err := NewKnowledge(instance, root); err == nil {
			t.Fatalf("accepted invalid root %q", root)
		}
	}
	for _, bad := range []provider.Ref{{Instance: "other", Scope: "scope", ID: "42"}, {Instance: instance, Scope: "", ID: "42"}, {Instance: instance, Scope: "scope", ID: "bad\n"}} {
		if _, err := s.Publish(ctx, bad, draft, provider.WriteOptions{}); err == nil {
			t.Fatalf("accepted invalid ref %+v", bad)
		}
	}
	external := filepath.Join(parent, "outside")
	if err := os.WriteFile(external, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "redirect.md")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := s.Publish(ctx, ref, draft, provider.WriteOptions{}); err == nil {
		t.Fatal("accepted symlinked record")
	}
	if _, err := s.Get(ctx, ref); err == nil || errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("read symlink = %v", err)
	}
	if raw, err := os.ReadFile(external); err != nil || string(raw) != "untouched" {
		t.Fatalf("outside = %q, %v", raw, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTasks(instance, alias); err == nil {
		t.Fatal("accepted symlink root")
	}
	if _, err := s.Publish(ctx, ref, draft, provider.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if files := files(t, dir); len(files) != 1 {
		t.Fatalf("path-like ref produced %v", files)
	}
}

func TestConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	a, b := docsAt(t, dir), docsAt(t, dir)
	ctx := t.Context()
	ref := provider.Ref{Instance: instance, Scope: "scope", ID: "id"}
	first, err := a.Publish(ctx, ref, provider.DocumentDraft{Title: "Original"}, provider.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 8)
	for i := range cap(results) {
		adapter := []*Knowledge{a, b}[i%2]
		go func() {
			<-start
			_, err := adapter.Publish(ctx, ref, provider.DocumentDraft{Title: fmt.Sprint(i)}, provider.WriteOptions{ExpectedRevision: first.Revision})
			results <- err
		}()
	}
	close(start)
	var applied int
	for range cap(results) {
		err := <-results
		switch {
		case err == nil:
			applied++
		case errors.Is(err, provider.ErrConflict):
		default:
			t.Fatalf("unexpected write error: %v", err)
		}
	}
	if applied != 1 {
		t.Fatalf("%d writes succeeded with the same revision", applied)
	}

	ta, tb := tasksAt(t, dir), tasksAt(t, dir)
	var wg sync.WaitGroup
	receipts := make(chan provider.WriteResult, 8)
	for i := range cap(receipts) {
		adapter := []*Tasks{ta, tb}[i%2]
		wg.Go(func() {
			r, err := adapter.Create(ctx, provider.TaskDraft{Title: "Once", Scope: "scope"}, provider.WriteOptions{OperationID: "one request"})
			if err != nil {
				t.Error(err)
				return
			}
			receipts <- r
		})
	}
	wg.Wait()
	close(receipts)
	applied = 0
	for r := range receipts {
		if r.Status == provider.Applied {
			applied++
		}
	}
	got, err := ta.Find(ctx, provider.TaskQuery{})
	if err != nil || len(got) != 1 || applied != 1 {
		t.Fatalf("duplicate request: %d applied, %v, %v", applied, got, err)
	}
}

func TestInterruptedWrite(t *testing.T) {
	// The child dies holding the actual kernel lock with an incomplete stage.
	// The parent then retries using a fresh adapter.
	const helper = "DEVFLOW_FILESYSTEM_TEST_CHILD_ROOT"
	if dir := os.Getenv(helper); dir != "" {
		s := docsAt(t, dir)
		lock, err := s.lock(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		err = s.root.WriteFile(".devflow-stage-interrupted", []byte("---\nversion:"), 0600)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Print("ready\n")
		time.Sleep(time.Minute)
		return
	}
	dir := t.TempDir()
	a := docsAt(t, dir)
	ref := provider.Ref{Instance: instance, Scope: "scope", ID: "doc"}
	first, err := a.Publish(t.Context(), ref, provider.DocumentDraft{Title: "Before"}, provider.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInterruptedWrite$")
	cmd.Env = append(os.Environ(), helper+"="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	var ready [6]byte
	if _, err := io.ReadFull(stdout, ready[:]); err != nil || string(ready[:]) != "ready\n" {
		t.Fatalf("child readiness %q, %v", ready, err)
	}
	draft := provider.DocumentDraft{Title: "After"}
	opts := provider.WriteOptions{OperationID: "update", ExpectedRevision: first.Revision}
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	if _, err := a.Publish(short, ref, draft, opts); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("write while child holds lock = %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("expected interrupted process")
	}
	b := docsAt(t, dir)
	if got, err := b.Get(ctx, ref); err != nil || got.Title != "Before" || got.Revision != first.Revision {
		t.Fatalf("interrupted original = %+v, %v", got, err)
	}
	if updated, err := b.Publish(ctx, ref, draft, opts); err != nil || updated.Status != provider.Applied {
		t.Fatalf("retry after interrupted writer = %+v, %v", updated, err)
	}
	if replay, err := a.Publish(ctx, ref, draft, opts); err != nil || replay.Status != provider.AlreadyApplied {
		t.Fatalf("repeat = %+v, %v", replay, err)
	}
}

func TestReaderSeesWholeRevisions(t *testing.T) {
	dir := t.TempDir()
	writer, reader := docsAt(t, dir), docsAt(t, dir)
	ctx := t.Context()
	ref := provider.Ref{Instance: instance, Scope: "scope", ID: "shared"}
	bodies := []string{strings.Repeat("first\n", 8192), strings.Repeat("second\n", 8192)}
	draft := provider.DocumentDraft{Title: "version 0", Body: bodies[0]}
	current, err := writer.Publish(ctx, ref, draft, provider.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ready := make(chan struct{})
	done := make(chan struct{})
	wg.Go(func() {
		close(ready)
		for {
			doc, err := reader.Get(ctx, ref)
			if err != nil {
				t.Error(err)
				return
			}
			first := doc.Title == "version 0" && doc.Body == bodies[0]
			second := doc.Title == "version 1" && doc.Body == bodies[1]
			if !first && !second {
				t.Error("reader observed a partial revision")
				return
			}
			select {
			case <-done:
				return
			default:
			}
		}
	})
	defer func() { close(done); wg.Wait() }()
	<-ready
	for i := 1; i <= 12; i++ {
		draft.Title = fmt.Sprintf("version %d", i%2)
		draft.Body = bodies[i%2]
		current, err = writer.Publish(ctx, ref, draft, provider.WriteOptions{ExpectedRevision: current.Revision})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenedDirectoryStaysConfinedAfterRename(t *testing.T) {
	parent := t.TempDir()
	original := filepath.Join(parent, "data")
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	s := docsAt(t, original)
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Skipf("platform prevents moving an open directory: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, original); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	ref := provider.Ref{Instance: instance, Scope: "scope", ID: "id"}
	if _, err := s.Publish(t.Context(), ref, provider.DocumentDraft{Title: "Pinned"}, provider.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := files(t, moved); len(got) != 1 {
		t.Fatalf("pinned directory contains %v", got)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("substituted directory contains %v, %v", entries, err)
	}
}
