package configconsole

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCIImpactUsesLocalEventAndPathFilters(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"ci.yml": `on:
  pull_request:
    paths: ["**", "!.agentflow/**", ".agentflow/reincluded.yml"]
  push:
    branches: [main]
    paths-ignore: [docs/**]
  merge_group:
  pull_request_target:
    types: [opened]
`,
		"jobs.yaml":  "on: [workflow_call, workflow_dispatch]\n",
		"tags.yml":   "on: {push: {tags: ['v*']}}\n",
		"broken.yml": "on: [\n",
		"follow.yml": "on: workflow_run\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := ciImpact(root, ".agentflow/config.yml", "main")
	for _, want := range []string{
		"[skip actions]", "no applicable rules", "pull_request (file excluded)",
		"push (default branch included, file included)", "merge_group", "pull_request_target (activity-filtered)",
		"workflow_call; workflow_dispatch", "tag pushes only", "triggers unknown", "workflow_run",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("missing %q in %s", want, result)
		}
	}
	if result = ciImpact(root, ".agentflow/reincluded.yml", ""); !strings.Contains(result, "pull_request (file included)") || !strings.Contains(result, "branch-filtered") {
		t.Errorf("did not respect later path inclusion and unresolved branch: %s", result)
	}
}

func TestCIFilterConservative(t *testing.T) {
	cases := []struct {
		patterns []string
		value    string
		ignore   bool
		want     string
	}{
		{[]string{"*.yml"}, "dir/config.yml", false, "excluded"},
		{[]string{"**/*.yml"}, "config.yml", false, "included"},
		{[]string{"dir/**/config.yml"}, "dir/config.yml", false, "filter unresolved"},
		{[]string{"docs/**"}, "docs/more/files.md", true, "excluded"},
		{[]string{"docs/**"}, "docstring.md", true, "included"},
		{[]string{"", "docs/**"}, "docs/file.md", false, "included"},
		{[]string{"docs/**", "!docs/secret/**"}, "docs/secret/one", false, "excluded"},
	}
	for _, tt := range cases {
		if result := ciFilter(tt.patterns, tt.value, tt.ignore); result != tt.want {
			t.Errorf("%q %q: got %s, want %s", tt.patterns, tt.value, result, tt.want)
		}
	}
}

func TestCIImpactDoesNotReadSymlinks(t *testing.T) {
	root := t.TempDir()
	if !strings.Contains(ciImpact(root, "config.yml", ""), "No local") {
		t.Fatal("empty repository was not reported")
	}
	private := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(private, []byte("on: private_event"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(dir, "link.yml")); err != nil {
		t.Fatal(err)
	}
	note := ciImpact(root, "config.yml", "")
	if !strings.Contains(note, "triggers unknown") || strings.Contains(note, "private_event") {
		t.Fatalf("unsafe workflow read: %s", note)
	}
}
