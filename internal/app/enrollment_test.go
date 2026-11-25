package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nscaledev/agentflow/internal/setup"
)

func TestProjectEnrollmentRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	original := map[string]string{
		"AGENTS.md":                       "# Project rules   ",
		"release-please-config.json":      "{\"packages\":{}}\n",
		".release-please-manifest.json":   "{\".\":\"3.2.1\"}\n",
		".githooks/commit-msg":            "#!/bin/sh\nexit 0\n",
		".github/workflows/agentflow.yml": "# pre-existing project workflow\n",
		".jira/config.yml":                "project: TEAM\n",
	}
	for name, data := range original {
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := exec.Command("git", "-C", dir, "config", "--local", "core.hooksPath", "custom-hooks").Run(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := InitRepo(ctx, dir, "test", false); err != nil {
			t.Fatal(err)
		}
	}
	for _, dry := range []bool{true, false, false} {
		if _, err := RemoveRepo(ctx, dir, setup.RemoveOptions{DryRun: dry}); err != nil {
			t.Fatal(err)
		}
		if dry {
			if _, err := os.Stat(filepath.Join(dir, ".agentflow/installation.yml")); err != nil {
				t.Fatal("preview removed receipt", err)
			}
		} else {
			// Older installations have no receipt. Repeated deletion of an
			// opted-out installation must not infer ownership of remaining files.
			for name, data := range original {
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(b) != data {
					t.Fatalf("%s changed on removal: %q (%v)", name, b, err)
				}
			}
		}
	}
	hooks, err := exec.Command("git", "-C", dir, "config", "--local", "core.hooksPath").Output()
	if err != nil || string(hooks) != "custom-hooks\n" {
		t.Fatalf("hooks %q: %v", hooks, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".agentflow")); !os.IsNotExist(err) {
		t.Fatalf("install artifacts remain: %v", err)
	}
}
