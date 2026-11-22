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
	dir := filepath.Join(root, ".devflow")
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
		{p.CodeHosting, "auto"}, {p.TaskManagement, "jira"}, {p.KnowledgeBase, "confluence"},
		{p.DevOps.Checks, "github"}, {p.DevOps.Deployments, "github"}, {p.DevOps.Releases, "github"},
		{p.SecOps, "github"}, {p.Notifications, "slack"},
	} {
		if tc.got.Version != 1 || tc.got.Provider != tc.id || tc.got.Configured || tc.got.Settings == nil {
			t.Errorf("default selection for %s = %+v", tc.id, tc.got)
		}
	}
	if p.DevOps.Version != 1 || p.TaskManagement.Settings.(*JiraSettings).Site != DefaultSite ||
		p.KnowledgeBase.Settings.(*ConfluenceSettings).Site != DefaultSite || p.Notifications.Settings.(*SlackSettings).Destination != nil {
		t.Fatalf("unexpected defaults: %+v", p)
	}
	if got := loadConfig(t, "devflow: 1\nfuture_extension: {nested: true}\n"); !reflect.DeepEqual(got.Providers, p) {
		t.Fatalf("legacy config changed defaults: %+v", got.Providers)
	}
}

func TestLegacyProviderSettings(t *testing.T) {
	c := loadConfig(t, `
devflow: 1
jira: {site: "https://jira.example", projects: [OLD]}
commit: {require_key: false}
task_management: {settings: {site: "https://tasks.example"}}
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
		cfg := loadConfig(t, "jira: {projects: [OLD]}\ntask_management: {settings: {projects: "+tc.override+"}}")
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
	docs := c.Providers.KnowledgeBase.Settings.(*RepositorySettings)
	if docs.Repository == nil || *docs.Repository != "org/handbook" || docs.Branch != nil ||
		docs.Directory != "docs/adr" || docs.Publication != "pull-request" {
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
	tasks := p.TaskManagement.Settings.(*RepositorySettings)
	if tasks.Directory != ".devflow/tasks" || *tasks.Branch != "tasks" || tasks.Publication != "push" ||
		p.KnowledgeBase.Settings.(*FileSystemSettings).Root != "../handbook" {
		t.Fatalf("file-backed roles = %+v, %+v", tasks, p.KnowledgeBase)
	}
	c = loadConfig(t, "task_management: {provider: filesystem}\nknowledge_base: {provider: repository}")
	if c.Providers.TaskManagement.Settings.(*FileSystemSettings).Root != ".devflow/tasks" ||
		c.Providers.KnowledgeBase.Settings.(*RepositorySettings).Directory != "docs" {
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
		{"task_management: {settings: {projects: [OK, false]}}", "task_management.settings.projects[1]"},
		{"knowledge_base: {provider: repository, settings: {publication: overwrite}}", "knowledge_base.settings.publication"},
		{"knowledge_base: {provider: filesystem, settings: {root: null}}", "knowledge_base.settings.root"},
		{"code_hosting: {settings: {hosts: {forge: bogus}}}", "code_hosting.settings.hosts.forge"},
		{"devops: {version: '1'}", "devops.version"},
		{"devops: {check: {}}", "devops.check"},
		{"devops: {deployments: {driver: github}}", "devops.deployments.driver"},
		{"devops: {releases: {provider: none, settings: {repository: owner/repo}}}", "devops.releases.settings.repository"},
		{"secops: {settings: []}", "secops.settings"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			c, found, err := LoadRepo(writeConfig(t, tc.input))
			if c != nil || !found || err == nil || !strings.Contains(err.Error(), ".devflow/config.yml: "+tc.path+":") {
				t.Fatalf("want error at %s, got found=%v, err=%v", tc.path, found, err)
			}
		})
	}
}
