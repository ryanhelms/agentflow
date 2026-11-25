package setup

import (
	"strings"
	"testing"
)

func TestAsanaReadinessMissingPrerequisite(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ASANA_ACCESS_TOKEN", "")
	root := t.TempDir()
	setupFile(t, root, "records/file", "doc", 0600)
	for _, tc := range []struct{ settings, detail string }{
		{"{}", "task_management.settings.project"},
		{"{project: '10'}", "Asana MCP"},
	} {
		cfg := strings.Replace(headlessConfig, "provider: github-issues, settings: {repository: sample/project}", "provider: asana, settings: "+tc.settings, 1)
		setupFile(t, root, ".agentflow/config.yml", cfg, 0600)
		plan := planFixture(t, root, "")
		if plan.Ready || plan.InstallGH != nil {
			t.Fatalf("readiness %+v", plan)
		}
		planContains(t, plan, tc.detail)
	}
}
