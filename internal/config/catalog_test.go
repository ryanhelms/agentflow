package config

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func catalogByID(fields []CatalogField) map[string][]CatalogField {
	out := map[string][]CatalogField{}
	for _, f := range fields {
		out[f.ID] = append(out[f.ID], f)
	}
	return out
}

func TestCatalogListsFieldsOfBothLayers(t *testing.T) {
	byID := catalogByID(Catalog())
	for _, id := range []string{
		"personal:autonomy.local",
		"personal:autonomy.actions",
		"personal:approval_window",
		"personal:update.auto",
		"personal:atlassian.email",
		"personal:init.auto",
		"personal:init.orgs",
		"personal:worktree.root",
		"personal:worktree.checkouts",
		"repo:policy.max_autonomy.local",
		"repo:policy.max_autonomy.actions",
		"repo:policy.max_approval_window",
		"repo:policy.protected_branches",
		"repo:commit.types",
		"repo:commit.subject_max",
		"repo:update.track",
		"repo:jira.site",
		"repo:flow_map",
		"repo:worktree.root",
		"repo:worktree.go_work.use",
		"repo:worktree.go_work.siblings",
		// Provider selections, one for each role, devops roles included.
		"repo:task_management.provider",
		"repo:devops.checks.provider",
		"repo:notifications.provider",
		// A provider role's settings sit under "<role>.settings".
		"repo:task_management.settings.repository",
		"repo:devops.deployments.settings.path",
		"repo:notifications.settings.native.workspace_id",
		"repo:secops.settings.incidents.fixture",
		// Task templates do not depend on the provider.
		"repo:task_management.templates.catalog",
		"repo:task_management.templates.default.name",
		"repo:task_management.templates.default.revision",
	} {
		if len(byID[id]) == 0 {
			t.Errorf("the catalog has no field %s", id)
		}
	}
	for id, fields := range byID {
		layer, path, err := ParseFieldID(id)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		for _, f := range fields {
			if f.Layer != layer || f.Path != path {
				t.Errorf("%s: layer %q and path %q do not match the ID", id, f.Layer, f.Path)
			}
		}
	}
}

func TestCatalogLeavesOutFormatMarkers(t *testing.T) {
	byID := catalogByID(Catalog())
	for _, m := range catalogFormatMarkers {
		if m.Reason == "" {
			t.Errorf("%s: a format marker states a reason", FieldID(m.Layer, m.Path))
		}
		if id := FieldID(m.Layer, m.Path); len(byID[id]) != 0 {
			t.Errorf("%s is a format marker and must not be a field", id)
		}
	}
	for _, f := range Catalog() {
		if strings.HasSuffix(f.Path, ".version") && f.Provider == "" {
			t.Errorf("%s: a section version is a format marker", f.ID)
		}
	}
	// The marker list names each role's version, and the top-level one.
	for _, want := range []string{"agentflow", "devops.version", "task_management.version", "devops.checks.version"} {
		if !slices.ContainsFunc(catalogFormatMarkers, func(m formatMarker) bool { return m.Layer == FieldRepo && m.Path == want }) {
			t.Errorf("repo:%s is not in the format markers", want)
		}
	}
}

// TestCatalogCoversEveryCompiledProviderField walks the provider schemas
// independently of the catalog and expects a field for each setting.
func TestCatalogCoversEveryCompiledProviderField(t *testing.T) {
	type key struct{ id, provider string }
	have := map[key]bool{}
	for _, f := range Catalog() {
		have[key{f.ID, f.Provider}] = true
	}

	var leaves func(prefix []string, f Field, fn func([]string))
	leaves = func(prefix []string, f Field, fn func([]string)) {
		if f.Type == "object" && f.AdditionalProperties == nil {
			for name, p := range f.Properties {
				leaves(append(slices.Clone(prefix), name), p, fn)
			}
			return
		}
		fn(prefix)
	}

	schemas := ProviderFieldSchemas()
	count := 0
	for _, role := range ProviderCatalog() {
		if !have[key{FieldID(FieldRepo, role.Role+".provider"), ""}] {
			t.Errorf("role %s has no provider field", role.Role)
		}
		for _, id := range role.Providers {
			if id == "none" {
				continue
			}
			leaves(strings.Split(role.Role+".settings", "."), schemas[role.Role][id], func(path []string) {
				count++
				want := key{FieldID(FieldRepo, FormatFieldPath(path...)), id}
				if !have[want] {
					t.Errorf("no field %s for provider %s", want.id, id)
				}
			})
		}
	}
	if count == 0 {
		t.Fatal("no provider settings found")
	}
}

