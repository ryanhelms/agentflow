package manifest_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

const richSettings = `schema: 1
id: rich
name: Rich plugin
version: 1.2.3
tier: declarative
permissions:
  subscribe: [git.**]
  invoke: [comment]
extensions:
  handler:
    - name: any-git
      on: [git.**]
      actions:
        - invoke: {command: comment, input: {text: "{{event.type}}"}}
settings:
  - {key: loud, type: boolean, default: true, label: Loud, description: Shout.}
  - {key: mode, type: string, enum: [fast, slow], default: slow, label: Mode, description: Pick one.}
  - {key: name, type: string, label: Name, description: A name., advanced: true}
  - {key: wait, type: string, widget: duration, default: 5m, min_duration: 1m, max_duration: 1h, label: Wait, description: How long.}
  - {key: count, type: integer, min: 1, max: 9, default: 3, label: Count, description: How many., applies: restart}
  - {key: tags, type: string-list, default: [a, b], label: Tags, description: Labels.}
`

func installed(t *testing.T, texts ...string) []manifest.Installed {
	t.Helper()
	var out []manifest.Installed
	for _, text := range texts {
		m := mustLoad(t, text)
		out = append(out, manifest.Installed{ID: m.ID, Version: m.Version, Tier: m.Tier, Manifest: m})
	}
	return out
}

func TestCatalogFieldsOfASample(t *testing.T) {
	fields := manifest.CatalogFields(installed(t, ciNotify))
	if len(fields) != 2 {
		t.Fatalf("%d fields, want the toggle and one setting", len(fields))
	}
	enabled, signal := fields[0], fields[1]
	if enabled.ID != "personal:plugins.ci-notify.enabled" || enabled.Layer != config.FieldPersonal || enabled.Path != "plugins.ci-notify.enabled" {
		t.Errorf("enabled = %+v", enabled)
	}
	m := enabled.Meta
	if m.Label != "Enabled" || m.Group != "plugins.ci-notify" || m.Widget != config.WidgetToggle || m.Applies != config.AppliesLive || m.Loosens != config.LoosensEnable {
		t.Errorf("enabled meta = %+v", m)
	}
	if enabled.Schema.Type != "boolean" || enabled.Schema.Default != false {
		t.Errorf("enabled schema = %+v", enabled.Schema)
	}
	if signal.ID != "personal:plugins.ci-notify.settings.signal" || signal.Schema.Type != "string" || signal.Schema.Default != "ci.failed" {
		t.Errorf("signal = %+v", signal)
	}
	if m := signal.Meta; m.Label != "Notification signal" || m.Widget != config.WidgetText || m.Group != "plugins.ci-notify" || m.Loosens != "" || m.Order <= enabled.Meta.Order {
		t.Errorf("signal meta = %+v", m)
	}
}

