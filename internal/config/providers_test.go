package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".agentflow")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func loadConfig(t *testing.T, contents string) *Repo {
	t.Helper()
	c, found, err := LoadRepo(writeConfig(t, contents))
	if err != nil || !found {
		t.Fatalf("LoadRepo: found %v, err %v", found, err)
	}
	return c
}

func TestProviderDefaults(t *testing.T) {
	c, found, err := LoadRepo(t.TempDir())
	if err != nil || found {
		t.Fatalf("missing config: found %v, err %v", found, err)
	}
	p := c.Providers
	for _, tc := range []struct {
		got Selection
		id  string
	}{
		{p.CodeHosting, "auto"}, {p.TaskManagement, "none"}, {p.KnowledgeBase, "none"},
		{p.DevOps.Checks, "github"}, {p.DevOps.Deployments, "github"}, {p.DevOps.Releases, "github"},
		{p.SecOps, "github"}, {p.Notifications, "slack"},
	} {
		if tc.got.Version != 1 || tc.got.Provider != tc.id || tc.got.Configured || tc.got.Settings == nil {
			t.Errorf("default selection for %s = %+v", tc.id, tc.got)
		}
	}
	if p.DevOps.Version != 1 || c.Commit.KeyRequired() || p.Notifications.Settings.(*SlackSettings).Destination != nil {
		t.Fatalf("unexpected defaults: %+v", p)
	}
	if got := loadConfig(t, "agentflow: 1\nfuture_extension: {nested: true}\n"); !reflect.DeepEqual(got.Providers, p) {
		t.Fatalf("legacy config changed defaults: %+v", got.Providers)
	}
}

func TestUnassignedProvidersKeepLegacyAndInlineSettingsDormant(t *testing.T) {
	cfg := loadConfig(t, `
jira: {site: https://jira.example, projects: [OPS]}
confluence: {site: https://wiki.example}
task_management: {settings: {projects: [OPS]}}
knowledge_base: {settings: {site: https://other.example}}
`)
	for _, selection := range []Selection{cfg.Providers.TaskManagement, cfg.Providers.KnowledgeBase} {
		if selection.Provider != "none" {
			t.Fatalf("settings unexpectedly activated a provider: %+v", selection)
		}
		if _, ok := selection.Settings.(*DisabledSettings); !ok {
			t.Fatalf("unassigned role has active settings: %+v", selection)
		}
	}
	if cfg.Commit.KeyRequired() {
		t.Fatal("unassigned tasks implicitly require a ticket key")
	}

	tasks := loadConfig(t, "task_management: {provider: jira}")
	if tasks.Providers.TaskManagement.Provider != "jira" || tasks.Providers.KnowledgeBase.Provider != "none" || !tasks.Commit.KeyRequired() {
		t.Fatalf("explicit task role changed its independent defaults: %+v", tasks.Providers)
	}
	knowledge := loadConfig(t, "knowledge_base: {provider: confluence}")
	if knowledge.Providers.KnowledgeBase.Provider != "confluence" || knowledge.Providers.TaskManagement.Provider != "none" || knowledge.Commit.KeyRequired() {
		t.Fatalf("explicit knowledge role changed its independent defaults: %+v", knowledge.Providers)
	}
}

func TestLegacyProviderSettings(t *testing.T) {
	c := loadConfig(t, `
agentflow: 1
jira: {site: "https://jira.example", projects: [OLD]}
commit: {require_key: false}
task_management: {provider: jira, settings: {site: "https://tasks.example"}}
knowledge_base: {provider: confluence}
`)
	task := c.Providers.TaskManagement.Settings.(*JiraSettings)
	if !c.Providers.TaskManagement.Configured || task.Site != "https://tasks.example" || !slices.Equal(task.Projects, []string{"OLD"}) {
		t.Fatalf("overlaid Jira: %+v", task)
	}
	if c.Jira.Site != "https://jira.example" || c.Commit.KeyRequired() || c.Confluence.Site != c.Jira.Site ||
		c.Providers.KnowledgeBase.Settings.(*ConfluenceSettings).Site != c.Jira.Site {
		t.Fatalf("changed legacy fields or knowledge defaults: %+v", c)
	}
	task.Projects[0] = "NEW"
	if c.Jira.Projects[0] != "OLD" {
		t.Fatal("resolved settings share a slice with legacy config")
	}

	for _, tc := range []struct {
		override string
		want     []string
	}{
		{"[NEW]", []string{"NEW"}}, {"[]", []string{}},
	} {
		cfg := loadConfig(t, "jira: {projects: [OLD]}\ntask_management: {provider: jira, settings: {projects: "+tc.override+"}}")
		if got := cfg.Providers.TaskManagement.Settings.(*JiraSettings).Projects; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("projects = %#v, want %#v", got, tc.want)
		}
	}
}

