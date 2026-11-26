package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestEveryConfigFieldHasControl keeps the settings catalog complete (ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md, "Phase 4
// Field metadata"). A new configuration field needs an entry in
// field_meta_table.go; an entry for a field that no longer exists is removed;
// and each entry follows the rules in catalogProblems. Every message names
// the field ID.
func TestEveryConfigFieldHasControl(t *testing.T) {
	table := metaTable()
	fields := buildCatalog(table)
	if len(fields) == 0 {
		t.Fatal("the catalog is empty")
	}
	for _, p := range catalogProblems(fields, table, Groups()) {
		t.Error(p)
	}
}

// loosening is the ADR's list of fields where a change can loosen policy
// ("Phase 4 preview", "Loosening"), with how each one loosens. Only these set
// FieldMeta.Loosens.
var loosening = map[string]Loosening{
	"personal:autonomy.local":               LoosensLevel,
	"personal:autonomy.remote":              LoosensLevel,
	"personal:autonomy.actions":             LoosensLevel,
	"personal:approval_window":              LoosensLonger,
	"repo:policy.max_autonomy.local":        LoosensLevel,
	"repo:policy.max_autonomy.remote":       LoosensLevel,
	"repo:policy.max_autonomy.actions":      LoosensLevel,
	"repo:policy.max_approval_window":       LoosensLonger,
	"repo:policy.allow_auto_pr_publication": LoosensEnable,
	"repo:policy.coordinator_merge":         LoosensEnable,
	"repo:policy.protected_branches":        LoosensShrink,
	"repo:plugins.disabled":                 LoosensShrink,
}

// approvalWindows are the duration fields whose limits the code checks
// (validateAutonomy): MinApprovalWindow to MaxApprovalWindow.
var approvalWindows = []string{"personal:approval_window", "repo:policy.max_approval_window"}

// fieldName names a field in a message: its ID, and its provider when it has one.
func fieldName(f CatalogField) string {
	if f.Provider != "" {
		return f.ID + " (provider " + f.Provider + ")"
	}
	return f.ID
}

// catalogProblems checks a catalog against its metadata table and the rules
// of the ADR, and returns one message for each broken rule. It returns
// nothing when the catalog is complete.
func catalogProblems(fields []CatalogField, table []metaEntry, groups []Group) []string {
	var problems []string
	addf := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	groupIDs := map[string]bool{}
	for _, g := range groups {
		groupIDs[g.ID] = true
	}

	// The table: no entry twice.
	seenEntries := map[string]bool{}
	for i := range table {
		key := table[i].describe()
		if seenEntries[key] {
			addf("%s: more than one table entry", key)
		}
		seenEntries[key] = true
	}

	// Each field has an entry, and the entry is the one it uses.
	won := map[*metaEntry]bool{}
	seen := map[string]bool{}
	labels := map[string]string{}
	pickers := map[string]bool{}
	for _, f := range fields {
		name := fieldName(f)
		if seen[name] {
			addf("%s: listed twice in the catalog", name)
		}
		seen[name] = true
		if f.ID != FieldID(f.Layer, f.Path) {
			addf("%s: ID does not match layer %q and path %q", name, f.Layer, f.Path)
		}
		if _, _, err := ParseFieldID(f.ID); err != nil {
			addf("%s: %v", name, err)
		}
		keys, err := ParseFieldPath(f.Path)
		if err != nil {
			addf("%s: %v", name, err)
		}
		entry := bestEntry(table, f.Layer, keys, f.Provider)
		if entry == nil {
			addf("%s: no table entry in field_meta_table.go", name)
			continue
		}
		won[entry] = true

		problems = append(problems, controlProblems(name, f.Meta, f.Schema, groupIDs)...)
		problems = append(problems, fieldProblems(name, f, keys)...)
		if f.Meta.Widget == WidgetProviderPicker {
			pickers[f.Path] = true
		}
		if f.Meta.Label != "" {
			unique := f.Meta.Group + "|" + f.Provider + "|" + f.Meta.Label
			if first, dup := labels[unique]; dup {
				addf("%s: the label %q is already used by %s in group %q", name, f.Meta.Label, first, f.Meta.Group)
			}
			labels[unique] = name
		}
	}

	// No entry is left over.
	for i := range table {
		if !won[&table[i]] {
			addf("%s: stale table entry: it matches no field of the catalog", table[i].describe())
		}
	}

	// Each provider role has its picker and its group.
	for _, role := range ProviderRoles {
		if !pickers[role+".provider"] {
			addf("repo:%s.provider: the role has no provider-picker", role)
		}
		if !groupIDs[roleGroup(role)] {
			addf("%s: no group %q in Groups()", role, roleGroup(role))
		}
	}

	// Groups are named and ordered.
	ids := map[string]bool{}
	orders := map[int]string{}
	for _, g := range groups {
		if strings.TrimSpace(g.Label) == "" {
			addf("group %q: empty label", g.ID)
		}
		if ids[g.ID] {
			addf("group %q: listed twice in Groups()", g.ID)
		}
		ids[g.ID] = true
		if other, dup := orders[g.Order]; dup {
			addf("group %q: same order as %q", g.ID, other)
		}
		orders[g.Order] = g.ID
	}
	return problems
}

// controlProblems checks the rules that hold for any control: a field, or a
// column of a list or kv-table.
func controlProblems(name string, m FieldMeta, schema Field, groupIDs map[string]bool) []string {
	var problems []string
	addf := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf("%s: "+format, append([]any{name}, args...)...))
	}

	if strings.TrimSpace(m.Label) == "" {
		addf("empty label")
	}
	if strings.TrimSpace(m.Description) == "" {
		addf("empty description")
	}
	switch {
	case m.Group == "":
		addf("empty group")
	case !groupIDs[m.Group]:
		addf("group %q is not in Groups()", m.Group)
	}
	switch m.Applies {
	case "":
		addf("empty applies")
	case AppliesLive, AppliesNextRun, AppliesRestart, AppliesNewWorktrees:
	default:
		addf("unknown applies %q", m.Applies)
	}
	switch m.Loosens {
	case LoosensNone, LoosensLevel, LoosensLonger, LoosensEnable, LoosensShrink:
	default:
		addf("unknown loosens %q", m.Loosens)
	}

	if !slices.Contains(Widgets, m.Widget) {
		addf("unknown widget %q", m.Widget)
		return problems
	}
	if !widgetSuits(m.Widget, schema) {
		addf("widget %q does not suit the schema node (%s)", m.Widget, describeSchema(schema))
	}

	// Stepper limits.
	if m.Widget == WidgetStepper {
		switch {
		case m.Min == nil:
			addf("stepper has no min")
		case schema.Nonnegative && *m.Min < 0:
			addf("min %d is below zero but the field is nonnegative", *m.Min)
		case m.Max != nil && *m.Max < *m.Min:
			addf("max %d is below min %d", *m.Max, *m.Min)
		}
		if m.Step < 0 {
			addf("negative step %d", m.Step)
		}
	} else if m.Min != nil || m.Max != nil || m.Step != 0 {
		addf("only a stepper has min, max or step")
	}

	// Duration limits.
	if m.Widget == WidgetDuration {
		lo, loErr := parseLimit(m.MinDuration)
		hi, hiErr := parseLimit(m.MaxDuration)
		switch {
		case loErr != nil:
			addf("min_duration %q: %v", m.MinDuration, loErr)
		case hiErr != nil:
			addf("max_duration %q: %v", m.MaxDuration, hiErr)
		case m.MinDuration != "" && m.MaxDuration != "" && lo > hi:
			addf("min_duration %q is longer than max_duration %q", m.MinDuration, m.MaxDuration)
		}
	} else if m.MinDuration != "" || m.MaxDuration != "" {
		addf("only a duration has min_duration or max_duration")
	}

	// Pickers.
	p := m.Picker
	switch m.Widget {
	case WidgetProviderPicker:
		if p == nil || p.Role == "" {
			addf("provider-picker names no role")
		} else if !slices.Contains(ProviderRoles, p.Role) {
			addf("provider-picker names unknown role %q", p.Role)
		}
	case WidgetBranchPicker:
		switch {
		case schema.Type == "array" && (p == nil || !p.Multiple):
			addf("branch-picker for a list needs picker.multiple")
		case schema.Type == "string" && p != nil && p.Multiple:
			addf("branch-picker for one branch must not set picker.multiple")
		}
	case WidgetFilePicker:
		switch {
		case p == nil:
			addf("file-picker has no picker")
		case p.Kind != PickFile && p.Kind != PickDir:
			addf("file-picker kind %q is not %q or %q", p.Kind, PickFile, PickDir)
		case p.Base != "repo" && p.Base != "home":
			addf("file-picker base %q is not \"repo\" or \"home\"", p.Base)
		}
	default:
		if p != nil {
			addf("only a provider, branch or file picker has a picker")
		}
	}
	return problems
}

