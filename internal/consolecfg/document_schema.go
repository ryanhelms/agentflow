package consolecfg

import (
	"strings"

	"github.com/nscaledev/devflow/internal/commit"
	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/contract"
	"github.com/nscaledev/devflow/internal/flowmap"
	"github.com/nscaledev/devflow/internal/release"
	"github.com/nscaledev/devflow/internal/tasktemplate"
)

type fieldSpec = config.Field

func object(fields map[string]fieldSpec) fieldSpec {
	return fieldSpec{Type: "object", Properties: fields}
}
func dictionary(value fieldSpec, keys ...string) fieldSpec {
	return fieldSpec{Type: "object", AdditionalProperties: &value, KeySuggestions: keys}
}

type documentMappings struct {
	Space       string `yaml:"space"`
	TitlePrefix string `yaml:"title_prefix"`
	ParentID    string `yaml:"parent_id"`
	Pages       []struct {
		File     string `yaml:"file"`
		Title    string `yaml:"title"`
		ParentID string `yaml:"parent_id"`
	} `yaml:"pages"`
}
type environmentMappings struct {
	Deployments struct {
		Mapping map[string][]string `yaml:"environmentMapping"`
	} `yaml:"deployments"`
}

// refine attaches consumer rules that are not encoded by the Go field type.
func refine(s *fieldSpec, path []string, apply func(*fieldSpec)) {
	if len(path) == 0 {
		apply(s)
		return
	}
	if path[0] == "*" && s.Items != nil {
		refine(s.Items, path[1:], apply)
	} else if child, ok := s.Properties[path[0]]; ok {
		refine(&child, path[1:], apply)
		s.Properties[path[0]] = child
	}
}

