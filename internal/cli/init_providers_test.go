package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/setup"
	"github.com/nscaledev/devflow/internal/setup/initproviders"
)

func TestGuidedInitCLI(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	if b, err := exec.Command("git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git: %s: %v", b, err)
	}
	out, err := captureCLI(t, "init", "--detect-providers")
	if err != nil {
		t.Fatal(err)
	}
	var discovery initproviders.Plan
	if err := json.Unmarshal([]byte(out), &discovery); err != nil || len(discovery.Roles) != len(config.ProviderRoles) {
		t.Fatalf("invalid detection %v: %s", err, out)
	}
	for _, args := range [][]string{{"--provider", "task_management=invalid"}, {"unexpected/path"}, {"--guided", "--non-interactive"}} {
		if _, err := captureCLI(t, "init", args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := os.Stat(".devflow"); !os.IsNotExist(err) {
		t.Fatal("inspection or invalid input created files")
	}
	for i := 0; i < 2; i++ {
		if _, err := captureCLI(t, "init", "--non-interactive", "--provider", "task_management=github-issues", "--provider", "knowledge_base=none"); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := config.LoadRepo(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Providers.TaskManagement.Provider != "github-issues" || cfg.Providers.KnowledgeBase.Provider != "none" || cfg.Providers.Notifications.Configured {
			t.Fatalf("choices differ: %#v", cfg.Providers)
		}
	}
	out, err = captureCLI(t, "init", "--detect-providers")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &discovery); err != nil {
		t.Fatal(err)
	}
	for _, r := range discovery.Roles {
		if (r.Role == "task_management" || r.Role == "knowledge_base") && r.Source != "saved" {
			t.Fatalf("selection not reused: %#v", r)
		}
	}
	before, err := os.ReadFile(".devflow/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.SaveInitProviderConfig(dir, []byte("stale"), []byte("overwritten")); err == nil {
		t.Fatal("overwrote changed file")
	}
	after, err := os.ReadFile(".devflow/config.yml")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("stale apply changed preferences: %v", err)
	}
}

func TestGuideLeavesDetectedOptionalProvidersUnassigned(t *testing.T) {
	plan := initproviders.Plan{Roles: []initproviders.Role{
		{Role: "task_management", Default: "none", Selected: "jira", Source: "detected", Options: []string{"jira", "none"}},
		{Role: "knowledge_base", Default: "none", Selected: "repository", Source: "detected", Options: []string{"repository", "none"}},
	}}
	answers := map[string]string{}
	var output bytes.Buffer
	if err := guideProviders(&plan, answers, strings.NewReader("\nrepository\n\n"), &output); err != nil {
		t.Fatal(err)
	}
	if _, activated := answers["task_management"]; activated || answers["knowledge_base"] != "repository" ||
		!strings.Contains(output.String(), "task_management = none") {
		t.Fatalf("%v: %s", answers, &output)
	}
}

func TestGuidePromptsUnknownAndAllowsRevision(t *testing.T) {
	plan := initproviders.Plan{Roles: []initproviders.Role{
		{Role: "code_hosting", Selected: "github", Source: "detected", Options: []string{"github", "none"}, Evidence: []initproviders.Evidence{{Provider: "github", Source: "Git remote resolves to GitHub"}}},
		{Role: "notifications", Default: "slack", Source: "unknown", Options: []string{"slack", "none"}},
	}}
	answers := map[string]string{}
	var output bytes.Buffer
	if err := guideProviders(&plan, answers, strings.NewReader("typo\nnone\ncode_hosting\nnone\n\n"), &output); err != nil {
		t.Fatal(err)
	}
	if answers["code_hosting"] != "none" || answers["notifications"] != "none" || !strings.Contains(output.String(), "Git remote resolves to GitHub") {
		t.Fatalf("%v: %s", answers, &output)
	}
	if err := guideProviders(&plan, map[string]string{}, strings.NewReader(""), &output); err == nil {
		t.Fatal("EOF accepted")
	}
}