func TestCatalogFieldsAreSorted(t *testing.T) {
	order := map[string]int{}
	for _, g := range Groups() {
		order[g.ID] = g.Order
	}
	var prev CatalogField
	for i, f := range Catalog() {
		if i > 0 {
			switch {
			case order[f.Meta.Group] < order[prev.Meta.Group]:
				t.Errorf("%s (group %s) comes after %s (group %s)", f.ID, f.Meta.Group, prev.ID, prev.Meta.Group)
			case f.Meta.Group == prev.Meta.Group && f.Meta.Order < prev.Meta.Order:
				t.Errorf("%s (order %d) comes after %s (order %d) in group %s", f.ID, f.Meta.Order, prev.ID, prev.Meta.Order, f.Meta.Group)
			}
		}
		prev = f
	}
	if !reflect.DeepEqual(Catalog(), Catalog()) {
		t.Error("two calls return different catalogs")
	}
}

func TestGroupsAreOrderedAndIncludeEachRole(t *testing.T) {
	groups := Groups()
	ids := make([]string, len(groups))
	for i, g := range groups {
		ids[i] = g.ID
		if i > 0 && g.Order <= groups[i-1].Order {
			t.Errorf("group %s has order %d, not after %s (%d)", g.ID, g.Order, groups[i-1].ID, groups[i-1].Order)
		}
	}
	// The ADR's initial groups come in this order, with the role groups after "providers".
	want := []string{"providers"}
	for _, role := range ProviderRoles {
		want = append(want, "providers."+role)
	}
	want = append(want, "autonomy", "policy", "commit", "worktrees", "integrations", "updates", "init", "plugins")
	if !slices.Equal(ids, want) {
		t.Errorf("groups = %v\nwant     %v", ids, want)
	}
}

// TestCatalogReturnsFreshValues checks that a caller can change what Catalog
// returns without changing the next call.
func TestCatalogReturnsFreshValues(t *testing.T) {
	first := Catalog()
	want, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		f := &first[i]
		f.Meta.Label = "changed"
		if f.Meta.Min != nil {
			*f.Meta.Min = 999
		}
		if f.Meta.Picker != nil {
			f.Meta.Picker.Role = "changed"
		}
		for name, c := range f.Columns {
			c.Label = "changed"
			f.Columns[name] = c
		}
		f.Schema.Enum = append(f.Schema.Enum, "changed")
		f.Schema.KeySuggestions = append(f.Schema.KeySuggestions, "changed")
	}
	_ = Groups()
	got, err := json.Marshal(Catalog())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Error("changing the result of Catalog changed the next call")
	}
}