func sourceSchema(kind string, value any, snap Snapshot) fieldSpec {
	stringField := fieldSpec{Type: "string"}
	required := func(f *fieldSpec) { f.Required = true; f.Nullable = false }
	suggest := func(names ...string) func(*fieldSpec) {
		return func(f *fieldSpec) { f.Suggestions = names }
	}
	enum := func(names ...string) func(*fieldSpec) {
		return func(f *fieldSpec) { f.Enum = names }
	}
	var s fieldSpec
	switch kind {
	case core:
		s = config.SchemaFor(config.Repo{}, "yaml")
		refine(&s, []string{"commit", "types", "*"}, suggest(config.DefaultTypes...))
		refine(&s, []string{"update", "channel"}, suggest("stable", "beta"))
		for _, key := range []string{"local", "remote"} {
			refine(&s, []string{"policy", "max_autonomy", key}, enum(string(config.Suggest), string(config.Confirm), string(config.Auto)))
		}
		data, _ := value.(map[string]any)
		roles := config.ProviderCatalog()
		devops := object(map[string]fieldSpec{"version": versionField()})
		for _, role := range roles {
			parent := data
			domain, sub, nested := strings.Cut(role.Role, ".")
			key := domain
			if nested {
				parent, _ = data[domain].(map[string]any)
				key = sub
			}
			fields, _ := parent[key].(map[string]any)
			id := role.Default
			if selected, ok := fields["provider"].(string); ok {
				id = selected
			}
			settings := providerSchema(snap, role.Role, id)
			section := object(map[string]fieldSpec{"version": versionField(),
				"provider": {Type: "string", Enum: role.Providers, Default: role.Default}, "settings": settings})
			if role.Role == "task_management" {
				tpl := config.SchemaFor(config.TaskTemplateSettings{}, "yaml")
				for _, path := range [][]string{{"catalog"}, {"default", "name"}, {"default", "revision"}} {
					refine(&tpl, path, required)
				}
				section.Properties["templates"] = tpl
			}
			if nested {
				devops.Properties[key] = section
			} else {
				s.Properties[key] = section
			}
		}
		s.Properties["devops"] = devops
	case def:
		providers := object(map[string]fieldSpec{})
		for _, role := range config.ProviderCatalog() {
			var ids []string
			for _, c := range contract.All() {
				if c.Role == role.Role {
					ids = append(ids, c.ID())
				}
			}
			for _, id := range role.Providers {
				if id == "none" {
					continue
				}
				definition, ok := providers.Properties[id]
				if !ok {
					definition = object(map[string]fieldSpec{})
				}
				definition.Properties[role.Role] = object(map[string]fieldSpec{
					"contract": {Type: "string", Suggestions: ids}, "settings": providerSchema(snap, role.Role, id),
				})
				providers.Properties[id] = definition
			}
		}
		s = object(map[string]fieldSpec{"providers": providers, "version": versionField()})
		refine(&s, []string{"version"}, required)
	case units:
		s = config.SchemaFor(release.Config{}, "yaml")
		for _, path := range [][]string{{"strategy"}, {"units"}, {"units", "*", "name"}, {"units", "*", "path"}, {"units", "*", "release_type"}} {
			refine(&s, path, required)
		}
		refine(&s, []string{"strategy"}, enum(release.Trunk, release.GitFlow))
		refine(&s, []string{"units", "*", "builds", "*"}, enum(release.Binary, release.Image, release.Helm, release.Library))
		refine(&s, []string{"units", "*", "release_type"}, suggest("go", "node", "python", "helm", "terraform-module", "simple"))
		refine(&s, []string{"units", "*", "publish"}, suggest("npm", "pypi", "go", "terraform"))
	case flow:
		nullable := fieldSpec{Type: "string", Nullable: true}
		project := dictionary(nullable, flowmap.BranchCreated, flowmap.PROpened, flowmap.PRMerged, flowmap.DeployStaging, flowmap.DeployProduction)
		project.Properties = map[string]fieldSpec{
			"verbs": dictionary(nullable, commit.Verbs...), "order": {Type: "array", Items: &stringField},
			"slack": stringField, "confluence": config.SchemaFor(flowmap.Project{}.Confluence, "yaml"),
		}
		s = object(map[string]fieldSpec{"version": {Type: "integer"}, "defaults": project, "projects": dictionary(project)})
	case catalog:
		s = config.SchemaFor(tasktemplate.Catalog{}, "json")
		s.Nullable = false
		for _, path := range [][]string{{"*", "name"}, {"*", "revision"}, {"*", "sections", "*", "heading"}} {
			refine(&s, path, required)
		}
		refine(&s, []string{"*", "required", "*"}, enum("scope", "title", "description", "type", "priority", "labels", "links"))
	case docs:
		s = config.SchemaFor(documentMappings{}, "yaml")
		for _, path := range [][]string{{"space"}, {"pages"}, {"pages", "*", "file"}} {
			refine(&s, path, required)
		}
	case envs:
		s = config.SchemaFor(environmentMappings{}, "yaml")
		refine(&s, []string{"deployments", "environmentMapping"}, func(f *fieldSpec) {
			f.Required = true
			f.Nullable = false
			f.KeySuggestions = []string{"development", "testing", "staging", "production"}
		})
	}
	return s
}

func versionField() fieldSpec {
	return fieldSpec{Type: "integer", Default: config.ProviderSchemaVersion, Nonnegative: true}
}

func providerSchema(snap Snapshot, role, id string) fieldSpec {
	field, ok := snap.Schemas[role][id]
	if !ok {
		return object(nil)
	}
	if snap.Providers == nil {
		return field
	}
	for _, active := range snap.Providers.Roles {
		if active.Role != role || active.Selected != id {
			continue
		}
		for _, setting := range active.Settings {
			if setting.Constraint == nil {
				continue
			}
			path, err := settingKeys(setting.Path)
			if err != nil {
				continue
			}
			// A constraint on one map key must not apply to its siblings.
			addConstraint(&field, path, setting.Constraint)
		}
	}
	return field
}

func addConstraint(field *fieldSpec, keys []string, c *config.Constraint) {
	if len(keys) == 0 {
		field.Constraint = c
		return
	}
	next, exists := field.Properties[keys[0]]
	if !exists {
		if field.AdditionalProperties == nil {
			return
		}
		next = *field.AdditionalProperties
	}
	// Clone the modified branch; the same reusable schema may have other owners.
	props := make(map[string]fieldSpec, len(field.Properties)+1)
	for k, v := range field.Properties {
		props[k] = v
	}
	field.Properties = props
	addConstraint(&next, keys[1:], c)
	field.Properties[keys[0]] = next
}