func TestProviderSwitchAndObjectOverrides(t *testing.T) {
	c := loadConfig(t, `
jira: {site: old, projects: [OLD]}
confluence: {site: old-docs}
task_management:
  version: 1
  provider: github-issues
  settings:
    repository: ""
    search: {open_only: false}
knowledge_base:
  provider: repository
  settings: {directory: docs/adr, repository: org/handbook, branch: null}
`)
	tasks := c.Providers.TaskManagement.Settings.(*GitHubIssuesSettings)
	if tasks.Repository == nil || *tasks.Repository != "" || tasks.Search.OpenOnly || tasks.Search.Limit != 30 || tasks.Labels == nil {
		t.Fatalf("github-issues settings = %+v", tasks)
	}
	docs := c.Providers.KnowledgeBase.Settings.(*RepositoryKnowledgeSettings)
	if docs.Repository == nil || *docs.Repository != "org/handbook" || docs.Branch != nil ||
		docs.Directory != "docs/adr" || docs.Publication != "pull-request" || docs.Format != "okf" {
		t.Fatalf("repository settings = %+v", docs)
	}

	c = loadConfig(t, "task_management: {provider: github-issues, settings: {search: {limit: 0}}}")
	if got := c.Providers.TaskManagement.Settings.(*GitHubIssuesSettings); got.Search.Limit != 0 || !got.Search.OpenOnly || got.Repository != nil {
		t.Fatalf("single-leaf override: %+v", got)
	}
}

func TestIndependentRoles(t *testing.T) {
	c := loadConfig(t, `
code_hosting:
  settings:
    hosts: {git.example.org: gitlab}
    source_remote: fork
    target_remote: canonical
devops:
  version: 1
  checks: {provider: none}
  deployments: {version: 1, settings: {repository: org/environments}}
secops: {}
notifications: {provider: none}
task_management: {provider: repository, settings: {branch: tasks, publication: push}}
knowledge_base: {provider: filesystem, settings: {root: ../handbook}}
`)
	p := c.Providers
	host := p.CodeHosting.Settings.(*CodeHostingSettings)
	if p.CodeHosting.Provider != "auto" || host.Hosts["git.example.org"] != "gitlab" ||
		*host.SourceRemote != "fork" || *host.TargetRemote != "canonical" {
		t.Fatalf("code_hosting = %+v", host)
	}
	if p.DevOps.Checks.Provider != "none" || p.DevOps.Releases.Configured || p.DevOps.Releases.Settings.(*GitHubSettings).Repository != nil ||
		*p.DevOps.Deployments.Settings.(*GitHubSettings).Repository != "org/environments" ||
		p.Notifications.Provider != "none" || p.SecOps.Provider != "github" || !p.SecOps.Configured {
		t.Fatalf("independent selections = %+v", p)
	}
	tasks := p.TaskManagement.Settings.(*RepositoryTaskSettings)
	if tasks.Directory != ".agentflow/tasks" || *tasks.Branch != "tasks" || tasks.Publication != "push" ||
		p.KnowledgeBase.Settings.(*FileSystemKnowledgeSettings).Root != "../handbook" ||
		p.KnowledgeBase.Settings.(*FileSystemKnowledgeSettings).Format != "okf" {
		t.Fatalf("file-backed roles = %+v, %+v", tasks, p.KnowledgeBase)
	}
	c = loadConfig(t, "task_management: {provider: filesystem}\nknowledge_base: {provider: repository}")
	if c.Providers.TaskManagement.Settings.(*FileSystemTaskSettings).Root != ".agentflow/tasks" ||
		c.Providers.KnowledgeBase.Settings.(*RepositoryKnowledgeSettings).Directory != "docs" {
		t.Fatal("both roles must support each file-backed provider with its own defaults")
	}
}