// fieldProblems checks the rules that need the whole field: its ID, its
// columns and the ADR's lists of loosening, restart and new-worktrees fields.
func fieldProblems(name string, f CatalogField, keys []string) []string {
	var problems []string
	addf := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf("%s: "+format, append([]any{name}, args...)...))
	}
	m := f.Meta

	// The provider field of each role is the provider-picker, and only it.
	role, isPicker := strings.CutSuffix(strings.Join(keys, "."), ".provider")
	isPicker = isPicker && f.Layer == FieldRepo && slices.Contains(ProviderRoles, role)
	switch {
	case isPicker && m.Widget != WidgetProviderPicker:
		addf("the provider of role %q needs a provider-picker", role)
	case isPicker && m.Picker != nil && m.Picker.Role != role:
		addf("provider-picker names role %q, not %q", m.Picker.Role, role)
	case isPicker && f.Schema.Enum == nil:
		addf("the provider of role %q has no choices", role)
	case !isPicker && m.Widget == WidgetProviderPicker:
		addf("only the provider of a role is a provider-picker")
	}
	if isPicker {
		for _, r := range ProviderCatalog() {
			if r.Role == role && !slices.Equal(f.Schema.Enum, r.Providers) {
				addf("choices %v are not the providers of role %q (%v)", f.Schema.Enum, role, r.Providers)
			}
		}
	}

	// Groups: a role's provider is in "providers", its settings in "providers.<role>".
	if want, ok := expectedGroup(f, keys); ok && m.Group != "" && m.Group != want {
		addf("group is %q, want %q", m.Group, want)
	}

	// Applies: restart is for provider selections and new-worktrees for worktree.*, and no other field.
	if wantRestart := isPicker; (m.Applies == AppliesRestart) != wantRestart {
		addf("applies %q: only a provider selection applies on restart", m.Applies)
	}
	if wantNew := keys[0] == "worktree"; (m.Applies == AppliesNewWorktrees) != wantNew {
		addf("applies %q: only worktree.* applies to new worktrees, and all of it does", m.Applies)
	}

	// The approval windows carry the limits validateAutonomy checks.
	if slices.Contains(approvalWindows, f.ID) {
		lo, hi := MinApprovalWindow.String(), MaxApprovalWindow.String()
		if got, err := parseLimit(m.MinDuration); err != nil || m.MinDuration == "" || got != MinApprovalWindow {
			addf("approval window needs min_duration %s (the shortest the code accepts), not %q", lo, m.MinDuration)
		}
		if got, err := parseLimit(m.MaxDuration); err != nil || m.MaxDuration == "" || got != MaxApprovalWindow {
			addf("approval window needs max_duration %s (the longest the code accepts), not %q", hi, m.MaxDuration)
		}
	}

	// Loosening: exactly the ADR's list.
	if want := loosening[f.ID]; f.Provider == "" && m.Loosens != want {
		if want != "" {
			addf("a change can loosen policy: loosens must be %q, not %q", want, m.Loosens)
		} else {
			addf("loosens is %q but the field is not in the ADR's list of loosening fields", m.Loosens)
		}
	} else if f.Provider != "" && m.Loosens != "" {
		addf("a provider setting does not loosen policy")
	}
	if !loosensSuits(m.Loosens, m.Widget, f.Schema) {
		addf("loosens %q does not suit widget %q", m.Loosens, m.Widget)
	}

	// Columns: one for every property of the item or value, and no other.
	want := columnSchemas(f.Schema)
	if m.Widget != WidgetList && m.Widget != WidgetKVTable && len(f.Columns) > 0 {
		addf("only a list or kv-table has columns")
	}
	if m.Widget == WidgetList && len(want) == 0 {
		addf("a list of items without properties has no columns to show")
	}
	groupIDs := map[string]bool{m.Group: true}
	for _, col := range slices.Sorted(maps.Keys(want)) {
		meta, ok := f.Columns[col]
		if !ok {
			addf("no column for property %q", col)
			continue
		}
		problems = append(problems, controlProblems(name+" column "+col, meta, want[col], groupIDs)...)
	}
	for _, col := range slices.Sorted(maps.Keys(f.Columns)) {
		if _, ok := want[col]; !ok {
			addf("stale column %q: the schema has no such property", col)
		}
	}
	return problems
}

