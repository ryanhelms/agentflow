package install_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/plugin/install"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// The sample plugins under examples/plugins are part of the product: the
// documentation tells people to add them. These tests add the real files.

const samples = "../../../examples/plugins"

func TestSampleCINotifyInstallsFromItsDirectory(t *testing.T) {
	home := t.TempDir()
	cleanup(t, home)
	plan := mustPlan(t, home, filepath.Join(samples, "ci-notify"))
	m := plan.Manifest
	if m.ID != "ci-notify" || m.Tier != manifest.TierDeclarative || m.Exec != nil {
		t.Fatalf("manifest = %+v", m)
	}
	s := plan.Summary(false)
	if len(s.Permissions.Subscribe) != 1 || s.Permissions.Subscribe[0] != "gh.ci.updated" || len(s.Permissions.Invoke) != 1 || s.Permissions.Invoke[0] != "notify" ||
		len(s.Permissions.Publish) != 0 || len(s.Settings) != 1 || s.Settings[0] != "signal" {
		t.Fatalf("summary = %+v", s)
	}
	res, err := install.Add(home, plan, epoch)
	if err != nil || res.Outcome != install.Installed {
		t.Fatalf("Add = %+v, %v", res, err)
	}
	// Its one setting reaches the settings page.
	fields := manifest.CatalogFields([]manifest.Installed{res.Entry})
	if len(fields) != 2 {
		t.Fatalf("catalog fields = %+v", fields)
	}
}

func TestSampleTimelineHintsNeedsItsBuiltProgramAndThenInstalls(t *testing.T) {
	home := t.TempDir()
	cleanup(t, home)
	src := filepath.Join(samples, "timeline-hints")
	// The checked-in directory has no program: the person builds it first. A
	// developer who followed the documentation has built one there, and then
	// there is nothing to refuse.
	if _, err := os.Stat(filepath.Join(src, "timeline-hints")); errors.Is(err, os.ErrNotExist) {
		if _, err := install.PlanAdd(home, src); err == nil || !strings.Contains(err.Error(), "build the plugin before you add it") {
			t.Fatalf("PlanAdd of the unbuilt sample = %v", err)
		}
	}
	// Copy the sample to a temporary directory and put a stand-in program next
	// to it: the test never builds into the source tree.
	dir := t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "timeline-hints" || e.IsDir() {
			continue // a built program, replaced below
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "timeline-hints"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := mustPlan(t, home, dir)
	if plan.Manifest.ID != "timeline-hints" || plan.Manifest.Tier != manifest.TierProcess || plan.Manifest.Exec.Command != "timeline-hints" {
		t.Fatalf("manifest = %+v", plan.Manifest)
	}
	s := plan.Summary(false)
	if s.Exec == nil || s.Permissions.Subscribe[0] != "op.failed" || s.Permissions.Publish[0] != "plugin.annotation.added" || len(s.Permissions.Invoke) != 0 {
		t.Fatalf("summary = %+v", s)
	}
	if !strings.Contains(strings.Join(s.Warnings, "\n"), "not a sandbox") {
		t.Errorf("a program plugin's warnings do not say it is not sandboxed: %v", s.Warnings)
	}
	if runtime.GOOS != "windows" {
		if _, err := install.Add(home, plan, epoch); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSamplesHaveNoSecretsPathsOrAddresses(t *testing.T) {
	err := filepath.WalkDir(samples, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, bad := range []string{"/Users/", "/home/", "BEGIN PRIVATE KEY", "ghp_", "xoxb-"} {
			if strings.Contains(string(b), bad) {
				return errors.New(p + " contains " + bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
