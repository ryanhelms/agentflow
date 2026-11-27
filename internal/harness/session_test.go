package harness_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/harness/harnesstest"
)

func TestLocateNeedsAnOKProbe(t *testing.T) {
	dir := harnesstest.Bin(t, "fake-agent", "1.0.0")
	home := harnesstest.PATH(t, dir)
	harnesstest.WriteSettings(t, home, harnesstest.Settings{Overrides: map[string]harnessdef.Definition{
		"demo": {Name: "Demo", Command: "fake-agent"},
	}})
	ctx := context.Background()

	f, h, err := harness.Locate(ctx, home, "test", nil, "demo")
	if !errors.Is(err, harness.ErrNotReady) {
		t.Fatalf("unprobed: err = %v, harness %+v", err, h)
	}
	if f.Path == "" || f.Identity == "" {
		t.Fatalf("found = %+v", f)
	}
	if h.Probe.Status != harness.ProbeNone {
		t.Errorf("Locate probed: %+v", h.Probe)
	}

	// A failed probe for this identity is still not ready.
	store := harness.NewFileStore(home)
	rec := harness.ProbeRecord{ID: "demo", Identity: f.Identity, Status: harness.ProbeFailed, Code: harness.CodeProtocol, ProbedAt: time.Now()}
	if err := store.Put(rec); err != nil {
		t.Fatal(err)
	}
	if _, _, err := harness.Locate(ctx, home, "test", nil, "demo"); !errors.Is(err, harness.ErrNotReady) {
		t.Fatalf("failed probe: err = %v", err)
	}

	rec.Status, rec.Code = harness.ProbeOK, harness.CodeOK
	rec.ACP = &harness.ACPInfo{ProtocolVersion: 1}
	if err := store.Put(rec); err != nil {
		t.Fatal(err)
	}
	f2, h2, err := harness.Locate(ctx, home, "test", nil, "demo")
	if err != nil {
		t.Fatalf("ok probe: %v (%+v)", err, h2)
	}
	if f2.Identity != f.Identity || h2.State != harness.StateNative || !harness.Ready(h2) {
		t.Errorf("located %+v / %+v", f2, h2)
	}

	// A record for another identity does not count.
	harnesstest.WriteSettings(t, home, harnesstest.Settings{Overrides: map[string]harnessdef.Definition{
		"demo": {Name: "Demo", Command: "fake-agent", Args: []string{"--acp"}},
	}})
	if _, _, err := harness.Locate(ctx, home, "test", nil, "demo"); !errors.Is(err, harness.ErrNotReady) {
		t.Fatalf("other identity: err = %v", err)
	}
	if _, _, err := harness.Locate(ctx, home, "test", nil, "nope"); !errors.Is(err, harness.ErrNotFound) {
		t.Fatalf("unknown: err = %v", err)
	}
	harnesstest.WriteSettings(t, home, harnesstest.Settings{Disabled: []string{"demo"}, Overrides: map[string]harnessdef.Definition{
		"demo": {Name: "Demo", Command: "fake-agent"},
	}})
	if _, _, err := harness.Locate(ctx, home, "test", nil, "demo"); !errors.Is(err, harness.ErrNotFound) {
		t.Fatalf("disabled: err = %v", err)
	}
}

func TestGuardPathRefusesRealPaths(t *testing.T) {
	if err := harness.GuardPath("/bin/sh"); !errors.Is(err, harness.ErrTestGuard) {
		t.Fatalf("GuardPath(/bin/sh) = %v", err)
	}
	if err := harness.GuardPath(t.TempDir()); err != nil {
		t.Fatalf("GuardPath(temp) = %v", err)
	}
}