// expectedGroup says which group a field belongs to: the provider selection
// of a role is in "providers", and the role's settings (and templates) are
// in "providers.<role>".
func expectedGroup(f CatalogField, keys []string) (string, bool) {
	if f.Layer != FieldRepo {
		return "", false
	}
	for _, role := range ProviderRoles {
		prefix := strings.Split(role, ".")
		if len(keys) <= len(prefix) || !slices.Equal(keys[:len(prefix)], prefix) {
			continue
		}
		switch rest := keys[len(prefix):]; {
		case len(rest) == 1 && rest[0] == "provider":
			return groupProviders, true
		case rest[0] == "settings" || (role == "task_management" && rest[0] == "templates"):
			return roleGroup(role), true
		}
	}
	return "", false
}

// loosensSuits says whether a kind of loosening fits the control that edits it.
func loosensSuits(l Loosening, w Widget, s Field) bool {
	switch l {
	case LoosensNone:
		return true
	case LoosensLevel:
		return w == WidgetSelect || (w == WidgetKVTable && s.AdditionalProperties != nil && slices.Equal(s.AdditionalProperties.Enum, levelChoices))
	case LoosensLonger:
		return w == WidgetDuration
	case LoosensEnable:
		return w == WidgetToggle
	case LoosensShrink:
		return w == WidgetChips || w == WidgetBranchPicker || w == WidgetKVTable
	}
	return false
}

