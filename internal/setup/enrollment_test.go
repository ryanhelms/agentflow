package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemovalPreservesUnrelatedProject(t *testing.T) {
	dir := gitRepo(t)
	files := map[string]string{
		".claude/settings.json":      `{"includeCoAuthoredBy":false}`,
		".codex/config.toml":         "# Workspace configuration\n[mcp_servers.agentflow]\ncommand = \"my-company-command\"\n",
		".agentflow/config.yml":        "project: local\n",
		".agentflow/bin/agentflow":       "#!/bin/sh\n# Another project tool\n",
		"release-please-config.json": "{}\n",
	}
	for rel, body := range files {
		setupFile(t, dir, rel, body, 0o600)
	}
	if err := exec.Command("git", "-C", dir, "config", "--local", "core.hooksPath", ".githooks").Run(); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false, false} {
		if _, err := Remove(t.Context(), dir, RemoveOptions{Force: true, DryRun: dry}); err != nil {
			t.Fatal(err)
		}
		for rel, body := range files {
			data, err := os.ReadFile(filepath.Join(dir, rel))
			if err != nil || string(data) != body {
				t.Fatalf("%s after remove: %q, %v", rel, data, err)
			}
		}
	}
	hp, err := exec.Command("git", "-C", dir, "config", "--local", "core.hooksPath").Output()
	if err != nil || strings.TrimSpace(string(hp)) != ".githooks" {
		t.Fatal("changed an unrelated core.hooksPath", err)
	}
}

func TestProjectLifecyclePreservesSharedOwnership(t *testing.T) {
	for _, mode := range []string{"init", "client", "client-then-init", "guided-client"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			dir := gitRepo(t)
			const rel = ".claude/settings.json"
			const original = `{"includeCoAuthoredBy":true,"enabledPlugins":{"agentflow@nscale":true},"team":"project-private-value"}`
			setupFile(t, dir, rel, original, 0o600)
			const codex = "# team-owned table\n[mcp_servers.agentflow]\ncommand = \"custom\"\n"
			setupFile(t, dir, ".codex/config.toml", codex, 0o600)
			const existingGuidance = "# Team instructions\n\n" + blockStart + "\nTeam-managed version\n" + blockEnd + "\n"
			if mode == "guided-client" {
				setupFile(t, dir, agentInstructionsFile, existingGuidance, 0o600)
			}
			switch mode {
			case "init":
				if _, err := Init(t.Context(), dir, false); err != nil {
					t.Fatal(err)
				}
			case "guided-client":
				if err := RegisterProviderClient(dir, "claude"); err != nil {
					t.Fatal(err)
				}
			default:
				if _, err := Install(dir, []string{"claude", "codex"}); err != nil {
					t.Fatal(err)
				}
				if mode == "client-then-init" {
					setupFile(t, dir, "release-please-config.json", "{}", 0o600)
					if _, err := Init(t.Context(), dir, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			path := filepath.Join(dir, rel)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if doc["includeCoAuthoredBy"] != true {
				t.Fatal("overwrote a pre-existing client preference")
			}
			if mode != "init" && doc["extraKnownMarketplaces"] == nil {
				t.Fatal("missing added client registration")
			}
			doc["later"] = "another-private-value"
			after, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			setupFile(t, dir, rel, string(after), 0o600)
			setupFile(t, dir, ".codex/config.toml", codex+"# later edit\n", 0o600)
			record, err := os.ReadFile(filepath.Join(dir, receiptPath))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(record, []byte("private-value")) {
				t.Fatal("receipt contains project data")
			}
			if _, err := Remove(t.Context(), dir, RemoveOptions{Force: true}); err != nil {
				t.Fatal(err)
			}
			read, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(read, &result); err != nil {
				t.Fatal(err)
			}
			if result["includeCoAuthoredBy"] != true || result["extraKnownMarketplaces"] != nil || result["later"] != "another-private-value" || result["team"] != "project-private-value" || result["enabledPlugins"].(map[string]any)["agentflow@nscale"] != true {
				t.Fatalf("unexpected cleanup: %s", read)
			}
			data, err = os.ReadFile(filepath.Join(dir, ".codex/config.toml"))
			if err != nil || string(data) != codex+"# later edit\n" {
				t.Fatal("removed a user Codex table", err)
			}
			if mode == "client-then-init" && !exists(dir, "release-please-config.json") {
				t.Fatal("lost config created between client and full enrollment")
			}
			if mode == "guided-client" {
				current, err := os.ReadFile(filepath.Join(dir, agentInstructionsFile))
				if err != nil || string(current) != existingGuidance {
					t.Fatal("standalone registration changed untouched agent guidance", err)
				}
			}
			if exists(dir, receiptPath) {
				t.Fatal("receipt left behind")
			}
		})
	}
}

func TestDefaultHooksRemainActiveAtInit(t *testing.T) {
	dir := gitRepo(t)
	setupFile(t, dir, ".git/hooks/pre-commit", "#!/bin/sh\nexit 0\n", 0o755)
	if _, err := Init(t.Context(), dir, false); err != nil {
		t.Fatal(err)
	}
	if hp, _ := exec.Command("git", "-C", dir, "config", "--local", "core.hooksPath").Output(); len(hp) > 0 {
		t.Fatalf("shadowed pre-existing default hooks with %s", hp)
	}
	if _, err := Remove(t.Context(), dir, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if !exists(dir, ".git/hooks/pre-commit") {
		t.Fatal("removed default hook")
	}
}