func TestCatalogFieldsOfEveryWidget(t *testing.T) {
	fields := manifest.CatalogFields(installed(t, richSettings))
	byKey := map[string]config.CatalogField{}
	for _, f := range fields {
		byKey[strings.TrimPrefix(f.Path, "plugins.rich.")] = f
	}
	for key, want := range map[string]struct {
		widget config.Widget
		typ    string
	}{
		"enabled": {config.WidgetToggle, "boolean"}, "settings.loud": {config.WidgetToggle, "boolean"},
		"settings.mode": {config.WidgetSelect, "string"}, "settings.name": {config.WidgetText, "string"},
		"settings.wait": {config.WidgetDuration, "string"}, "settings.count": {config.WidgetStepper, "integer"},
		"settings.tags": {config.WidgetChips, "array"},
	} {
		f, ok := byKey[key]
		if !ok || f.Meta.Widget != want.widget || f.Schema.Type != want.typ {
			t.Errorf("%s = %+v, %v; want widget %s, type %s", key, f.Meta.Widget, ok, want.widget, want.typ)
		}
	}
	if m := byKey["settings.mode"]; !slices.Equal(m.Schema.Enum, []string{"fast", "slow"}) || m.Schema.Default != "slow" {
		t.Errorf("mode schema = %+v", m.Schema)
	}
	if n := byKey["settings.name"]; !n.Meta.Advanced {
		t.Error("advanced was lost")
	}
	if w := byKey["settings.wait"]; w.Meta.MinDuration != "1m" || w.Meta.MaxDuration != "1h" {
		t.Errorf("wait meta = %+v", w.Meta)
	}
	c := byKey["settings.count"]
	if c.Meta.Min == nil || *c.Meta.Min != 1 || c.Meta.Max == nil || *c.Meta.Max != 9 || c.Meta.Step != 1 || c.Meta.Applies != config.AppliesRestart || !c.Schema.Nonnegative || c.Schema.Default != 3 {
		t.Errorf("count = %+v / %+v", c.Meta, c.Schema)
	}
	if tags := byKey["settings.tags"]; tags.Schema.Items == nil || tags.Schema.Items.Type != "string" || !slices.Equal(tags.Schema.Default.([]string), []string{"a", "b"}) {
		t.Errorf("tags schema = %+v", tags.Schema)
	}
}

// TestCatalogFieldsFollowTheControlRules applies the rules of
// TestEveryConfigFieldHasControl (internal/config) to the fields plugins add.
func TestCatalogFieldsFollowTheControlRules(t *testing.T) {
	list := installed(t, ciNotify, richSettings, timelineHints)
	groups := map[string]bool{}
	for _, g := range config.Groups() {
		groups[g.ID] = true
	}
	orders := map[int]string{}
	for _, g := range manifest.Groups(list) {
		if groups[g.ID] {
			t.Errorf("group %s is also a compiled group", g.ID)
		}
		groups[g.ID] = true
		if g.Label == "" {
			t.Errorf("group %s has no label", g.ID)
		}
		if g.Order <= config.GroupOrderPlugins {
			t.Errorf("group %s (order %d) is not after the plugins group", g.ID, g.Order)
		}
		if other, dup := orders[g.Order]; dup {
			t.Errorf("group %s has the order of %s", g.ID, other)
		}
		orders[g.Order] = g.ID
	}
	seen := map[string]bool{}
	labels := map[string]bool{}
	for _, f := range manifest.CatalogFields(list) {
		if seen[f.ID] {
			t.Errorf("%s is listed twice", f.ID)
		}
		seen[f.ID] = true
		if layer, path, err := config.ParseFieldID(f.ID); err != nil || layer != config.FieldPersonal || path != f.Path || f.ID != config.FieldID(f.Layer, f.Path) {
			t.Errorf("%s: bad ID, layer or path (%v)", f.ID, err)
		}
		m := f.Meta
		if m.Label == "" || m.Description == "" || !groups[m.Group] || m.Order <= 0 {
			t.Errorf("%s: incomplete meta %+v", f.ID, m)
		}
		if lk := m.Group + "|" + m.Label; labels[lk] {
			t.Errorf("%s: the label %q repeats in group %s", f.ID, m.Label, m.Group)
		} else {
			labels[lk] = true
		}
		if !slices.Contains(config.Widgets, m.Widget) || !suits(m.Widget, f.Schema) {
			t.Errorf("%s: widget %s does not suit %+v", f.ID, m.Widget, f.Schema)
		}
		switch m.Applies {
		case config.AppliesLive, config.AppliesRestart:
		default:
			t.Errorf("%s: applies %q", f.ID, m.Applies)
		}
		if m.Sensitive || m.Picker != nil {
			t.Errorf("%s: a plugin field is never sensitive and has no picker", f.ID)
		}
		if m.Widget == config.WidgetStepper && (m.Min == nil || f.Schema.Nonnegative && *m.Min < 0 || m.Max != nil && *m.Max < *m.Min) {
			t.Errorf("%s: bad stepper limits %+v", f.ID, m)
		}
		if m.Widget != config.WidgetStepper && (m.Min != nil || m.Max != nil || m.Step != 0) {
			t.Errorf("%s: only a stepper has limits", f.ID)
		}
		if m.Widget != config.WidgetDuration && (m.MinDuration != "" || m.MaxDuration != "") {
			t.Errorf("%s: only a duration has duration limits", f.ID)
		}
		isToggle := strings.HasSuffix(f.Path, ".enabled") && m.Loosens == config.LoosensEnable && m.Widget == config.WidgetToggle
		if m.Loosens != config.LoosensNone && !isToggle {
			t.Errorf("%s: only the enabled toggle loosens", f.ID)
		}
		if len(f.Columns) != 0 {
			t.Errorf("%s: plugin fields have no columns", f.ID)
		}
	}
}