// widgetSuits says whether a widget can edit a schema node: the ADR's table
// "Widgets allowed", with one extension. The table lists a list of strings
// for chips; a list of integers (rejected_statuses) also uses chips, and the
// control reads the item type from the schema.
func widgetSuits(w Widget, s Field) bool {
	switch s.Type {
	case "boolean":
		return w == WidgetToggle
	case "integer":
		return w == WidgetStepper
	case "string":
		if len(s.Enum) > 0 {
			return w == WidgetSelect || w == WidgetProviderPicker
		}
		return slices.Contains([]Widget{WidgetText, WidgetDuration, WidgetBranchPicker, WidgetFilePicker, WidgetCredentialRef}, w)
	case "array":
		if s.Items == nil {
			return false
		}
		switch s.Items.Type {
		case "string":
			return w == WidgetChips || w == WidgetBranchPicker
		case "integer":
			return w == WidgetChips
		case "object":
			return w == WidgetList
		}
	case "object":
		return s.AdditionalProperties != nil && w == WidgetKVTable
	}
	return false
}

// columnSchemas returns the properties a list item or a kv-table value that
// is an object has, by name. A property that is itself an object with fixed
// properties is split the way the catalog walk splits a field: its
// properties are named by their path ("native.channel_id"). A value that is
// not an object with fixed properties has no columns.
func columnSchemas(s Field) map[string]Field {
	var item *Field
	switch {
	case s.Type == "array" && s.Items != nil:
		item = s.Items
	case s.Type == "object" && s.AdditionalProperties != nil:
		item = s.AdditionalProperties
	}
	if item == nil || item.Type != "object" || item.AdditionalProperties != nil {
		return nil
	}
	out := map[string]Field{}
	walkSchema(nil, *item, func(keys []string, f Field) { out[FormatFieldPath(keys...)] = f })
	return out
}

func describeSchema(s Field) string {
	switch {
	case s.Type == "array" && s.Items != nil:
		return "array of " + s.Items.Type
	case s.Type == "string" && len(s.Enum) > 0:
		return "string with enum"
	case s.Type == "object" && s.AdditionalProperties != nil:
		return "object with additional_properties"
	}
	return s.Type
}

