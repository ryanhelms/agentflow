package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRepoPreservesPolicyOnLegacyTargetBranch(t *testing.T) {
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	root := t.TempDir()
	write := func(directory, content string) {
		t.Helper()
		dir := filepath.Join(root, directory)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".devflow", "devflow: 1\ncommit: {require_key: true}\n")
	for _, load := range []func(string) (*Repo, bool, error){LoadRepo, LoadRepoCore} {
		cfg, found, err := load(root)
		if err != nil || !found || !cfg.Commit.KeyRequired() {
			t.Fatalf("legacy commit policy unavailable: found=%v, err=%v", found, err)
		}
	}

	write(".agentflow", "agentflow: 1\ncommit: {require_key: false}\n")
	for _, load := range []func(string) (*Repo, bool, error){LoadRepo, LoadRepoCore} {
		cfg, found, err := load(root)
		if err != nil || !found || cfg.Commit.KeyRequired() {
			t.Fatalf("canonical config should take precedence: found=%v, err=%v", found, err)
		}
	}
}

func TestLoadUserPreservesLegacyAutonomy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTFLOW_HOME", "")
	t.Setenv("DEVFLOW_HOME", "")
	legacy := filepath.Join(home, ".devflow")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.yml"), []byte("autonomy: {local: suggest, remote: auto}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadUser()
	if err != nil || cfg.Autonomy.Local != Suggest || cfg.Autonomy.Remote != Auto {
		t.Fatalf("legacy autonomy unavailable: %v, %v", cfg, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agentflow")); !os.IsNotExist(err) {
		t.Fatalf("should keep using the existing directory until migration: %v", err)
	}
	t.Setenv("AGENTFLOW_HOME", filepath.Join(home, "explicit"))
	t.Setenv("DEVFLOW_HOME", legacy)
	if dir, err := HomeDir(); err != nil || dir != filepath.Join(home, "explicit") {
		t.Fatalf("explicit Agentflow home should win: %q, %v", dir, err)
	}
}
