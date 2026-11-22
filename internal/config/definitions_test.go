package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// layers sets the release layer and returns a repo root and user home.
func layers(t *testing.T, release string) (root, home string) {
	t.Helper()
	old := releaseDefinitions
	t.Cleanup(func() { releaseDefinitions = old })
	releaseDefinitions = fstest.MapFS{}
	if release != "" {
		releaseDefinitions = fstest.MapFS{"nscale.yml": {Data: []byte(release)}}
	}
	home = t.TempDir()
	t.Setenv("DEVFLOW_HOME", home)
	return t.TempDir(), home
}

func put(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func project(t *testing.T, root, name, contents string) {
	put(t, filepath.Join(root, ".devflow", "providers", name), contents)
}

func user(t *testing.T, home, name, contents string) {
	put(t, filepath.Join(home, "providers", name), contents)
}

func config(t *testing.T, root, contents string) {
	put(t, filepath.Join(root, ".devflow", "config.yml"), contents)
}

const jiraRelease = `version: 1
providers:
  jira:
    task_management:
      contract: tasks/v1
      settings: {site: nscale.atlassian.net, projects: [PLAT]}
`

func TestDefinitionLayerOrder(t *testing.T) {
	root, home := layers(t, jiraRelease)
	project(t, root, "jira.yml", "version: 1\nproviders:\n  jira:\n    task_management:\n      settings: {projects: [PLAT, INFRA]}\n")
	config(t, root, "devflow: 1\ntask_management:\n  provider: jira\n  settings: {projects: [OPS]}\n")
	user(t, home, "jira.yaml", "version: 1\nproviders:\n  jira:\n    task_management:\n      settings: {site: dev.atlassian.net}\n")
	c, _, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	sel := c.Providers.TaskManagement
	got := sel.Settings.(*JiraSettings)
	if got.Site != "dev.atlassian.net" || !reflect.DeepEqual(got.Projects, []string{"OPS"}) {
		t.Fatalf("settings = %+v", got)
	}
	want := map[string]Source{
		"site":     {LayerUser, "~/.devflow/providers/jira.yaml"},
		"projects": {LayerProject, ".devflow/config.yml"},
	}
	if !reflect.DeepEqual(sel.Sources, want) {
		t.Errorf("sources = %v, want %v", sel.Sources, want)
	}
	if sel.Contract != "tasks/v1" {
		t.Errorf("contract = %q", sel.Contract)
	}
	files := []Source{
		{LayerRelease, "release/nscale.yml"},
		{LayerProject, ".devflow/providers/jira.yml"},
		{LayerUser, "~/.devflow/providers/jira.yaml"},
	}
	if !reflect.DeepEqual(c.Providers.Definitions, files) {
		t.Errorf("definitions = %v", c.Providers.Definitions)
	}
}

func TestDefinitionsOnlyApplyToSelectedProvider(t *testing.T) {
	root, _ := layers(t, jiraRelease)
	config(t, root, "devflow: 1\ntask_management: {provider: github-issues}\n")
	c, _, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers.TaskManagement.Sources) != 0 {
		t.Errorf("sources = %v", c.Providers.TaskManagement.Sources)
	}
}

const constrained = `version: 1
providers:
  jira:
    task_management:
      settings: {site: nscale.atlassian.net, projects: [PLAT]}
      constraints:
        site: {lock: true}
        projects: {allow: [PLAT, INFRA]}
  github-issues:
    task_management:
      settings: {labels: [triage]}
      constraints:
        labels: {lock: append}
        repository: {pattern: '^nscaledev/'}
`