func parseLimit(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

// mutateTable returns a copy of the table with the entry for a layer, path
// and provider changed by fn.
func mutateTable(t *testing.T, table []metaEntry, id, provider string, fn func(*metaEntry)) []metaEntry {
	t.Helper()
	out := slices.Clone(table)
	for i := range out {
		if FieldID(out[i].layer, out[i].path) == id && out[i].provider == provider {
			out[i].columns = maps.Clone(out[i].columns)
			fn(&out[i])
			return out
		}
	}
	t.Fatalf("no table entry for %s (provider %q)", id, provider)
	return nil
}

// TestFieldCoverageRulesFail checks that the coverage test notices each way a
// field can lack a proper control, and names the field.
func TestFieldCoverageRulesFail(t *testing.T) {
	table := metaTable()
	groups := Groups()
	noDuration := func(e *metaEntry) { e.meta.MinDuration, e.meta.MaxDuration = "", "" }

	tests := []struct {
		name     string
		id       string
		provider string
		change   func(*metaEntry)
		want     string // in a message that names the ID
	}{
		{"empty label", "personal:autonomy.local", "", func(e *metaEntry) { e.meta.Label = " " }, "empty label"},
		{"empty description", "personal:autonomy.local", "", func(e *metaEntry) { e.meta.Description = "" }, "empty description"},
		{"empty group", "personal:autonomy.local", "", func(e *metaEntry) { e.meta.Group = "" }, "empty group"},
		{"unknown group", "personal:autonomy.local", "", func(e *metaEntry) { e.meta.Group = "nowhere" }, `group "nowhere" is not in Groups()`},
		{"empty applies", "personal:autonomy.local", "", func(e *metaEntry) { e.meta.Applies = "" }, "empty applies"},
		{"unknown widget", "personal:autonomy.local", "", func(e *metaEntry) { e.meta.Widget = "dial" }, `unknown widget "dial"`},
		{"widget that does not suit", "repo:commit.subject_max", "", func(e *metaEntry) { e.meta.Widget = WidgetToggle }, "does not suit"},
		{"select without choices", "repo:policy.test_cmd", "", func(e *metaEntry) { e.meta.Widget = WidgetSelect }, "does not suit"},
		{"stepper without min", "repo:commit.subject_max", "", func(e *metaEntry) { e.meta.Min = nil }, "stepper has no min"},
		{"stepper below zero", "repo:task_management.settings.search.limit", "github-issues", func(e *metaEntry) { v := -1; e.meta.Min = &v }, "below zero"},
		{"duration without limits", "personal:approval_window", "", noDuration, "approval_window"},
		{"duration limits that are not durations", "repo:update.check_every", "", func(e *metaEntry) { e.meta.MinDuration = "soon" }, "min_duration"},
		{"provider-picker without a role", "repo:task_management.provider", "", func(e *metaEntry) { e.meta.Picker = nil }, "names no role"},
		{"provider-picker for the wrong role", "repo:task_management.provider", "", func(e *metaEntry) { e.meta.Picker = &Picker{Role: "secops"} }, `names role "secops"`},
		{"list branch-picker that is not multiple", "repo:policy.protected_branches", "", func(e *metaEntry) { e.meta.Picker = nil }, "picker.multiple"},
		{"file-picker without a kind", "repo:worktree.root", "", func(e *metaEntry) { e.meta.Picker = &Picker{Base: "home"} }, "file-picker kind"},
		{"file-picker with an unknown base", "repo:worktree.root", "", func(e *metaEntry) { e.meta.Picker = &Picker{Kind: PickDir, Base: "disk"} }, "file-picker base"},
		{"object kv-table without a column", "repo:worktree.go_work.siblings", "", func(e *metaEntry) { delete(e.columns, "path") }, `no column for property "path"`},
		{"object kv-table with a stale column", "repo:worktree.go_work.siblings", "", func(e *metaEntry) {
			e.columns["branch"] = e.columns["path"]
		}, `stale column "branch"`},
		{"nested property without a column", "repo:notifications.settings.destinations", "router", func(e *metaEntry) {
			delete(e.columns, "native.channel_id")
		}, `no column for property "native.channel_id"`},
		{"column that does not suit", "repo:worktree.go_work.siblings", "", func(e *metaEntry) {
			c := e.columns["repo"]
			c.Widget = WidgetToggle
			e.columns["repo"] = c
		}, "does not suit"},
		{"loosening field without loosens", "repo:policy.coordinator_merge", "", func(e *metaEntry) { e.meta.Loosens = LoosensNone }, `loosens must be "enable"`},
		{"loosening field with the wrong kind", "personal:approval_window", "", func(e *metaEntry) { e.meta.Loosens = LoosensLevel }, `loosens must be "longer"`},
		{"loosens on another field", "repo:commit.ai_trailer", "", func(e *metaEntry) { e.meta.Loosens = LoosensEnable }, "not in the ADR's list"},
		{"restart on another field", "repo:policy.test_cmd", "", func(e *metaEntry) { e.meta.Applies = AppliesRestart }, "restart"},
		{"live provider selection", "repo:secops.provider", "", func(e *metaEntry) { e.meta.Applies = AppliesLive }, "restart"},
		{"worktree field that is live", "personal:worktree.root", "", func(e *metaEntry) { e.meta.Applies = AppliesLive }, "new worktrees"},
		{"provider setting in the wrong group", "repo:secops.settings.min_severity", "github", func(e *metaEntry) { e.meta.Group = groupPolicy }, `want "providers.secops"`},
		{"label used twice", "repo:policy.lint_cmd", "", func(e *metaEntry) { e.meta.Label = "Test command" }, `label "Test command" is already used`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := mutateTable(t, table, tt.id, tt.provider, tt.change)
			problems := catalogProblems(buildCatalog(changed), changed, groups)
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, tt.id) && strings.Contains(p, tt.want) }) {
				t.Errorf("no problem naming %s and containing %q; got:\n%s", tt.id, tt.want, strings.Join(problems, "\n"))
			}
		})
	}

	t.Run("field without an entry", func(t *testing.T) {
		const id = "repo:commit.types"
		changed := slices.DeleteFunc(slices.Clone(table), func(e metaEntry) bool { return FieldID(e.layer, e.path) == id })
		problems := catalogProblems(buildCatalog(changed), changed, groups)
		if want := id + ": no table entry"; !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, want) }) {
			t.Errorf("want a problem starting %q; got:\n%s", want, strings.Join(problems, "\n"))
		}
	})
	t.Run("provider field without an entry", func(t *testing.T) {
		changed := slices.DeleteFunc(slices.Clone(table), func(e metaEntry) bool {
			return FieldID(e.layer, e.path) == "repo:task_management.settings.project" && e.provider == "asana"
		})
		problems := catalogProblems(buildCatalog(changed), changed, groups)
		if want := "repo:task_management.settings.project (provider asana): no table entry"; !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, want) }) {
			t.Errorf("want a problem starting %q; got:\n%s", want, strings.Join(problems, "\n"))
		}
	})
	t.Run("entry for a field that is gone", func(t *testing.T) {
		stale := metaEntry{layer: FieldRepo, path: "policy.removed", keys: []string{"policy", "removed"}, meta: FieldMeta{Label: "Removed"}}
		changed := append(slices.Clone(table), stale)
		problems := catalogProblems(buildCatalog(changed), changed, groups)
		if want := "repo:policy.removed: stale table entry"; !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, want) }) {
			t.Errorf("want a problem starting %q; got:\n%s", want, strings.Join(problems, "\n"))
		}
	})
	t.Run("entry for a provider that has no such field", func(t *testing.T) {
		stale := metaEntry{layer: FieldRepo, path: "secops.settings.repository", provider: "gitlab", keys: []string{"secops", "settings", "repository"}, meta: FieldMeta{Label: "Repository"}}
		changed := append(slices.Clone(table), stale)
		problems := catalogProblems(buildCatalog(changed), changed, groups)
		if want := "repo:secops.settings.repository (provider gitlab): stale table entry"; !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, want) }) {
			t.Errorf("want a problem starting %q; got:\n%s", want, strings.Join(problems, "\n"))
		}
	})
	t.Run("entry that another entry hides", func(t *testing.T) {
		// An entry with no provider is stale once an entry per provider covers every field it matches.
		extra := metaEntry{layer: FieldRepo, path: "secops.settings.repository", keys: []string{"secops", "settings", "repository"}, meta: FieldMeta{Label: "Repository"}}
		changed := append(slices.Clone(table), extra)
		problems := catalogProblems(buildCatalog(changed), changed, groups)
		if want := "repo:secops.settings.repository: stale table entry"; !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, want) }) {
			t.Errorf("want a problem starting %q; got:\n%s", want, strings.Join(problems, "\n"))
		}
	})
	t.Run("entry twice", func(t *testing.T) {
		changed := append(slices.Clone(table), table[0])
		problems := catalogProblems(buildCatalog(changed), changed, groups)
		if want := table[0].describe() + ": more than one table entry"; !slices.Contains(problems, want) {
			t.Errorf("want %q; got:\n%s", want, strings.Join(problems, "\n"))
		}
	})
	t.Run("group missing from Groups", func(t *testing.T) {
		some := slices.DeleteFunc(Groups(), func(g Group) bool { return g.ID == groupPolicy })
		problems := catalogProblems(buildCatalog(table), table, some)
		if want := `repo:policy.test_cmd: group "policy" is not in Groups()`; !slices.Contains(problems, want) {
			t.Errorf("want %q; got:\n%s", want, strings.Join(problems, "\n"))
		}
	})
}

