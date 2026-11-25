package consolecfg

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/config"
	"gopkg.in/yaml.v3"
)

func TestPreviewDiffAppliesWithGit(t *testing.T) {
	for _, tt := range []struct{ name, old, next string }{
		{"replace", "a\nb\nc\nd\ne\nf\ng\nh\ni\n", "a\nb\nc\nd\nnew\nf\ng\nh\ni\n"},
		{"append", "a\nb", "a\nb\nc"},
		{"last newline", "a\nb\n", "a\nb"},
		{"empty source", "", "text\n"},
		{"empty result", "text\n", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, MainFile, tt.old)
			cmd := exec.Command("git", "apply", "-")
			cmd.Dir = root
			cmd.Stdin = strings.NewReader(diff(MainFile, tt.old, tt.next))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git apply: %s: %v", output, err)
			}
			actual, err := os.ReadFile(filepath.Join(root, MainFile))
			if err != nil || string(actual) != tt.next {
				t.Fatalf("applied patch has wrong contents: %q, %v", actual, err)
			}
		})
	}
}

func put(t *testing.T, root, name, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (root, personal string) {
	t.Helper()
	root, personal = t.TempDir(), t.TempDir()
	t.Setenv("AGENTFLOW_HOME", personal)
	return root, personal
}

func searchLimit(t *testing.T, s Snapshot) config.Setting {
	t.Helper()
	if s.Providers == nil {
		t.Fatalf("provider snapshot unavailable: %v", s.Problems)
	}
	for _, role := range s.Providers.Roles {
		if role.Role == "task_management" {
			for _, field := range role.Settings {
				if field.Path == "search.limit" {
					return field
				}
			}
		}
	}
	t.Fatal("missing search limit")
	return config.Setting{}
}

func TestInheritancePreviewAndStaleBase(t *testing.T) {
	root, home := fixture(t)
	content := "# keep this comment\ntask_management:\n  provider: github-issues\n  settings: {search: {limit: 40}}\n"
	definition := func(n string) string {
		return "version: 1\nproviders:\n  github-issues:\n    task_management:\n      settings: {search: {limit: " + n + "}}\n"
	}
	put(t, root, MainFile, content)
	put(t, root, definitionsDir+"/issues.yaml", definition("25"))
	put(t, home, "providers/issues.yml", definition("10"))
	ctx := context.Background()
	s, err := View(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	got := searchLimit(t, s)
	if got.Value != 10 || len(got.History) != 4 || got.History[0].Value != 30 || got.History[1].Value != 25 || got.History[2].Value != 40 || got.History[3].Value != 10 {
		t.Fatalf("wrong trace: %+v", got)
	}
	if s.Schemas["task_management"]["github-issues"].Properties["search"].Properties["limit"].Type != "integer" {
		t.Fatal("missing typed search limit")
	}
	draft, err := EditSetting(content, "task_management", "search.limit", json.RawMessage("55"), false)
	if err != nil || !strings.Contains(draft, "# keep this comment") {
		t.Fatalf("compose: %s %v", draft, err)
	}
	plan, err := Preview(ctx, root, MainFile, draft, digest(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 || !strings.Contains(plan.Diff, "55") {
		t.Fatalf("unexpected effective changes: %+v", plan)
	}
	if s := searchLimit(t, Snapshot{Providers: plan.Providers}); s.Value != 10 || s.History[2].Value != 55 {
		t.Fatalf("candidate trace: %+v", s)
	}
	if _, err := Preview(ctx, root, MainFile, draft, "stale"); err == nil {
		t.Fatal("stale digest accepted")
	}
	b, _ := os.ReadFile(filepath.Join(root, MainFile))
	if string(b) != content {
		t.Fatal("preview changed live file")
	}
}

func TestRepairAndCatalogReferences(t *testing.T) {
	root, _ := fixture(t)
	bad := "task_management: ["
	put(t, root, MainFile, bad)
	ctx := context.Background()
	s, err := View(ctx, root)
	if err != nil || len(s.Problems) == 0 || len(s.Files) != 1 || !s.Files[0].Editable {
		t.Fatalf("repair unavailable: %+v %v", s, err)
	}
	good := "agentflow: 1\ntask_management:\n  provider: github-issues\n  templates: {catalog: docs/tasks.json, default: {name: engineering, revision: '1'}}\n"
	if _, err := Preview(ctx, root, MainFile, good, digest(bad)); err == nil {
		t.Fatal("missing catalog accepted")
	}
	put(t, root, "docs/tasks.json", `[{"name":"engineering","revision":"1"}]`)
	if _, err := Preview(ctx, root, MainFile, good, digest(bad)); err != nil {
		t.Fatalf("could not repair: %v", err)
	}
}

func TestManagedFilesLinksAndCredentialsStayPrivate(t *testing.T) {
	root, _ := fixture(t)
	put(t, root, MainFile, "task_management: {provider: jira}\n")
	put(t, root, ".codex/config.toml", "do not expose this raw file")
	put(t, root, "private-notes", "secret: withheld-here")
	if err := os.MkdirAll(filepath.Join(root, definitionsDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "private-notes"), filepath.Join(root, definitionsDir, "alias.yml")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := View(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "do not expose") || strings.Contains(string(raw), "withheld-here") || len(s.Problems) == 0 {
		t.Fatalf("unsafe snapshot")
	}
	for _, p := range []string{".codex/config.toml", "../private-notes", definitionsDir + "/alias.yml"} {
		if _, err := Preview(ctx, root, p, "new", "fake"); err == nil {
			t.Fatalf("unsafe edit %s", p)
		}
	}
	if err := os.Remove(filepath.Join(root, definitionsDir, "alias.yml")); err != nil {
		t.Fatal(err)
	}
	put(t, root, MainFile, "secret: actual-cleartext")
	s, err = View(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(s)
	if strings.Contains(string(raw), "actual-cleartext") {
		t.Fatal("secret appeared in snapshot")
	}
}

func TestEditLiteralKeysAndProviderSwitch(t *testing.T) {
	content := "task_management:\n  provider: github-issues\n  settings:\n    transitions:\n      pr.merged: closed\n  templates: {catalog: .agentflow/task-templates.json}\n"
	next, err := EditSetting(content, "task_management", `transitions.pr\.merged`, json.RawMessage(`"none"`), false)
	if err != nil || !strings.Contains(next, "pr.merged: \"none\"") {
		t.Fatalf("escaped key: %v %s", err, next)
	}
	root, _ := fixture(t)
	put(t, root, MainFile, "task_management: {provider: github-issues}\n")
	put(t, root, ".agentflow/task-templates.json", "[]")
	reset, err := EditSetting(next, "task_management", `transitions.pr\.merged`, nil, true)
	if err != nil || strings.Contains(reset, "pr.merged:") {
		t.Fatalf("reset: %v %s", err, reset)
	}
	switched, err := EditSetting(next, "task_management", "", json.RawMessage(`"jira"`), false)
	if err != nil || strings.Contains(switched, "transitions:") || !strings.Contains(switched, "templates:") {
		t.Fatalf("switch: %v %s", err, switched)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(switched), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, err := EditSetting(next, "task_management", "", nil, true); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidFileTypesAndUnchangedSensitiveReferences(t *testing.T) {
	root, _ := fixture(t)
	content := "agentflow: 1\nflow_map: .env\n"
	put(t, root, MainFile, content)
	put(t, root, ".env", "a-private-value")
	put(t, root, ".agentflow/releases.yml", "strategy: trunk\nunits: []\n")
	s, err := View(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "a-private-value") {
		t.Fatal("read private reference")
	}
	cases := []struct{ path, old, next string }{
		{MainFile, content, "agentflow: 1\n---\nunknown: true\n"},
		{".agentflow/releases.yml", "strategy: trunk\nunits: []\n", "strategy: wrong\nunits: []\n"},
	}
	for _, c := range cases {
		if _, err := Preview(context.Background(), root, c.path, c.next, digest(c.old)); err == nil {
			t.Errorf("invalid %s accepted", c.path)
		}
	}
}