func TestDefinitionConstraints(t *testing.T) {
	for _, tc := range []struct {
		name, project, config, user, want string
	}{
		{name: "lock allows same value", config: "provider: jira\n  settings: {site: nscale.atlassian.net}"},
		{name: "lock", user: "jira:\n    task_management:\n      settings: {site: other.atlassian.net}",
			want: "~/.devflow/providers/u.yml: task_management.settings.site is locked by release:release/nscale.yml"},
		{name: "allow", config: "provider: jira\n  settings: {projects: [PLAT, OPS]}",
			want: ".devflow/config.yml: task_management.settings.projects value OPS is not allowed"},
		{name: "allow passes", config: "provider: jira\n  settings: {projects: [INFRA]}"},
		{name: "append", project: "github-issues:\n    task_management:\n      settings: {labels: [bug]}", config: "provider: github-issues",
			want: ".devflow/providers/p.yml: task_management.settings.labels must keep triage"},
		{name: "append passes", project: "github-issues:\n    task_management:\n      settings: {labels: [triage, bug]}", config: "provider: github-issues"},
		{name: "pattern", config: "provider: github-issues\n  settings: {repository: other/repo}",
			want: "repository value other/repo must match ^nscaledev/"},
		{name: "pattern rejects explicit reset", config: "provider: github-issues\n  settings: {repository: null}", want: "repository value <nil> must match"},
		{name: "pattern passes", config: "provider: github-issues\n  settings: {repository: nscaledev/devflow}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, home := layers(t, constrained)
			if tc.project != "" {
				project(t, root, "p.yml", "version: 1\nproviders:\n  "+tc.project+"\n")
			}
			if tc.user != "" {
				user(t, home, "u.yml", "version: 1\nproviders:\n  "+tc.user+"\n")
			}
			cfg := tc.config
			if cfg == "" {
				cfg = "provider: jira"
			}
			config(t, root, "devflow: 1\ntask_management:\n  "+cfg+"\n")
			_, _, err := LoadRepo(root)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestInvalidDefinitions(t *testing.T) {
	for _, tc := range []struct{ name, file, want string }{
		{"version", "version: 2\nproviders: {}", "version: expected integer 1"},
		{"unknown field", "version: 1\nproviders: {}\nextra: 1", "field extra not found"},
		{"null settings", "version: 1\nproviders: {jira: {task_management: {settings: null}}}", "settings: expected an object"},
		{"trailing document", "version: 1\nproviders: {}\n---\nunknown: true", "expected exactly one YAML document"},
		{"unknown role", "version: 1\nproviders:\n  jira:\n    tasks: {}", "providers.jira.tasks: unknown role"},
		{"config-only provider", "version: 1\nproviders:\n  linear:\n    task_management: {}", "linear is not available for task_management"},
		{"contract", "version: 1\nproviders:\n  jira:\n    task_management: {contract: knowledge/v1}", "does not apply"},
		{"settings type", "version: 1\nproviders:\n  jira:\n    task_management:\n      settings: {site: 3}", "task_management.settings.site: expected string"},
		{"project constraints", "version: 1\nproviders:\n  jira:\n    task_management:\n      constraints: {site: {lock: true}}", "only release definitions may set constraints"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := layers(t, "")
			project(t, root, "bad.yml", tc.file)
			_, _, err := LoadRepo(root)
			var defErr *DefinitionError
			if !errors.As(err, &defErr) || defErr.Source.File != ".devflow/providers/bad.yml" || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestInvalidReleaseConstraints(t *testing.T) {
	for _, tc := range []struct{ constraint, want string }{
		{"nope: {lock: true}", `unknown settings property "nope"`},
		{"site: {lock: append}", "append applies only to lists"},
		{"site: {lock: maybe}", `expected true or "append"`},
		{"site: {pattern: '('}", "pattern:"},
		{"site: {}", "expected lock, allow or pattern"},
	} {
		t.Run(tc.constraint, func(t *testing.T) {
			root, _ := layers(t, "version: 1\nproviders:\n  jira:\n    task_management:\n      constraints: {"+tc.constraint+"}\n")
			if _, _, err := LoadRepo(root); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDuplicateDefinitionInLayer(t *testing.T) {
	root, _ := layers(t, "")
	def := "version: 1\nproviders:\n  jira:\n    task_management: {}\n"
	project(t, root, "a.yml", def)
	project(t, root, "b.yml", def)
	if _, _, err := LoadRepo(root); err == nil || !strings.Contains(err.Error(), "already defined in .devflow/providers/a.yml") {
		t.Fatalf("err = %v", err)
	}
}

func TestShippedReleaseDefinitionsLoad(t *testing.T) {
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	c, _, err := LoadRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := []Source{{LayerRelease, "release/providers.yml"}}
	if !reflect.DeepEqual(c.Providers.Definitions, want) {
		t.Fatalf("definitions = %v, want %v", c.Providers.Definitions, want)
	}
}

func TestConstraintsCheckMergedObjectsAndLiteralKeys(t *testing.T) {
	root, home := layers(t, `version: 1
providers:
  github-issues:
    task_management:
      constraints:
        search: {lock: true}
        'transitions.pr\.merged': {lock: true}
  slack:
    notifications:
      settings:
        native: {workspace_id: T0123456789, channel_id: C0123456789}
      constraints:
        native.channel_id: {lock: true}
`)
	config(t, root, "task_management: {provider: github-issues, settings: {search: {limit: 30}}}")
	c, _, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := c.Providers.TaskManagement.Explain()
	if err != nil {
		t.Fatal(err)
	}
	locked := map[string]any{}
	for _, s := range settings {
		if s.Constraint != nil {
			locked[s.Path] = s.Value
		}
	}
	if !reflect.DeepEqual(locked, map[string]any{`transitions.pr\.merged`: "closed", "search": map[string]any{"limit": 30, "open_only": true}}) {
		t.Fatalf("explained locks = %#v", locked)
	}
	for _, tc := range []struct{ def, want string }{
		{"github-issues: {task_management: {settings: {transitions: {pr.merged: open}}}}", `transitions.pr\.merged is locked`},
		{"slack: {notifications: {settings: {native: null}}}", "native.channel_id is locked"},
	} {
		user(t, home, "override.yml", "version: 1\nproviders:\n  "+tc.def)
		if _, _, err := LoadRepo(root); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want %q", tc.def, err, tc.want)
		}
	}
}

func TestReleaseConstraintsCheckLegacySettings(t *testing.T) {
	root, _ := layers(t, "version: 1\nproviders:\n  jira: {task_management: {constraints: {projects: {allow: [PLAT]}}}}")
	config(t, root, "jira: {projects: [OPS]}")
	if _, _, err := LoadRepo(root); err == nil || !strings.Contains(err.Error(), "value OPS is not allowed") {
		t.Fatalf("invalid inherited projects: %v", err)
	}
	config(t, root, "jira: {site: https://legacy.example, projects: [PLAT]}")
	c, _, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers.KnowledgeBase.Sources["site"] != configSource || c.Providers.TaskManagement.Sources["projects"] != configSource {
		t.Fatal("legacy settings lost their repository source")
	}
}

func TestRepositoryScopeSurvivesReleaseAndUserDefaults(t *testing.T) {
	root, home := layers(t, constrained)
	config(t, root, "jira: {projects: [INFRA]}")
	user(t, home, "user.yml", "version: 1\nproviders:\n  jira: {task_management: {settings: {projects: []}}}")
	c, _, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	s := c.Providers.TaskManagement
	if got := s.RepositorySettings("projects").(*JiraSettings).Projects; !reflect.DeepEqual(got, []string{"INFRA"}) {
		t.Fatalf("trusted projects %v", got)
	}
	if len(s.Settings.(*JiraSettings).Projects) != 0 || s.CheckTarget("projects", "OTHER") == nil || s.CheckTarget("projects", "INFRA") != nil {
		t.Fatal("user preference or release allowlist lost")
	}
}

func TestInlineNullSettingsAreRejected(t *testing.T) {
	root, _ := layers(t, "")
	config(t, root, "task_management: {settings: null}")
	if _, _, err := LoadRepo(root); err == nil || !strings.Contains(err.Error(), "task_management.settings: expected an object") {
		t.Fatal(err)
	}
	config(t, root, "task_management: {provider: github-issues, settings: {repository: null}}")
	if _, _, err := LoadRepo(root); err != nil {
		t.Fatal(err)
	}
}

func TestWinningDefinitionOwnsSemanticError(t *testing.T) {
	root, home := layers(t, "")
	config(t, root, "task_management: {provider: asana}")
	project(t, root, "project.yml", "version: 1\nproviders: {asana: {task_management: {settings: {project: '10'}}}}")
	user(t, home, "override.yml", "version: 1\nproviders: {asana: {task_management: {settings: {project: bad/project}}}}")
	_, _, err := LoadRepo(root)
	var def *DefinitionError
	if !errors.As(err, &def) || def.Source != (Source{LayerUser, "~/.devflow/providers/override.yml"}) || !strings.Contains(err.Error(), "task_management.settings.project") {
		t.Fatal(err)
	}
}