// TestFieldCoverageListColumns checks the list rules on a synthetic field:
// the catalog has no list of objects today, so the real data does not cover
// them.
func TestFieldCoverageListColumns(t *testing.T) {
	item := Field{Type: "object", Properties: map[string]Field{"name": {Type: "string"}, "enabled": {Type: "boolean"}}}
	field := CatalogField{
		ID: "repo:things", Layer: FieldRepo, Path: "things",
		Schema: Field{Type: "array", Items: &item},
		Meta:   FieldMeta{Label: "Things", Description: "Things.", Group: groupPolicy, Widget: WidgetList, Applies: AppliesLive},
		Columns: map[string]FieldMeta{
			"name":  {Label: "Name", Description: "Its name.", Group: groupPolicy, Widget: WidgetText, Applies: AppliesLive},
			"extra": {Label: "Extra", Description: "Not a property.", Group: groupPolicy, Widget: WidgetText, Applies: AppliesLive},
		},
	}
	problems := fieldProblems(fieldName(field), field, []string{"things"})
	for _, want := range []string{`repo:things: no column for property "enabled"`, `repo:things: stale column "extra"`} {
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, want) }) {
			t.Errorf("want a problem starting %q; got:\n%s", want, strings.Join(problems, "\n"))
		}
	}

	field.Columns["enabled"] = FieldMeta{Label: "Enabled", Description: "On or off.", Group: groupPolicy, Widget: WidgetToggle, Applies: AppliesLive}
	delete(field.Columns, "extra")
	if problems := fieldProblems(fieldName(field), field, []string{"things"}); len(problems) != 0 {
		t.Errorf("a complete list has problems:\n%s", strings.Join(problems, "\n"))
	}
}

