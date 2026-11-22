package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/nscaledev/devflow/internal/contract"
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
	// Definitions lists the provider definition files loaded, in layer order.
	Definitions []Source
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
	// Contract is the role contract the selection implements, e.g. tasks/v1.
	Contract string
	// Sources maps each settings path a layer set to that layer. Paths not
	// listed keep the provider default.
	Sources map[string]Source
	// Constraints are the release layer's limits on settings paths.
	Constraints map[string]Constraint
	// The pre-user snapshot supplies the write ceiling for targets explicitly
	// set by the repository. User defaults still participate in Settings.
	repositorySettings any
	repositorySources  map[string]Source
}

// RepositorySettings returns the pre-user settings if the repository supplied
// this property or any of its children, or the release locked the property.
func (s Selection) RepositorySettings(property string) any {
	if c := s.Constraints[property]; c.Lock == true {
		return s.repositorySettings
	}
	for path, source := range s.repositorySources {
		if source.Layer == LayerProject && (property == "" || path == property || strings.HasPrefix(path, property+".")) {
			return s.repositorySettings
		}
	}
	return nil
}

// CheckTarget applies release allow and pattern restrictions to a concrete
// operation target, including targets resolved from Git or supplied as input.
// Locks and append minima govern the setting itself during layer resolution.
func (s Selection) CheckTarget(property, value string) error {
	c, ok := s.Constraints[property]
	if !ok {
		return nil
	}
	c.Lock = nil
	return c.check(value, nil)
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
	Destination   *string              `yaml:"destination"`
	CredentialRef *string              `yaml:"credential_ref"`
	Native        *SlackNativeSettings `yaml:"native"`
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

// ProviderRoles lists every role path in the order setup reports them.
var ProviderRoles = []string{
	"code_hosting", "task_management", "knowledge_base", "devops.checks",
	"devops.deployments", "devops.releases", "secops", "notifications",
}

// RoleProviders describes the provider ids one role accepts. Providers is
// sorted and always ends with "none", which disables the role.
type RoleProviders struct {
	Role      string   `json:"role"`
	Default   string   `json:"default"`
	Providers []string `json:"providers"`
}

// ProviderCatalog lists the providers each role accepts, from the same
// registry that validates .devflow/config.yml.
func ProviderCatalog() []RoleProviders {
	schemas := providerSchemas()
	out := make([]RoleProviders, 0, len(ProviderRoles))
	for _, role := range ProviderRoles {
		s := schemas[role]
		out = append(out, RoleProviders{Role: role, Default: s.defaultProvider, Providers: s.choices()})
	}
	return out
}

func (s roleSchema) choices() []string {
	return append(slices.Sorted(maps.Keys(s.providers)), "none")
}

// providerSchemas is the provider registry, keyed by role path.
func providerSchemas() map[string]roleSchema {
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
	tasks["asana"] = func() any { return &AsanaSettings{} }
	tasks["jira"] = func() any {
		return &JiraSettings{Site: DefaultSite, Projects: []string{}}
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
	knowledge["confluence"] = func() any { return &ConfluenceSettings{Site: DefaultSite} }
	knowledge["github-wiki"] = func() any { return &GitHubWikiSettings{} }
	github := roleSchema{"github", map[string]settingsFactory{"github": githubSettings}}
	devOpsFixture := roleSchema{"github", map[string]settingsFactory{
		"github": githubSettings, "fixture": func() any { return &DevOpsFixtureSettings{} },
	}}
	return map[string]roleSchema{
		"code_hosting": {"auto", map[string]settingsFactory{
			"auto": codeHostingSettings, "github": codeHostingSettings,
		}},
		"task_management":    {"jira", tasks},
		"knowledge_base":     {"confluence", knowledge},
		"devops.checks":      github,
		"devops.deployments": devOpsFixture,
		"devops.releases":    devOpsFixture,
		"secops": {"github", map[string]settingsFactory{
			"github": func() any {
				return &GitHubSecOpsSettings{Findings: []string{"dependabot", "code-scanning"}, MinSeverity: "high"}
			},
		}},
		"notifications": {"slack", map[string]settingsFactory{
			"slack":   func() any { return &SlackSettings{} },
			"webhook": func() any { return &WebhookSettings{} },
			"router":  func() any { return &NotificationRoutes{} },
		}},
	}
}

func resolveProviders(repo *Repo, extra map[string]yaml.Node, root string) (Providers, error) {
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
	schemas := providerSchemas()
	defs, err := loadDefinitions(root, schemas)
	if err != nil {
		return Providers{}, err
	}

	p := Providers{Definitions: defs.files}
	taskManagement, present, err := section(raw, "task_management", "task_management", "version", "provider", "settings", "templates")
	if err != nil {
		return Providers{}, err
	}
	p.TaskManagement, err = schemas["task_management"].resolve(taskManagement, present, "task_management", defs, repo)
	if err != nil {
		return Providers{}, err
	}
	if settings, ok := p.TaskManagement.Settings.(*AsanaSettings); ok {
		if err := settings.validate(); err != nil {
			return Providers{}, err
		}
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
		out    *Selection
	}{
		{raw, "code_hosting", "code_hosting", &p.CodeHosting},
		{raw, "knowledge_base", "knowledge_base", &p.KnowledgeBase},
		{devops, "checks", "devops.checks", &p.DevOps.Checks},
		{devops, "deployments", "devops.deployments", &p.DevOps.Deployments},
		{devops, "releases", "devops.releases", &p.DevOps.Releases},
		{raw, "secops", "secops", &p.SecOps},
		{raw, "notifications", "notifications", &p.Notifications},
	} {
		fields, present, err := section(role.parent, role.key, role.path, "version", "provider", "settings")
		if err != nil {
			return Providers{}, err
		}
		*role.out, err = schemas[role.path].resolve(fields, present, role.path, defs, repo)
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
	return p, validateNotificationSettings(p.Notifications)
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

func (s roleSchema) resolve(fields map[string]any, present bool, path string, defs *definitions, repo *Repo) (Selection, error) {
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
		return Selection{}, fmt.Errorf("%s.provider: expected one of %s", path, strings.Join(s.choices(), ", "))
	}
	sel := Selection{Version: version, Provider: id, Settings: settings, Configured: present}
	if id == "none" {
		// A disabled role accepts no settings; this reports any it is given.
		if overrides, ok := fields["settings"]; ok {
			return sel, applySettings(settings, overrides, path+".settings")
		}
		return sel, nil
	}
	err = sel.applyLayers(fields, path, defs, repo)
	return sel, err
}

var configSource = Source{Layer: LayerProject, File: ".devflow/config.yml"}

// Legacy fields seed the role before the release defaults. Preserve their
// provenance even when the value happens to equal a compiled default.
func (repo *Repo) legacySettings(id string) map[string]any {
	values := map[string]any{}
	switch id {
	case "jira":
		if repo.legacyJiraSite {
			values["site"] = repo.Jira.Site
		}
		if repo.Jira.Projects != nil {
			items := make([]any, len(repo.Jira.Projects))
			for i, project := range repo.Jira.Projects {
				items[i] = project
			}
			values["projects"] = items
		}
	case "confluence":
		if repo.legacyConfluenceSite {
			values["site"] = repo.Confluence.Site
		}
	}
	return values
}

func (sel *Selection) applyLayers(fields map[string]any, path string, defs *definitions, repo *Repo) error {
	sel.Sources = map[string]Source{}
	c, err := contract.Resolve(path, "")
	if err != nil {
		return err
	}
	sel.Contract = c.ID()
	release, _ := defs.get(LayerRelease, sel.Provider, path)
	sel.Constraints = release.Constraints
	var baseline map[string]any
	if len(sel.Constraints) != 0 {
		// Locks bind the compiled and release values, independent of legacy
		// repository settings used to seed the effective configuration.
		settings := providerSchemas()[path].providers[sel.Provider]()
		if release.Settings != nil {
			if err := applySettings(settings, release.Settings, path+".settings"); err != nil {
				return err
			}
		}
		if baseline, err = plain(settings); err != nil {
			return err
		}
	}
	fromDefinition := func(layer string) error {
		def, ok := defs.get(layer, sel.Provider, path)
		if !ok {
			return nil
		}
		if def.Contract != "" {
			sel.Contract = def.Contract // validated when the definition was read
		}
		if def.Settings == nil {
			return nil
		}
		if err := sel.applyLayer(def.Settings, def.source, path); err != nil {
			return &DefinitionError{Source: def.source, Err: err}
		}
		if layer != LayerRelease {
			if err := sel.checkConstraints(baseline, path); err != nil {
				return &DefinitionError{Source: def.source, Err: err}
			}
		}
		return nil
	}
	legacy := repo.legacySettings(sel.Provider)
	if err := sel.applyLayer(legacy, configSource, path); err != nil {
		return err
	}
	if err := fromDefinition(LayerRelease); err != nil {
		return err
	}
	if err := sel.checkConstraints(baseline, path); err != nil {
		return err
	}
	if err := fromDefinition(LayerProject); err != nil {
		return err
	}
	if overrides, ok := fields["settings"]; ok {
		if err := sel.applyLayer(overrides, configSource, path); err != nil {
			return err
		}
		if err := sel.checkConstraints(baseline, path); err != nil {
			return err
		}
	}
	// A release default can supersede an old top-level field for preferences.
	// The explicit repository target still bounds writes until the repository
	// specifies a new target in a project file or inline role setting.
	sel.repositorySettings = copySettings(sel.Settings)
	sel.repositorySources = maps.Clone(sel.Sources)
	for key, value := range legacy {
		if sel.Sources[key].Layer == LayerRelease {
			if err := applySettings(sel.repositorySettings, map[string]any{key: value}, path+".settings"); err != nil {
				return err
			}
			sel.repositorySources[key] = configSource
		}
	}
	return fromDefinition(LayerUser)
}

// mergeSetting replaces modified maps, pointers and slices, so untouched
// branches can be shared by these read-only snapshots.
func copySettings(settings any) any {
	current := reflect.ValueOf(settings).Elem()
	copy := reflect.New(current.Type())
	copy.Elem().Set(current)
	return copy.Interface()
}

func (sel *Selection) applyLayer(raw any, src Source, path string) error {
	if err := applySettings(sel.Settings, raw, path+".settings"); err != nil {
		return err
	}
	leaves := map[string]any{}
	flatten("", raw, leaves)
	delete(leaves, "")
	for leaf := range leaves {
		sel.Sources[leaf] = src
	}
	return nil
}

func (sel Selection) checkConstraints(baseline map[string]any, path string) error {
	if baseline == nil {
		return nil
	}
	current, err := plain(sel.Settings)
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(sel.Constraints)) {
		con := sel.Constraints[name]
		value, present := lookup(current, con.parts)
		base, wasPresent := lookup(baseline, con.parts)
		if con.Lock == true && present != wasPresent {
			return fmt.Errorf("%s.settings.%s is locked by %s", path, name, con.source)
		}
		if value == nil && base == nil {
			continue // an optional property has no configured value
		}
		if err := con.check(value, base); err != nil {
			return fmt.Errorf("%s.settings.%s %w by %s", path, name, err, con.source)
		}
	}
	return nil
}
