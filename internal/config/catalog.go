package config

import (
	"cmp"
	"maps"
	"slices"
	"strings"
)

// The settings field catalog: the phase 4 contract (ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md, section
// "Phase 4 Field metadata", lane 4.1).
//
// The catalog is built from compiled types only: it walks the schema of
// config.User and config.Repo, then each provider role's compiled settings.
// Each field takes its FieldMeta from the table in field_meta_table.go.
// config.Field and the agentflow_config output do not change: the walk reads
// SchemaFor and ProviderFieldSchemas and adds nothing to them.

// formatMarker is a field the walk leaves out.
type formatMarker struct {
	Layer  FieldLayer
	Path   string
	Reason string
}

// catalogFormatMarkers are the fields the walk leaves out: they say which
// schema version a file or section uses. agentflow init and migrations write
// them, so a control would only invite a broken file. Each role's "version"
// never reaches the walk (roles come from ProviderCatalog, which has
// settings only); it is listed so the exclusion is stated in one place and
// tested.
var catalogFormatMarkers = func() []formatMarker {
	const reason = "schema version of the file or section, written by agentflow init and migrations"
	markers := []formatMarker{{FieldRepo, "agentflow", reason}, {FieldRepo, "devops.version", reason}}
	for _, role := range ProviderRoles {
		markers = append(markers, formatMarker{FieldRepo, role + ".version", reason})
	}
	return markers
}()

func isFormatMarker(layer FieldLayer, path string) bool {
	return slices.ContainsFunc(catalogFormatMarkers, func(m formatMarker) bool {
		return m.Layer == layer && m.Path == path
	})
}

// Groups lists the sections of the Settings page in display order: the
// provider selections, one group per provider role for that role's settings,
// then the other groups.
func Groups() []Group {
	out := []Group{{ID: groupProviders, Label: "Providers", Order: 100}}
	for i, role := range ProviderRoles {
		out = append(out, Group{ID: roleGroup(role), Label: roleLabels[role], Order: 110 + 10*i})
	}
	return append(out,
		Group{ID: groupAutonomy, Label: "Autonomy", Order: 200},
		Group{ID: groupPolicy, Label: "Repository policy", Order: 300},
		Group{ID: groupCommit, Label: "Commits", Order: 400},
		Group{ID: groupWorktrees, Label: "Worktrees", Order: 500},
		Group{ID: groupIntegrations, Label: "Integrations", Order: 600},
		Group{ID: groupUpdates, Label: "Updates", Order: 700},
		Group{ID: groupInit, Label: "Automatic setup", Order: 800},
	)
}

// roleLabels names each provider role in the page. A role missing here shows
// as an empty group label, which TestEveryConfigFieldHasControl reports.
var roleLabels = map[string]string{
	"code_hosting":       "Code hosting",
	"task_management":    "Task management",
	"knowledge_base":     "Knowledge base",
	"devops.checks":      "DevOps checks",
	"devops.deployments": "DevOps deployments",
	"devops.releases":    "DevOps releases",
	"secops":             "SecOps",
	"notifications":      "Notifications",
}

// Catalog lists every settings field of both configuration layers, sorted by
// group, then field order, then ID and provider. Each call returns a new
// slice the caller may change.
//
// A field with no entry in the metadata table is still listed, with an empty
// FieldMeta: the catalog does not hide what it cannot describe.
// TestEveryConfigFieldHasControl fails for it.
func Catalog() []CatalogField { return buildCatalog(metaTable()) }

