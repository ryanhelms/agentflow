package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/setup"
)

func TestReadinessUsesOnlyCurrentCheckoutAndReturnsMeasuredResults(t *testing.T) {
	s := testServer(t)
	contents := "code_hosting: {provider: none}\ntask_management: {provider: filesystem, settings: {root: private-fixture}}\nknowledge_base: {provider: confluence}\ndevops: {checks: {provider: none}, deployments: {provider: none}, releases: {provider: none}}\nsecops: {provider: none}\nnotifications: {provider: none}\n"
	configPath := filepath.Join(s.repo.Dir, ".devflow", "config.yml")
	if err := os.WriteFile(configPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	for _, present := range []bool{false, true} {
		if present {
			if err := os.Mkdir(filepath.Join(s.repo.Dir, "private-fixture"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		w := request(t, s, "POST", "/api/readiness", s.origin, "session", struct{}{})
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var got struct {
			Ready        bool
			Requirements []connectionCheck
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Ready || len(got.Requirements) != 2 {
			t.Fatalf("unexpected checks: %+v", got)
		}
		if task := got.Requirements[0]; task.Role != "task_management" || task.Provider != "filesystem" || task.Ready != present || task.VerifyInClient || task.Detail == "" {
			t.Fatalf("filesystem check: %+v", task)
		}
		if docs := got.Requirements[1]; docs.Role != "knowledge_base" || !docs.VerifyInClient || docs.Ready {
			t.Fatalf("MCP check: %+v", docs)
		}
		for _, private := range []string{"private-fixture", s.repo.Dir, "install_gh", "registration", "provider_tools", "companions"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("unexpected plan data in response: %q", private)
			}
		}
	}
	after, err := os.ReadFile(configPath)
	if err != nil || string(after) != contents {
		t.Fatal("connection check changed the configured providers")
	}
}

func TestReadinessRejectsOverridesAndCrossOriginProbes(t *testing.T) {
	s := testServer(t)
	for _, tc := range []struct {
		origin, key string
		body        any
		code        int
	}{
		{"https://unrelated.invalid", "session", struct{}{}, 403},
		{s.origin, "", struct{}{}, 401},
		{s.origin, "session", map[string]any{"client": "codex"}, 400},
		{s.origin, "session", map[string]any{"root": "/elsewhere"}, 400},
		{s.origin, "session", map[string]any{"content": "staged configuration"}, 400},
		{s.origin, "session", nil, 400},
	} {
		w := request(t, s, "POST", "/api/readiness", tc.origin, tc.key, tc.body)
		if w.Code != tc.code {
			t.Fatalf("expected %d, got %d: %s", tc.code, w.Code, w.Body.String())
		}
	}
	const sensitivePath = "secret-stdout-example"
	for _, provider := range []string{"github", "jira", "filesystem", "asana", "custom"} {
		detail := connectionDetail(setup.ProviderRequirement{Role: "task_management", Provider: provider, Detail: "check connectivity and run gh auth login " + sensitivePath})
		if strings.Contains(detail, sensitivePath) || detail == "" {
			t.Fatal("unsafe provider output")
		}
	}
}