func TestProviderSettingsAcrossLoads(t *testing.T) {
	root := writeConfig(t, `
common: &github {provider: github, settings: {repository: org/shared}}
devops:
  checks: *github
  deployments: {<<: *github, settings: {repository: org/deploy}}
`)
	first, _, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	checks := first.Providers.DevOps.Checks.Settings.(*GitHubSettings)
	deployments := first.Providers.DevOps.Deployments.Settings.(*GitHubSettings)
	if *checks.Repository != "org/shared" || *deployments.Repository != "org/deploy" {
		t.Fatalf("unexpected repositories %q and %q", *checks.Repository, *deployments.Repository)
	}
	*checks.Repository = "org/changed"
	first.Providers.CodeHosting.Settings.(*CodeHostingSettings).Hosts["example.org"] = "github"
	second, _, err := LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if *second.Providers.DevOps.Checks.Settings.(*GitHubSettings).Repository != "org/shared" ||
		len(second.Providers.CodeHosting.Settings.(*CodeHostingSettings).Hosts) != 0 {
		t.Fatal("mutating one load changed another")
	}
}

func TestInvalidProviderConfig(t *testing.T) {
	for _, tc := range []struct{ input, path string }{
		{"code_hosting: {version: 2}", "code_hosting.version"},
		{"notifications: null", "notifications"},
		{"notifications: {destination: room}", "notifications.destination"},
		{"notifications: {settings: {credential_ref: true}}", "notifications.settings.credential_ref"},
		{"task_management: {provider: GitHubIssues}", "task_management.provider"},
		{"knowledge_base: {provider: github-issues}", "knowledge_base.provider"},
		{"task_management: {provider: github-issues, settings: {projects: []}}", "task_management.settings.projects"},
		{"task_management: {provider: github-issues, settings: {search: {limit: -1}}}", "task_management.settings.search.limit"},
		{"task_management: {provider: github-issues, settings: {search: {open_only: 'false'}}}", "task_management.settings.search.open_only"},
		{"task_management: {provider: jira, settings: {projects: [OK, false]}}", "task_management.settings.projects[1]"},
		{"knowledge_base: {provider: repository, settings: {publication: overwrite}}", "knowledge_base.settings.publication"},
		{"knowledge_base: {provider: filesystem, settings: {root: null}}", "knowledge_base.settings.root"},
		{"knowledge_base: {provider: filesystem, settings: {format: other}}", "knowledge_base.settings.format"},
		{"knowledge_base: {provider: repository, settings: {format: null}}", "knowledge_base.settings.format"},
		{"knowledge_base: {provider: github-wiki, settings: {format: legacy}}", "knowledge_base.settings.format"},
		{"knowledge_base: {provider: confluence, settings: {format: okf}}", "knowledge_base.settings.format"},
		{"task_management: {provider: filesystem, settings: {format: okf}}", "task_management.settings.format"},
		{"task_management: {provider: repository, settings: {format: legacy}}", "task_management.settings.format"},
		{"knowledge_base: {provider: github-wiki, settings: {origin: /local/wiki.git}}", "knowledge_base.settings.origin"},
		{"knowledge_base: {provider: github-wiki, settings: {repository: 123}}", "knowledge_base.settings.repository"},
		{"knowledge_base: {provider: github-wiki, settings: {repository: ''}}", "knowledge_base.settings.repository"},
		{"knowledge_base: {provider: github-wiki, settings: {repository: https://github.com/owner/repo}}", "knowledge_base.settings.repository"},
		{"knowledge_base: {provider: github-wiki, settings: {repository: owner/..}}", "knowledge_base.settings.repository"},
		{"code_hosting: {settings: {hosts: {forge: bogus}}}", "code_hosting.settings.hosts.forge"},
		{"devops: {version: '1'}", "devops.version"},
		{"devops: {check: {}}", "devops.check"},
		{"devops: {deployments: {driver: github}}", "devops.deployments.driver"},
		{"devops: {releases: {provider: none, settings: {repository: owner/repo}}}", "devops.releases.settings.repository"},
		{"secops: {settings: []}", "secops.settings"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			c, found, err := LoadRepo(writeConfig(t, tc.input))
			if c != nil || !found || err == nil || !strings.Contains(err.Error(), ".agentflow/config.yml: "+tc.path+":") {
				t.Fatalf("want error at %s, got found=%v, err=%v", tc.path, found, err)
			}
		})
	}
}

