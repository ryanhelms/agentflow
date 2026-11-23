package provider

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestTaskRefs(t *testing.T) {
	ref := Ref{Instance: "github-issues", Scope: "nscaledev/devflow", ID: "42"}
	body := "feat: add adapter (#57)\n\nCompare #31 for background.\n\n" + TaskTrailer + ref.String() + "\n"
	refs, err := TaskRefs(body + TaskTrailer + ref.String() + "\r\n")
	if err != nil || !slices.Equal(refs, []Ref{ref}) {
		t.Fatalf("task refs = %v, %v", refs, err)
	}
	refs, err = TaskRefs("feat: add adapter (#57)\n")
	if err != nil || len(refs) != 0 {
		t.Fatalf("squash subject resolved as a task: %v, %v", refs, err)
	}
	if _, err := TaskRefs("Task-Ref: #42"); err == nil {
		t.Fatal("accepted an unqualified reference")
	}
}

func TestReferenceRoundTripAndStorageKey(t *testing.T) {
	ref := Ref{Instance: "tracker", Scope: "org/my repository", ID: "../%/â?+"}
	got, err := ParseRef(ref.String())
	if err != nil || got != ref {
		t.Fatalf("round trip = %v, %v", got, err)
	}
	other := ref
	other.Scope = "different org/my repository"
	if len(ref.StorageKey()) != 64 || ref.StorageKey() == other.StorageKey() {
		t.Fatal("storage key is not scoped")
	}
	for _, text := range []string{"tracker//42", "tracker/scope/%0A", "tracker/scope/%", "tracker/scope/foo/bar"} {
		if _, err := ParseRef(text); err == nil {
			t.Errorf("accepted invalid reference %q", text)
		}
	}
}

type fakeTasks map[Ref]Task

func (f fakeTasks) Get(_ context.Context, ref Ref) (Task, error) {
	task, ok := f[ref]
	if !ok {
		return Task{}, ErrNotFound
	}
	return task, nil
}

func (f fakeTasks) Find(_ context.Context, query TaskQuery) ([]Task, error) {
	var result []Task
	for ref, task := range f {
		if ref.Scope == query.Scope && (!query.OpenOnly || !task.State.Terminal) {
			result = append(result, task)
		}
	}
	return result, nil
}

func (f fakeTasks) Create(_ context.Context, draft TaskDraft, options WriteOptions) (WriteResult, error) {
	ref := Ref{Instance: "fake", Scope: draft.Scope, ID: options.OperationID}
	if _, ok := f[ref]; ok {
		return WriteResult{Ref: ref, Status: AlreadyApplied}, nil
	}
	f[ref] = Task{Ref: ref, Title: draft.Title}
	return WriteResult{Ref: ref, Status: Applied}, nil
}

func (f fakeTasks) Transition(_ context.Context, ref Ref, target TaskState, _ WriteOptions) (WriteResult, error) {
	task, ok := f[ref]
	if !ok {
		return WriteResult{}, ErrNotFound
	}
	if task.State == target {
		return WriteResult{Ref: ref, Status: AlreadyApplied}, nil
	}
	task.State = target
	f[ref] = task
	return WriteResult{Ref: ref, Status: Applied}, nil
}

func TestTaskContract(t *testing.T) {
	testTaskContract(t, fakeTasks{})
}

func testTaskContract(t *testing.T, tasks Tasks) {
	t.Helper()
	ctx := t.Context()
	draft := TaskDraft{Scope: "project", Title: "Add a provider"}
	options := WriteOptions{OperationID: "create-test"}
	created, err := tasks.Create(ctx, draft, options)
	if err != nil || created.Status != Applied {
		t.Fatalf("create = %v, %v", created, err)
	}
	if replay, err := tasks.Create(ctx, draft, options); err != nil || replay.Status != AlreadyApplied || replay.Ref != created.Ref {
		t.Fatalf("replay = %v, %v", replay, err)
	}
	if task, err := tasks.Get(ctx, created.Ref); err != nil || task.Title != draft.Title {
		t.Fatalf("get = %v, %v", task, err)
	}
	target := TaskState{ID: "done", Name: "Done", Terminal: true}
	if updated, err := tasks.Transition(ctx, created.Ref, target, WriteOptions{}); err != nil || updated.Status != Applied {
		t.Fatalf("transition = %v, %v", updated, err)
	}
	if got, err := tasks.Find(ctx, TaskQuery{Scope: draft.Scope, OpenOnly: true}); err != nil || len(got) != 0 {
		t.Fatalf("find open = %v, %v", got, err)
	}
	if !errors.Is(Unsupported("fake", "sprint reporting"), ErrUnsupported) {
		t.Fatal("missing unsupported capability error")
	}
}

func TestDocumentMetadataJSON(t *testing.T) {
	for _, doc := range []Document{{}, {Type: "ADR", Description: "Decision", Tags: []string{"eng"}}} {
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{"type": `"ADR"`, "description": `"Decision"`, "tags": `["eng"]`} {
			if doc.Type == "" {
				want = ""
			}
			if got := string(fields[key]); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
	}
}
