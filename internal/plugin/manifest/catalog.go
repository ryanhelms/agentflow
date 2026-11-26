package manifest

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/internal/config"
)

// groupStep spaces the groups of installed plugins after the compiled
// "plugins" group (config.GroupOrderPlugins).
const groupStep = 1

// Groups returns the Settings groups the installed plugins add: one group
// "plugins.<id>" for each, labelled with the plugin's name, in ID order after
// the compiled "plugins" group. The settings page needs no change for them.
func Groups(installed []Installed) []config.Group {
	out := make([]config.Group, 0, len(installed))
	for i, p := range sortedByID(installed) {
		label := strings.TrimSpace(p.Manifest.Name)
		if label == "" {
			label = p.ID
		}
		out = append(out, config.Group{ID: GroupPrefix + p.ID, Label: label, Order: config.GroupOrderPlugins + groupStep*(i+1)})
	}
	return out
}

// CatalogFields returns the Settings catalog fields the installed plugins
// add (ADR, "Phase 5 settings"): for each plugin the personal toggle
// plugins.<id>.enabled, which loosens policy when it goes from false to
// true, and one field for each of its settings, plugins.<id>.settings.<key>.
// A setting that does not pass SettingProblems is left out, so a registry
// that was edited by hand cannot put an unusable control on the page.
//
// The fields are sorted by plugin ID, then their order in the group. Each
// call returns new values the caller may change.
func CatalogFields(installed []Installed) []config.CatalogField {
	var out []config.CatalogField
	for _, p := range sortedByID(installed) {
		group := GroupPrefix + p.ID
		out = append(out, config.CatalogField{
			ID:     config.FieldID(config.FieldPersonal, fmt.Sprintf(EnabledPathFormat, p.ID)),
			Layer:  config.FieldPersonal,
			Path:   fmt.Sprintf(EnabledPathFormat, p.ID),
			Schema: config.Field{Type: "boolean", Default: false},
			Meta: config.FieldMeta{
				Label:       enabledLabel,
				Description: "Runs this plugin in the console. A plugin stays off until you turn it on, and it can do only what its permissions allow.",
				Group:       group,
				Order:       10,
				Widget:      config.WidgetToggle,
				Applies:     config.AppliesLive,
				Loosens:     config.LoosensEnable,
			},
		})
		n := 1
		for _, s := range p.Manifest.Settings {
			if len(SettingProblems(s)) > 0 {
				continue
			}
			n++
			out = append(out, settingField(p.ID, group, 10*n, s))
		}
	}
	return out
}

func sortedByID(installed []Installed) []Installed {
	out := slices.Clone(installed)
	slices.SortStableFunc(out, func(a, b Installed) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// settingField is the catalog field of one valid setting.
func settingField(id, group string, order int, s Setting) config.CatalogField {
	path := fmt.Sprintf(SettingPathFormat, id, s.Key)
	w := SettingWidget(s)
	meta := config.FieldMeta{
		Label:       s.Label,
		Description: s.Description,
		Group:       group,
		Order:       order,
		Widget:      w,
		Applies:     s.Applies,
		Advanced:    s.Advanced,
	}
	if meta.Applies == "" {
		meta.Applies = config.AppliesLive
	}
	schema := config.Field{}
	switch s.Type {
	case SettingBoolean:
		schema.Type = "boolean"
	case SettingString:
		schema.Type = "string"
		schema.Enum = slices.Clone(s.Enum)
		if w == config.WidgetDuration {
			meta.MinDuration, meta.MaxDuration = s.MinDuration, s.MaxDuration
		}
	case SettingInteger:
		schema.Type = "integer"
		lo := *s.Min
		meta.Min, meta.Step = &lo, 1
		if s.Max != nil {
			hi := *s.Max
			meta.Max = &hi
		}
		schema.Nonnegative = lo >= 0
	case SettingStringList:
		schema.Type = "array"
		schema.Items = &config.Field{Type: "string"}
	}
	if d, ok := SettingDefault(s); ok && d != nil {
		schema.Default = d
	}
	return config.CatalogField{
		ID:     config.FieldID(config.FieldPersonal, path),
		Layer:  config.FieldPersonal,
		Path:   path,
		Schema: schema,
		Meta:   meta,
	}
}
