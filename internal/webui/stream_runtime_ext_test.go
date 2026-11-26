package webui

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type recordingExtension struct {
	name  string
	fail  bool
	calls *[]string
}

func (x *recordingExtension) start(*eventRuntime) error {
	*x.calls = append(*x.calls, x.name+".start")
	if x.fail {
		return errors.New("unavailable")
	}
	return nil
}

func (x *recordingExtension) attached(_ *repoEvents, w registeredWorktree) {
	*x.calls = append(*x.calls, x.name+".attached:"+w.ID)
}

func (x *recordingExtension) stop() { *x.calls = append(*x.calls, x.name+".stop") }

// Phase 2 lanes start their sources through runtimeExtensions. A failing
// extension is skipped; the others start, see every attached worktree and
// stop in reverse order.
func TestEventRuntimeRunsExtensions(t *testing.T) {
	dir := eventRepo(t)
	ctx := context.Background()
	r, _, err := inspectWorktree(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	old := runtimeExtensions
	t.Cleanup(func() { runtimeExtensions = old })
	runtimeExtensions = []func() runtimeExtension{
		func() runtimeExtension { return &recordingExtension{name: "a", calls: &calls} },
		func() runtimeExtension { return &recordingExtension{name: "broken", fail: true, calls: &calls} },
		func() runtimeExtension { return &recordingExtension{name: "b", calls: &calls} },
	}
	rt, stop, err := startEventRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.attach(r); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.attach(r); err != nil { // already attached: no second call
		t.Fatal(err)
	}
	stop()
	want := []string{"a.start", "broken.start", "b.start", "a.attached:main", "b.attached:main", "b.stop", "a.stop"}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}
