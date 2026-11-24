package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderGuideExamples(t *testing.T) {
	guide, err := os.ReadFile(filepath.Join("..", "..", "docs", "providers.md"))
	if err != nil {
		t.Fatal(err)
	}
	examples := strings.Split(string(guide), "```yaml\n")
	checked, definitionsChecked := 0, 0
	for _, block := range examples[1:] {
		example, _, ok := strings.Cut(block, "```")
		if !ok {
			t.Fatal("unterminated YAML example")
		}
		if strings.Contains(example, "\nproviders:\n") {
			t.Run("definition", func(t *testing.T) {
				var root string
				if strings.Contains(example, "# Embedded release definition") {
					root, _ = layers(t, example)
				} else {
					root, _ = layers(t, "")
					project(t, root, "example.yml", example)
				}
				for _, id := range []string{"jira", "github-issues"} {
					config(t, root, "task_management: {provider: "+id+"}")
					if _, _, err := LoadRepo(root); err != nil {
						t.Fatalf("invalid definition: %v\n%s", err, example)
					}
				}
			})
			definitionsChecked++
			continue
		}
		// Companions and client manifests use separate files and validators.
		if !isRepositoryProviderExample(example) {
			continue
		}
		checked++
		root := t.TempDir()
		configDir := filepath.Join(root, ".agentflow")
		if err := os.Mkdir(configDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "config.yml"), []byte(example), 0o600); err != nil {
			t.Fatal(err)
		}
		_, found, err := LoadRepo(root)
		if err != nil || !found {
			t.Fatalf("provider guide example does not load: %v\n%s", err, example)
		}
	}
	if definitionsChecked < 2 {
		t.Fatal("missing release or project provider definition example")
	}
	if checked == 0 {
		t.Fatal("no repository provider configuration examples found")
	}
}

func isRepositoryProviderExample(example string) bool {
	for _, line := range strings.Split(example, "\n") {
		key, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "agentflow", "code_hosting", "task_management", "knowledge_base", "devops", "secops", "notifications":
			return true
		}
	}
	return false
}
