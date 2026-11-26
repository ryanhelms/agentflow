package initproviders

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ryanhelms/agentflow/internal/gitx"
)

func TestRepositoryEvidenceAndSavedChoices(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git: %s: %v", b, e)
		}
	}
	write := func(path, content string) {
		t.Helper()
		file := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	git("remote", "add", "origin", "https://github.com/example/product.git")
	repo, err := gitx.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	roles := func() map[string]Role {
		t.Helper()
		p, e := Detect(t.Context(), repo)
		if e != nil {
			t.Fatal(e)
		}
		rs := map[string]Role{}
		for _, r := range p.Roles {
			rs[r.Role] = r
		}
		return rs
	}
	initial := roles()
	if initial["code_hosting"].Selected != "github" || initial["task_management"].Source != "unknown" || initial["knowledge_base"].Selected != "" {
		t.Fatalf("bare remote implied other services: %#v", initial)
	}
	if _, err := os.Stat(filepath.Join(root, ".agentflow")); !os.IsNotExist(err) {
		t.Fatal("detection mutated the checkout")
	}
	write(".github/ISSUE_TEMPLATE/bug.md", "# Report a bug")
	write("docs/knowledge/index.md", "---\nokf_version: \"0.3\"\n---\n")
	write(".github/workflows/ci.yml", "jobs:\n  build:\n    environment: staging\n    steps:\n      - uses: googleapis/release-please-action@v4\n      - uses: github/codeql-action/analyze@v3\n      - uses: slackapi/slack-github-action@v2\n")
	git("add", "docs/knowledge/index.md")
	detected := roles()
	for role, id := range map[string]string{"task_management": "github-issues", "knowledge_base": "repository", "devops.checks": "github", "devops.deployments": "github", "devops.releases": "github", "secops": "github", "notifications": "slack"} {
		if got := detected[role]; got.Selected != id || got.Source != "detected" || len(got.Evidence) == 0 {
			t.Errorf("%s: %#v", role, got)
		}
	}
	write(".agentflow/config.yml", "# team configuration\njira: {projects: [OPS]}\nnotifications: {provider: none}\nknowledge_base: {}\n")
	conflict := roles()
	if got := conflict["task_management"]; got.Source != "ambiguous" || got.Selected != "" || len(got.Evidence) != 2 {
		t.Errorf("conflict: %#v", got)
	}
	if got := conflict["notifications"]; got.Source != "saved" || got.Selected != "none" {
		t.Errorf("none lost: %#v", got)
	}
	if got := conflict["knowledge_base"]; got.Source != "detected" || got.Selected != "repository" {
		t.Errorf("empty section treated as saved: %#v", got)
	}
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.org", "commit", "--allow-empty", "-qm", "feat: adopt a tracker", "-m", "Task-Ref: asana/100/900")
	write(".agentflow/config.yml", "task_management: {settings: {projects: [OPS]}}\nnotifications: {provider: none}\n")
	refs := roles()["task_management"]
	hasAsana := false
	for _, ev := range refs.Evidence {
		if ev.Provider == "asana" {
			hasAsana = true
		}
	}
	if !hasAsana || refs.Source != "ambiguous" {
		t.Fatalf("qualified task trailer was not retained as evidence: %#v", refs)
	}

}

func TestPrepareOnlyRequestedAnswers(t *testing.T) {
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	before := []byte("# keep team rules\nagentflow: 1\ntask_management: {provider: github-issues, settings: {labels: [team]}}\n")
	p := Plan{original: before, Roles: []Role{
		{Role: "task_management", Options: []string{"github-issues", "jira"}, Selected: "github-issues", Source: "saved"},
		{Role: "knowledge_base", Options: []string{"none", "repository"}, Selected: "repository", Source: "detected"},
	}}
	if edit, err := p.Prepare(map[string]string{"task_management": "github-issues"}, false); err != nil || edit != nil {
		t.Fatalf("should preserve exact bytes %v / %v", edit, err)
	}
	edit, err := p.Prepare(map[string]string{"knowledge_base": "none"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(edit.Before, before) || !bytes.Contains(edit.After, []byte("keep team rules")) || !bytes.Contains(edit.After, []byte("[team]")) || !bytes.Contains(edit.After, []byte("none")) {
		t.Fatalf("unexpected edit: %s", edit.After)
	}
	if _, err := p.Prepare(map[string]string{"task_management": "typo"}, false); err == nil {
		t.Fatal("invalid option accepted")
	}
}