func suits(w config.Widget, s config.Field) bool {
	switch s.Type {
	case "boolean":
		return w == config.WidgetToggle
	case "integer":
		return w == config.WidgetStepper
	case "string":
		if len(s.Enum) > 0 {
			return w == config.WidgetSelect
		}
		return w == config.WidgetText || w == config.WidgetDuration
	case "array":
		return s.Items != nil && s.Items.Type == "string" && w == config.WidgetChips
	}
	return false
}

func TestCatalogFieldsSkipAnInvalidSetting(t *testing.T) {
	list := installed(t, ciNotify)
	list[0].Manifest.Settings = append(list[0].Manifest.Settings,
		manifest.Setting{Key: "token", Type: manifest.SettingString, Widget: config.WidgetCredentialRef, Label: "Token", Description: "x"})
	fields := manifest.CatalogFields(list)
	if len(fields) != 2 {
		t.Errorf("%d fields: a setting with a credential control must not reach the page", len(fields))
	}
}

func TestCatalogFieldsSurviveTheRegistry(t *testing.T) {
	list := installed(t, richSettings)
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	var back []manifest.Installed
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(manifest.CatalogFields(list))
	got, _ := json.Marshal(manifest.CatalogFields(back))
	if string(want) != string(got) {
		t.Errorf("the catalog changes after the registry's JSON round trip:\n%s\n%s", want, got)
	}
}

func TestGroupsAreSortedAndLabelled(t *testing.T) {
	list := installed(t, timelineHints, ciNotify)
	groups := manifest.Groups(list)
	if len(groups) != 2 || groups[0].ID != "plugins.ci-notify" || groups[0].Label != "CI failure notifications" || groups[1].ID != "plugins.timeline-hints" {
		t.Fatalf("groups = %+v", groups)
	}
	if groups[0].Order >= groups[1].Order {
		t.Errorf("orders %d, %d", groups[0].Order, groups[1].Order)
	}
	if len(manifest.Groups(nil)) != 0 || len(manifest.CatalogFields(nil)) != 0 {
		t.Error("no plugins, no groups or fields")
	}
}

func TestCatalogFieldsReturnFreshValues(t *testing.T) {
	list := installed(t, richSettings)
	first := manifest.CatalogFields(list)
	for i := range first {
		first[i].Meta.Label = "changed"
		if first[i].Meta.Min != nil {
			*first[i].Meta.Min = 99
		}
		first[i].Schema.Enum = append(first[i].Schema.Enum, "changed")
	}
	second := manifest.CatalogFields(list)
	for _, f := range second {
		if f.Meta.Label == "changed" || (f.Meta.Min != nil && *f.Meta.Min == 99) || slices.Contains(f.Schema.Enum, "changed") {
			t.Errorf("%s: changing a result changed the next call", f.ID)
		}
	}
	if list[0].Manifest.Settings[4].Min == nil || *list[0].Manifest.Settings[4].Min != 1 {
		t.Error("the manifest's own limits were changed")
	}
}
