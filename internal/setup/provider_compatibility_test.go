package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type callerContract struct {
	Inputs  map[string]yaml.Node `yaml:"inputs"`
	Outputs map[string]yaml.Node `yaml:"outputs"`
	Secrets map[string]yaml.Node `yaml:"secrets"`
}

func TestProviderWorkflowCompatibility(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		inputs  []string
		outputs []string
		secrets []string
	}{
		{"jira-key", []string{"warn-only"}, nil, []string{"JIRA_USER", "JIRA_TOKEN"}},
		{"transitions", []string{"dry-run"}, nil, []string{"JIRA_USER", "JIRA_TOKEN"}},
		{"semver-release", []string{"registry"}, []string{"releases"}, []string{"JIRA_USER", "JIRA_TOKEN"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join("..", "..", ".github", "workflows", fixture.name+".yml")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				On struct {
					Call callerContract `yaml:"workflow_call"`
				} `yaml:"on"`
			}
			if err := yaml.Unmarshal(raw, &workflow); err != nil {
				t.Fatal(err)
			}
			checkContractKeys(t, "inputs", workflow.On.Call.Inputs, fixture.inputs...)
			checkContractKeys(t, "outputs", workflow.On.Call.Outputs, fixture.outputs...)
			checkContractKeys(t, "secrets", workflow.On.Call.Secrets, fixture.secrets...)
		})
	}
}

func checkContractKeys(t *testing.T, section string, values map[string]yaml.Node, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			t.Errorf("missing legacy %s key %q", section, key)
		}
	}
}

func TestLegacyWorkflowWritersRemainSelected(t *testing.T) {
	type step struct {
		Name string            `yaml:"name"`
		If   string            `yaml:"if"`
		Run  string            `yaml:"run"`
		Env  map[string]string `yaml:"env"`
	}
	for _, tc := range []struct {
		file, title, selection string
		commands               []string
	}{
		{".github/workflows/transitions.yml", "Apply legacy Jira transitions", "steps.task.outputs.provider == 'jira'", []string{"handle-event.sh"}},
		{".github/workflows/auto-issues.yml", "Mirror labelled GitHub issue", "steps.task.outputs.provider == 'jira'", []string{"jira-create.sh"}},
		{".github/workflows/ai-pr-draft.yml", "Fetch Jira context", "steps.task.outputs.provider == 'jira'", []string{"jira-context.sh"}},
		{".github/workflows/docs-sync.yml", "Publish pages", "steps.knowledge.outputs.provider == 'confluence'", []string{"confluence-upsert.sh"}},
		{".github/workflows/release-notes.yml", "Publish to Confluence", "steps.knowledge.outputs.provider == 'confluence'", []string{"confluence-upsert.sh"}},
		{".github/workflows/sprint-report.yml", "Publish to Confluence", "steps.providers.outputs.knowledge == 'confluence'", []string{"confluence-upsert.sh"}},
		{"actions/ship/action.yml", "Apply Jira effects for shipped commits", "steps.keys.outputs.provider == 'jira'", []string{"transition.sh", "jira-fix-version.sh"}},
		{".github/workflows/semver-release.yml", "Record released task references", "", []string{`[ "$PROVIDER" = jira ]`, "jira-fix-version.sh"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			var manifest struct {
				Jobs map[string]struct{ Steps []step } `yaml:"jobs"`
				Runs struct{ Steps []step }            `yaml:"runs"`
			}
			if err := yaml.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			steps := manifest.Runs.Steps
			for _, job := range manifest.Jobs {
				steps = append(steps, job.Steps...)
			}
			for _, call := range steps {
				if call.Name != tc.title {
					continue
				}
				if tc.selection != "" && !strings.Contains(call.If, tc.selection) {
					t.Errorf("step is missing its provider selection: %q", call.If)
				}
				for _, command := range tc.commands {
					if !strings.Contains(call.Run, command) {
						t.Errorf("existing workflow step no longer runs %q", command)
					}
				}
				return
			}
			t.Fatalf("missing provider workflow step %q", tc.title)
		})
	}
}
