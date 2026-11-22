package setup

import (
	"os"
	"path/filepath"
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