// TestCatalogLeavesSchemasAlone checks that config.Field and the schemas the
// agentflow_config output uses are not changed by the catalog: its choices
// are added to a copy.
func TestCatalogLeavesSchemasAlone(t *testing.T) {
	snapshot := func() string {
		b, err := json.Marshal(map[string]any{
			"user": SchemaFor(User{}, "yaml"), "repo": SchemaFor(Repo{}, "yaml"), "providers": ProviderFieldSchemas(),
			"templates": SchemaFor(TaskTemplateSettings{}, "yaml"),
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	before := snapshot()
	_ = Catalog()
	if snapshot() != before {
		t.Error("building the catalog changed a schema")
	}
	if got := SchemaFor(User{}, "yaml").Properties["autonomy"].Properties["local"].Enum; len(got) != 0 {
		t.Errorf("the personal autonomy level schema has choices %v; the catalog adds them to its own copy", got)
	}
}

// TestCatalogSchemasAreTheCompiledOnes checks that a catalog schema is the
// node SchemaFor builds, except for the choices the table adds.
func TestCatalogSchemasAreTheCompiledOnes(t *testing.T) {
	table := metaTable()
	raw := map[string]Field{}
	for _, f := range compiledFields() {
		raw[f.ID+"|"+f.Provider] = f.Schema
	}
	for _, f := range Catalog() {
		keys, _ := ParseFieldPath(f.Path)
		e := bestEntry(table, f.Layer, keys, f.Provider)
		if e == nil {
			t.Fatalf("%s: no table entry", fieldName(f))
		}
		if len(e.enum)+len(e.itemSuggestions)+len(e.valueEnum)+len(e.keySuggestions) > 0 {
			continue
		}
		if want := raw[f.ID+"|"+f.Provider]; !reflect.DeepEqual(f.Schema, want) {
			t.Errorf("%s: schema differs from the compiled one:\n got %+v\nwant %+v", fieldName(f), f.Schema, want)
		}
	}
}

// TestCatalogAddsChoicesTheTypesLack checks the choices the compiled types do
// not carry: autonomy levels, action names and the update channel.
func TestCatalogAddsChoicesTheTypesLack(t *testing.T) {
	byID := catalogByID(Catalog())
	one := func(id string) CatalogField {
		t.Helper()
		if len(byID[id]) != 1 {
			t.Fatalf("%s: %d fields, want 1", id, len(byID[id]))
		}
		return byID[id][0]
	}
	levels := []string{"suggest", "confirm", "auto"}
	for _, id := range []string{"personal:autonomy.local", "personal:autonomy.remote", "repo:policy.max_autonomy.local", "repo:policy.max_autonomy.remote"} {
		if got := one(id).Schema.Enum; !slices.Equal(got, levels) {
			t.Errorf("%s: choices = %v, want %v", id, got, levels)
		}
	}
	for _, id := range []string{"personal:autonomy.actions", "repo:policy.max_autonomy.actions"} {
		f := one(id)
		if !slices.Equal(f.Schema.AdditionalProperties.Enum, levels) {
			t.Errorf("%s: value choices = %v, want %v", id, f.Schema.AdditionalProperties.Enum, levels)
		}
		if !slices.Equal(f.Schema.KeySuggestions, AutonomyActions()) {
			t.Errorf("%s: key suggestions are not the autonomy actions", id)
		}
	}
	if got := one("repo:update.channel").Schema.Enum; !slices.Equal(got, []string{"stable", "beta"}) {
		t.Errorf("update channel choices = %v", got)
	}
	if got := one("repo:commit.types").Schema.Items.Suggestions; !slices.Equal(got, DefaultTypes) {
		t.Errorf("commit type suggestions = %v, want the defaults", got)
	}
	picker := one("repo:task_management.provider")
	if want := []string{"asana", "filesystem", "github-issues", "jira", "repository", "none"}; !slices.Equal(picker.Schema.Enum, want) {
		t.Errorf("task provider choices = %v, want %v", picker.Schema.Enum, want)
	}
	if picker.Schema.Default != "none" {
		t.Errorf("task provider default = %v, want none", picker.Schema.Default)
	}
}

// TestCatalogHasColumns checks the fields with columns: a kv-table whose
// value is an object has one for each property, and nested objects split into
// their properties by path.
func TestCatalogHasColumns(t *testing.T) {
	byID := catalogByID(Catalog())
	columns := func(id string) []string {
		t.Helper()
		if len(byID[id]) != 1 {
			t.Fatalf("%s: %d fields, want 1", id, len(byID[id]))
		}
		return slices.Sorted(maps.Keys(byID[id][0].Columns))
	}
	if got, want := columns("repo:worktree.go_work.siblings"), []string{"path", "repo"}; !slices.Equal(got, want) {
		t.Errorf("go.work siblings columns = %v, want %v", got, want)
	}
	if got, want := columns("repo:notifications.settings.destinations"), []string{
		"credential_ref", "header_refs", "native.actor_id", "native.channel_id", "native.server", "native.workspace_id", "provider", "rejected_statuses",
	}; !slices.Equal(got, want) {
		t.Errorf("destinations columns = %v, want %v", got, want)
	}
	// A value that is a map or a list has no properties, so no columns.
	for _, id := range []string{"repo:notifications.settings.routes", "repo:task_management.settings.transitions", "repo:code_hosting.settings.hosts"} {
		for _, f := range byID[id] {
			if len(f.Columns) != 0 {
				t.Errorf("%s: has columns %v", fieldName(f), slices.Sorted(maps.Keys(f.Columns)))
			}
		}
	}
	// A column takes its group and applies from the field.
	f := byID["repo:worktree.go_work.siblings"][0]
	for name, c := range f.Columns {
		if c.Group != f.Meta.Group || c.Applies != f.Meta.Applies {
			t.Errorf("column %s: group %q and applies %q, want %q and %q", name, c.Group, c.Applies, f.Meta.Group, f.Meta.Applies)
		}
	}
}

// TestCatalogJSONUsesADRNames checks the JSON of one field against the
// shapes the ADR gives.
func TestCatalogJSONUsesADRNames(t *testing.T) {
	var local CatalogField
	for _, f := range Catalog() {
		if f.ID == "personal:autonomy.local" {
			local = f
		}
	}
	b, err := json.Marshal(local)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "personal:autonomy.local" || got["layer"] != "personal" || got["path"] != "autonomy.local" {
		t.Errorf("field = %v", got)
	}
	meta, _ := got["meta"].(map[string]any)
	for key, want := range map[string]any{
		"label":       "Local actions",
		"group":       "autonomy",
		"widget":      "select",
		"applies":     "live",
		"loosens":     "level",
		"description": "Highest autonomy for local actions such as commit, in every repository.",
	} {
		if meta[key] != want {
			t.Errorf("meta.%s = %v, want %v", key, meta[key], want)
		}
	}
	schema, _ := got["schema"].(map[string]any)
	if schema["type"] != "string" {
		t.Errorf("schema = %v", schema)
	}
}
