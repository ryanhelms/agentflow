package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsPointAtRyanhelmsRepository(t *testing.T) {
	if DefaultFlowMap != "ryanhelms/agentflow@v0:flow-map.yml" {
		t.Fatalf("DefaultFlowMap = %q", DefaultFlowMap)
	}
	if len(DefaultInitOrgs) != 0 {
		t.Fatalf("DefaultInitOrgs = %v, want none", DefaultInitOrgs)
	}
}

func TestLoadUserHasNoDefaultInitOrgs(t *testing.T) {
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	u, err := LoadUser()
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Init.Orgs) != 0 || u.Init.Auto != "auto" {
		t.Fatalf("init = %+v, want no owners", u.Init)
	}
}

func TestParseWorktreeAndCoordinatorMerge(t *testing.T) {
	raw := []byte(`policy:
  coordinator_merge: true
worktree:
  root: ~/wt
  go_work:
    use: [".", "multiplayer"]
    siblings:
      agent-acp.local/sdks/go: { repo: agent-acp, path: sdks/go }
      agent-acp.local/transport: { repo: agent-acp-transport, path: . }
`)
	c, _, err := ParseRepoCore(raw)
	if err != nil {
		t.Fatal(err)
	}
	gw := c.Worktree.GoWork
	if !c.Policy.CoordinatorMerge || c.Worktree.Root != "~/wt" || gw == nil || len(gw.Use) != 2 ||
		gw.Siblings["agent-acp.local/transport"] != (GoWorkSibling{Repo: "agent-acp-transport", Path: "."}) {
		t.Fatalf("parsed %+v %+v", c.Policy, c.Worktree)
	}
}

func TestAtlassianSite(t *testing.T) {
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	for _, tc := range []struct{ name, config, want string }{
		{"none", "task_management: {provider: jira}\n", ""},
		{"task provider", "task_management: {provider: jira, settings: {site: https://task.example}}\n", "https://task.example"},
		{"jira.site", "jira: {site: https://legacy.example}\n", "https://legacy.example"},
		{"knowledge base", "knowledge_base: {provider: confluence, settings: {site: https://docs.example}}\n", "https://docs.example"},
		{"confluence.site", "confluence: {site: https://wiki.example}\n", "https://wiki.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".agentflow"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".agentflow", "config.yml"), []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := AtlassianSite(root); got != tc.want {
				t.Fatalf("site %q, want %q", got, tc.want)
			}
		})
	}
}
