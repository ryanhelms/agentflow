package install_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

var yamlBlock = regexp.MustCompile("(?s)```yaml\n(.*?)```")

// TestPluginsDocExamplesAreValid keeps docs/plugins.md true: its complete
// manifest loads, its permission rule validates inside a manifest, and its
// repository setting is one agentflow reads.
func TestPluginsDocExamplesAreValid(t *testing.T) {
	b, err := os.ReadFile("../../../docs/plugins.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := yamlBlock.FindAllStringSubmatch(string(b), -1)
	if len(blocks) < 3 {
		t.Fatalf("found %d yaml blocks in docs/plugins.md", len(blocks))
	}
	var manifests, rules, repos int
	for _, m := range blocks {
		text := m[1]
		switch {
		case strings.HasPrefix(text, "schema: 1"):
			manifests++
			if _, err := manifest.Load([]byte(text)); err != nil {
				t.Errorf("manifest example does not load: %v\n%s", err, text)
			}
		case strings.HasPrefix(text, "extensions:") && strings.Contains(text, "permission_rule"):
			rules++
			full := "schema: 1\nid: pause-pushes\nname: Pause pushes\nversion: 0.1.0\ntier: declarative\n" + text
			if _, err := manifest.Load([]byte(full)); err != nil {
				t.Errorf("rule example does not load: %v\n%s", err, text)
			}
		case strings.HasPrefix(text, "plugins:"):
			repos++
			var repo config.Repo
			dec := yaml.NewDecoder(strings.NewReader(text))
			dec.KnownFields(true)
			if err := dec.Decode(&repo); err != nil || len(repo.Plugins.Disabled) != 1 {
				t.Errorf("repository example: %v, %+v", err, repo.Plugins)
			}
		}
	}
	if manifests != 1 || rules != 1 || repos != 1 {
		t.Errorf("checked %d manifests, %d rules and %d repository settings, want one of each", manifests, rules, repos)
	}
}
