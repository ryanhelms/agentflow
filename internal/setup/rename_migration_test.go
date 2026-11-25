package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/version"
)

func TestRepinLegacyCaller(t *testing.T) {
	root := t.TempDir()
	legacyPath := ".github/workflows/devflow.yml"
	dest := filepath.Join(root, legacyPath)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	oldSHA, newSHA := strings.Repeat("1", 40), strings.Repeat("2", 40)
	old := "    uses: nscaledev/devflow/.github/workflows/transitions.yml@" + oldSHA + " # v0.52.0\n" +
		"    uses: example/devflow/.github/workflows/another.yml@main\n"
	if err := os.WriteFile(dest, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	oldCommit, oldVersion := version.Commit, version.Version
	version.Commit, version.Version = newSHA, "0.53.0"
	t.Cleanup(func() { version.Commit, version.Version = oldCommit, oldVersion })
	preview, err := Pin(root, true)
	if err != nil || !preview.Changed || preview.File != legacyPath || preview.From != "v0.52.0" {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != old {
		t.Fatalf("preview changed workflow: %s, %v", data, err)
	}
	if _, err := Pin(root, false); err != nil {
		t.Fatal(err)
	}
	want := "    uses: nscaledev/agentflow/.github/workflows/transitions.yml@" + newSHA + " # v0.53.0\n" +
		"    uses: example/devflow/.github/workflows/another.yml@main\n"
	if data, err := os.ReadFile(dest); err != nil || string(data) != want {
		t.Fatalf("workflow = %s, %v; want %s", data, err, want)
	}
	if result, err := Pin(root, false); err != nil || result.Changed {
		t.Fatalf("second pin = %+v, %v", result, err)
	}
}

func TestRefreshMigratesLegacyInstructionBlock(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "AGENTS.md")
	before := "# My project\n\n"
	after := "\n\n## Local guidance\nKeep this exact text.\n"
	old := before + "<!-- devflow:start -->\nold workflow\n<!-- devflow:end -->" + after
	if err := os.WriteFile(file, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshAgentInstructions(root); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(file)
	want := before + blockStart + "\n" + AgentInstructions() + "\n" + blockEnd + after
	if err != nil || string(current) != want {
		t.Fatalf("expected the legacy block to be replaced in place, retaining authored text: %s, %v", current, err)
	}
}

func TestInitPreservesUnmigratedProject(t *testing.T) {
	root := gitRepo(t)
	legacyConfig := []byte("# Project-owned settings\ncommit:\n  require_key: true\n")
	if err := os.Mkdir(filepath.Join(root, ".devflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".devflow", "config.yml"), legacyConfig, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Init(context.Background(), root, false); err == nil || !strings.Contains(err.Error(), "migrate the project") {
		t.Fatalf("init error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agentflow")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("init must leave the checkout unchanged: %v", err)
	}

	if err := os.Rename(filepath.Join(root, ".devflow"), filepath.Join(root, ".agentflow")); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(context.Background(), root, false); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(root, ".agentflow", "config.yml"))
	if err != nil || string(config) != string(legacyConfig) {
		t.Fatalf("project config = %s, %v; wanted unchanged user content", config, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agentflow", "bin", "agentflow")); err != nil {
		t.Fatalf("Agentflow launcher unavailable: %v", err)
	}
}