// buildCatalog is Catalog with the metadata table given, so tests can check
// the rules against a changed table.
func buildCatalog(table []metaEntry) []CatalogField {
	fields := compiledFields()
	for i := range fields {
		f := &fields[i]
		keys, _ := ParseFieldPath(f.Path)
		if e := bestEntry(table, f.Layer, keys, f.Provider); e != nil {
			e.apply(f)
		}
	}
	order := map[string]int{}
	for _, g := range Groups() {
		order[g.ID] = g.Order
	}
	slices.SortStableFunc(fields, func(a, b CatalogField) int {
		return cmp.Or(
			cmp.Compare(order[a.Meta.Group], order[b.Meta.Group]),
			cmp.Compare(a.Meta.Order, b.Meta.Order),
			cmp.Compare(a.ID, b.ID),
			cmp.Compare(a.Provider, b.Provider),
		)
	})
	return fields
}

// compiledFields walks the compiled types and returns every field with its
// schema node and no metadata.
//
//  1. config.User is the personal layer and config.Repo the repository layer.
//     An object with fixed properties is not a field: its properties are. A
//     scalar, an array or a map is one field, and the walk stops there.
//  2. Each provider role has a provider field (repo:<role>.provider) and the
//     settings of every compiled provider (repo:<role>.settings.<path>), each
//     with its provider set. The task_management role also has the template
//     settings, which do not depend on the provider.
func compiledFields() []CatalogField {
	var out []CatalogField
	emit := func(layer FieldLayer, keys []string, provider string, schema Field) {
		path := FormatFieldPath(keys...)
		if isFormatMarker(layer, path) {
			return
		}
		out = append(out, CatalogField{ID: FieldID(layer, path), Layer: layer, Path: path, Provider: provider, Schema: schema})
	}
	for _, layer := range []struct {
		layer  FieldLayer
		schema Field
	}{
		{FieldPersonal, SchemaFor(User{}, "yaml")},
		{FieldRepo, SchemaFor(Repo{}, "yaml")},
	} {
		walkSchema(nil, layer.schema, func(keys []string, f Field) { emit(layer.layer, keys, "", f) })
	}
	schemas := ProviderFieldSchemas()
	for _, role := range ProviderCatalog() {
		keys := strings.Split(role.Role, ".")
		emit(FieldRepo, append(slices.Clone(keys), "provider"), "", Field{
			Type: "string", Enum: slices.Clone(role.Providers), Default: role.Default,
		})
		for _, id := range role.Providers {
			if id == "none" {
				continue // no settings
			}
			walkSchema(append(slices.Clone(keys), "settings"), schemas[role.Role][id], func(keys []string, f Field) {
				emit(FieldRepo, keys, id, f)
			})
		}
		if role.Role == "task_management" {
			walkSchema(append(slices.Clone(keys), "templates"), SchemaFor(TaskTemplateSettings{}, "yaml"), func(keys []string, f Field) {
				emit(FieldRepo, keys, "", f)
			})
		}
	}
	return out
}

// walkSchema calls fn for each field below f. An object with fixed properties
// is not a field: its properties are. An object with no properties has none.
func walkSchema(prefix []string, f Field, fn func(keys []string, f Field)) {
	if f.Type == "object" && f.AdditionalProperties == nil {
		for _, name := range slices.Sorted(maps.Keys(f.Properties)) {
			walkSchema(append(slices.Clone(prefix), name), f.Properties[name], fn)
		}
		return
	}
	if len(prefix) > 0 {
		fn(prefix, f)
	}
}

// bestEntry returns the table entry for a field: the most specific one whose
// layer, path pattern and provider match. An entry with no provider matches
// every provider; one with a provider wins over it.
func bestEntry(table []metaEntry, layer FieldLayer, keys []string, provider string) *metaEntry {
	var best *metaEntry
	bestRank := -1
	for i := range table {
		e := &table[i]
		if e.layer != layer || (e.provider != "" && e.provider != provider) || !MatchFieldPath(e.keys, keys) {
			continue
		}
		rank := 0
		for _, k := range e.keys {
			if k != FieldWildcard {
				rank++
			}
		}
		if e.provider != "" {
			rank += 1000
		}
		if rank > bestRank {
			best, bestRank = e, rank
		}
	}
	return best
}
