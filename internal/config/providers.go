package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nscaledev/devflow/internal/gitx"
	"gopkg.in/yaml.v3"
)

// ProviderSchemaVersion versions the section and settings schemas together.
// Go types define property types, yaml tags name properties, allowed tags list
// enum values, and nonnegative tags constrain integers. Pointer fields default
// to unset and accept YAML null. Factories below supply role-specific defaults.
const ProviderSchemaVersion = 1

// Providers is the effective configuration for each independent role. A section's
// version selects the schema for both its envelope and its provider settings.
type Providers struct {
	CodeHosting    Selection
	TaskManagement Selection
	TaskTemplates  *TaskTemplateSettings
	KnowledgeBase  Selection
	DevOps         DevOps
	SecOps         Selection
	Notifications  Selection
}

type DevOps struct {
	Version     int
	Checks      Selection
	Deployments Selection
	Releases    Selection
}

// Selection.Settings is a pointer to the selected provider's settings type
// below. Configured reports whether the repo supplied this particular section,
// including an empty section or an explicit selection of its default provider.
type Selection struct {
	Version    int
	Provider   string
	Settings   any
	Configured bool
}

type DisabledSettings struct{}

type CodeHostingSettings struct {
	// Hosts maps a self-hosted hostname to its forge driver.
	Hosts        map[string]string `yaml:"hosts" allowed:"github,gitlab,bitbucket,azure-devops"`
	SourceRemote *string           `yaml:"source_remote"`
	TargetRemote *string           `yaml:"target_remote"`
}

type JiraSettings struct {
	Site     string   `yaml:"site"`
	Projects []string `yaml:"projects"`
}

type ConfluenceSettings struct {
	Site string `yaml:"site"`
}

type GitHubSettings struct {
	// An unset repository uses the repository selected by code-host detection.
	Repository *string `yaml:"repository"`
}

type GitHubSecOpsSettings struct {
	GitHubSettings `yaml:",inline"`
	Findings       []string               `yaml:"findings" allowed:"dependabot,code-scanning"`
	Incidents      *SecOpsIncidentFixture `yaml:"incidents"`
	MinSeverity    string                 `yaml:"min_severity" allowed:"low,medium,high,critical"`
}

type SecOpsIncidentFixture struct {
	Fixture string `yaml:"fixture"`
	Scope   string `yaml:"scope"`
}

type DevOpsFixtureSettings struct {
	Path string `yaml:"path"`
}

type GitHubWikiSettings struct {
	// An unset repository uses the GitHub.com target selected by forge detection.
	Repository *string `yaml:"repository"`
}

type GitHubIssuesSettings struct {
	GitHubSettings `yaml:",inline"`
	Labels         []string    `yaml:"labels"`
	Search         IssueSearch `yaml:"search"`
	// Events and commit verbs map to GitHub issue states. "none" disables a mapping.
	Transitions map[string]string `yaml:"transitions" allowed:"open,closed,none"`
}

type IssueSearch struct {
	OpenOnly bool `yaml:"open_only"`
	Limit    int  `yaml:"limit" nonnegative:"true"`
}

type FileSystemSettings struct {
	Root string `yaml:"root"`
}

type RepositorySettings struct {
	Repository  *string `yaml:"repository"` // unset uses the current repository
	Branch      *string `yaml:"branch"`     // unset uses its default branch
	Directory   string  `yaml:"directory"`
	Publication string  `yaml:"publication" allowed:"pull-request,push"`
}

// FileTaskSettings apply only to file-backed task management.
type FileTaskSettings struct {
	Scope       string            `yaml:"scope"`
	Transitions map[string]string `yaml:"transitions" allowed:"open,closed,none"`
}

type FileSystemTaskSettings struct {
	FileSystemSettings `yaml:",inline"`
	FileTaskSettings   `yaml:",inline"`
}

type RepositoryTaskSettings struct {
	RepositorySettings `yaml:",inline"`
	FileTaskSettings   `yaml:",inline"`
}