func TestGitHubWikiSettings(t *testing.T) {
	const section = "knowledge_base: {provider: github-wiki"
	for _, overrides := range []string{"", ", settings: {}", ", settings: {repository: null}"} {
		got := loadConfig(t, section+overrides+"}").Providers.KnowledgeBase
		settings, ok := got.Settings.(*GitHubWikiSettings)
		if !ok || settings.Repository != nil || got.Version != 1 || !got.Configured || got.Provider != "github-wiki" {
			t.Fatalf("default wiki settings (%q): %+v", overrides, got)
		}
	}
	cfg := loadConfig(t, section+", settings: {repository: Docs-Team/Runbooks.v2}}")
	if got := cfg.Providers.KnowledgeBase.Settings.(*GitHubWikiSettings).Repository; got == nil || *got != "Docs-Team/Runbooks.v2" {
		t.Fatalf("wiki repository: %v", got)
	}
}

func TestGitHubIssueTransitions(t *testing.T) {
	cfg := loadConfig(t, `task_management:
  provider: github-issues
  settings:
    transitions: {pr.merged: none, deploy.production: closed, "#start": open}
`)
	got := cfg.Providers.TaskManagement.Settings.(*GitHubIssuesSettings).Transitions
	if got["pr.merged"] != "none" || got["#done"] != "closed" || got["deploy.production"] != "closed" || got["#start"] != "open" {
		t.Fatalf("transitions = %v", got)
	}
	root := writeConfig(t, "task_management: {provider: github-issues, settings: {transitions: {pr.merged: done}}}")
	if _, _, err := LoadRepo(root); err == nil || !strings.Contains(err.Error(), "task_management.settings.transitions.pr.merged") {
		t.Fatalf("invalid state: %v", err)
	}
}

func TestAsanaScopeAndTransitions(t *testing.T) {
	cfg := loadConfig(t, `task_management:
  provider: asana
  settings:
    project: "10"
    transitions:
      "10": {"#qa": "12", pr.opened: "12", "#done": completed}
      "20": {branch.created: "21", pr.merged: none}
`)
	settings := cfg.Providers.TaskManagement.Settings.(*AsanaSettings)
	want := map[string][]string{"10": {"12"}, "20": {"21"}}
	if !reflect.DeepEqual(settings.AdapterConfig().Sections, want) || settings.HasProject("30") || cfg.Commit.KeyRequired() {
		t.Fatalf("settings %+v", settings)
	}
	for _, bad := range []string{"project: two/projects", "transitions: {'10': {'#qa': '../12'}}", "transitions: {'10': {'#qa': open}}"} {
		_, _, err := LoadRepo(writeConfig(t, "task_management: {provider: asana, settings: {"+bad+"}}"))
		if err == nil || !strings.Contains(err.Error(), "task_management.settings") {
			t.Fatalf("invalid %s: %v", bad, err)
		}
	}
}

func TestProviderCatalog(t *testing.T) {
	catalog := ProviderCatalog()
	if len(catalog) != len(ProviderRoles) {
		t.Fatalf("catalog has %d roles, want %d", len(catalog), len(ProviderRoles))
	}
	for i, r := range catalog {
		if r.Role != ProviderRoles[i] {
			t.Errorf("role %d = %s, want %s", i, r.Role, ProviderRoles[i])
		}
		if !slices.Contains(r.Providers, r.Default) {
			t.Errorf("%s default %q is not an option: %v", r.Role, r.Default, r.Providers)
		}
		if r.Providers[len(r.Providers)-1] != "none" {
			t.Errorf("%s options do not end with none: %v", r.Role, r.Providers)
		}
	}
	if got := catalog[1].Providers; !slices.Equal(got, []string{"asana", "filesystem", "github-issues", "jira", "repository", "none"}) {
		t.Errorf("task_management providers = %v", got)
	}
}