// TestWidgetsSuitSchemaNodes pins the ADR's table "Widgets allowed".
func TestWidgetsSuitSchemaNodes(t *testing.T) {
	text := Field{Type: "string"}
	number := Field{Type: "integer"}
	choice := Field{Type: "string", Enum: []string{"a", "b"}}
	strings1 := Field{Type: "array", Items: &text}
	numbers := Field{Type: "array", Items: &number}
	objects := Field{Type: "array", Items: &Field{Type: "object", Properties: map[string]Field{"a": text}}}
	table := Field{Type: "object", AdditionalProperties: &text}
	fixed := Field{Type: "object", Properties: map[string]Field{"a": text}}

	tests := []struct {
		name   string
		schema Field
		want   []Widget
	}{
		{"boolean", Field{Type: "boolean"}, []Widget{WidgetToggle}},
		{"string with enum", choice, []Widget{WidgetSelect, WidgetProviderPicker}},
		{"string", text, []Widget{WidgetText, WidgetDuration, WidgetBranchPicker, WidgetFilePicker, WidgetCredentialRef}},
		{"integer", number, []Widget{WidgetStepper}},
		{"array of string", strings1, []Widget{WidgetChips, WidgetBranchPicker}},
		{"array of integer", numbers, []Widget{WidgetChips}},
		{"array of object", objects, []Widget{WidgetList}},
		{"object with additional_properties", table, []Widget{WidgetKVTable}},
		{"object with fixed properties", fixed, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, w := range Widgets {
				if got, want := widgetSuits(w, tt.schema), slices.Contains(tt.want, w); got != want {
					t.Errorf("widgetSuits(%s) = %v, want %v", w, got, want)
				}
			}
		})
	}
}