func defaultTransitions() map[string]string {
	return map[string]string{"pr.merged": "closed", "#done": "closed"}
}

type SlackSettings struct {
	Destination   *string `yaml:"destination"`
	CredentialRef *string `yaml:"credential_ref"`
}

// Each entry constructs an independent default settings object. Role registries
// can reuse constructors or supply their own defaults for a shared provider.
type settingsFactory func() any
type roleSchema struct {
	defaultProvider string
	providers       map[string]settingsFactory
}

func githubSettings() any { return &GitHubSettings{} }

func codeHostingSettings() any { return &CodeHostingSettings{Hosts: map[string]string{}} }

func resolveProviders(repo *Repo, extra map[string]yaml.Node) (Providers, error) {
	// Decode only the reserved sections. Unrelated top-level extensions keep
	// their legacy behavior. yaml.v3 handles YAML anchors and merge keys here.
	raw := map[string]any{}
	for _, key := range []string{"code_hosting", "task_management", "knowledge_base", "devops", "secops", "notifications"} {
		if node, ok := extra[key]; ok {
			var value any
			if err := node.Decode(&value); err != nil {
				return Providers{}, fmt.Errorf("%s: %w", key, err)
			}
			raw[key] = value
		}
	}

	const taskDir = ".devflow/tasks"
	fileDefaults := func() FileTaskSettings {
		return FileTaskSettings{Scope: "default", Transitions: defaultTransitions()}
	}
	tasks := map[string]settingsFactory{
		"filesystem": func() any {
			return &FileSystemTaskSettings{
				FileSystemSettings: FileSystemSettings{Root: taskDir}, FileTaskSettings: fileDefaults(),
			}
		},
		"repository": func() any {
			return &RepositoryTaskSettings{
				RepositorySettings: RepositorySettings{Directory: taskDir, Publication: "pull-request"},
				FileTaskSettings:   fileDefaults(),
			}
		},
	}
	tasks["jira"] = func() any {
		return &JiraSettings{Site: repo.Jira.Site, Projects: append([]string{}, repo.Jira.Projects...)}
	}
	tasks["github-issues"] = func() any {
		return &GitHubIssuesSettings{
			Labels:      []string{},
			Search:      IssueSearch{OpenOnly: true, Limit: 30},
			Transitions: defaultTransitions(),
		}
	}
	knowledge := map[string]settingsFactory{
		"filesystem": func() any { return &FileSystemSettings{Root: "docs"} },
		"repository": func() any { return &RepositorySettings{Directory: "docs", Publication: "pull-request"} },
	}
	knowledge["confluence"] = func() any { return &ConfluenceSettings{Site: repo.Confluence.Site} }
	knowledge["github-wiki"] = func() any { return &GitHubWikiSettings{} }
	github := roleSchema{"github", map[string]settingsFactory{"github": githubSettings}}
	devOpsFixture := roleSchema{"github", map[string]settingsFactory{
		"github": githubSettings, "fixture": func() any { return &DevOpsFixtureSettings{} },
	}}

	var p Providers
	taskManagement, present, err := section(raw, "task_management", "task_management", "version", "provider", "settings", "templates")
	if err != nil {
		return Providers{}, err
	}
	p.TaskManagement, err = (roleSchema{"jira", tasks}).resolve(taskManagement, present, "task_management")
	if err != nil {
		return Providers{}, err
	}
	if value, ok := taskManagement["templates"]; ok {
		p.TaskTemplates, err = parseTaskTemplates(value)
		if err != nil {
			return Providers{}, err
		}
	}
	devops, _, err := section(raw, "devops", "devops", "version", "checks", "deployments", "releases")
	if err != nil {
		return Providers{}, err
	}
	p.DevOps.Version, err = sectionVersion(devops, "devops")
	if err != nil {
		return Providers{}, err
	}
	for _, role := range []struct {
		parent map[string]any
		key    string
		path   string
		schema roleSchema
		out    *Selection
	}{
		{raw, "code_hosting", "code_hosting", roleSchema{"auto", map[string]settingsFactory{
			"auto": codeHostingSettings, "github": codeHostingSettings,
		}}, &p.CodeHosting},
		{raw, "knowledge_base", "knowledge_base", roleSchema{"confluence", knowledge}, &p.KnowledgeBase},
		{devops, "checks", "devops.checks", github, &p.DevOps.Checks},
		{devops, "deployments", "devops.deployments", devOpsFixture, &p.DevOps.Deployments},
		{devops, "releases", "devops.releases", devOpsFixture, &p.DevOps.Releases},
		{raw, "secops", "secops", roleSchema{"github", map[string]settingsFactory{
			"github": func() any {
				return &GitHubSecOpsSettings{Findings: []string{"dependabot", "code-scanning"}, MinSeverity: "high"}
			},
		}}, &p.SecOps},
		{raw, "notifications", "notifications", roleSchema{"slack", map[string]settingsFactory{
			"slack": func() any { return &SlackSettings{} },
		}}, &p.Notifications},
	} {
		fields, present, err := section(role.parent, role.key, role.path, "version", "provider", "settings")
		if err != nil {
			return Providers{}, err
		}
		*role.out, err = role.schema.resolve(fields, present, role.path)
		if err != nil {
			return Providers{}, err
		}
	}
	if wiki, ok := p.KnowledgeBase.Settings.(*GitHubWikiSettings); ok && wiki.Repository != nil {
		if err := gitx.ValidateGitHubSlug(*wiki.Repository); err != nil {
			return Providers{}, fmt.Errorf("knowledge_base.settings.repository: %w", err)
		}
	}
	if settings, ok := p.SecOps.Settings.(*GitHubSecOpsSettings); ok && settings.Incidents != nil {
		fixture := settings.Incidents
		if !filepath.IsLocal(fixture.Fixture) || filepath.Clean(fixture.Fixture) == "." {
			return Providers{}, fmt.Errorf("secops.settings.incidents.fixture: expected a path within the checkout")
		}
		if strings.TrimSpace(fixture.Scope) == "" {
			return Providers{}, fmt.Errorf("secops.settings.incidents.scope: expected a stable source scope")
		}
	}
	return p, nil
}

