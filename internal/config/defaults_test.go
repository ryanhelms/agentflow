package config

import (
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