func section(parent map[string]any, key, path string, keys ...string) (map[string]any, bool, error) {
	value, present := parent[key]
	if !present {
		return nil, false, nil
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, true, fmt.Errorf("%s: expected an object with string keys", path)
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if !slices.Contains(keys, name) {
			return nil, true, fmt.Errorf("%s.%s: unknown property", path, name)
		}
	}
	return fields, true, nil
}

func sectionVersion(fields map[string]any, path string) (int, error) {
	if version, supplied := fields["version"]; supplied && version != ProviderSchemaVersion {
		return 0, fmt.Errorf("%s.version: expected integer %d", path, ProviderSchemaVersion)
	}
	return ProviderSchemaVersion, nil
}

func (s roleSchema) resolve(fields map[string]any, present bool, path string) (Selection, error) {
	version, err := sectionVersion(fields, path)
	if err != nil {
		return Selection{}, err
	}
	id := s.defaultProvider
	if value, ok := fields["provider"]; ok {
		id, _ = value.(string)
	}
	var settings any
	if id == "none" {
		settings = &DisabledSettings{}
	} else if factory, ok := s.providers[id]; ok {
		settings = factory()
	} else {
		choices := append(slices.Sorted(maps.Keys(s.providers)), "none")
		return Selection{}, fmt.Errorf("%s.provider: expected one of %s", path, strings.Join(choices, ", "))
	}
	if overrides, ok := fields["settings"]; ok {
		if err := applySettings(settings, overrides, path+".settings"); err != nil {
			return Selection{}, err
		}
	}
	return Selection{Version: version, Provider: id, Settings: settings, Configured: present}, nil
}
