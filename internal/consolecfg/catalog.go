package consolecfg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"sync"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// Catalog is the settings field catalog of one worktree (ADR, "Phase 4",
// "Repository catalog"). It implements settings.Catalog. The fields are the
// compiled catalog, the settings of providers defined in provider
// definition files, and the fields of every installed plugin (ADR, "Phase 5
// settings"). Definitions can configure compiled providers only today
// (config rejects a provider without a compiled type), so they add no field
// yet; the catalog is still built per worktree so that they can.
type Catalog struct {
	fields   []config.CatalogField
	keys     [][]string // parsed paths, parallel to fields
	groups   []config.Group
	revision string
}

// RepoCatalog returns the field catalog of the worktree at root. Installed
// plugins (the registry of the agentflow home) add an enabled toggle and
// their settings in a group each.
func RepoCatalog(root string) *Catalog {
	_ = root // provider definitions add no fields yet: see Catalog
	installed := installedPlugins()
	return newCatalog(append(slices.Clone(compiledFields()), manifest.CatalogFields(installed)...),
		append(config.Groups(), manifest.Groups(installed)...))
}

// installedPlugins lists the plugins of the agentflow home. A registry that
// cannot be read adds no fields: `agentflow plugin list` reports why.
func installedPlugins() []manifest.Installed {
	home, err := config.HomeDir()
	if err != nil {
		return nil
	}
	list, err := manifest.NewFileSource(home).Installed(context.Background())
	if err != nil {
		return nil
	}
	return list
}

// NewCatalog returns a catalog of fields, for tests and other sources. Its
// groups are the compiled ones.
func NewCatalog(fields []config.CatalogField) *Catalog { return newCatalog(fields, config.Groups()) }

func newCatalog(fields []config.CatalogField, groups []config.Group) *Catalog {
	c := &Catalog{fields: append([]config.CatalogField(nil), fields...), groups: slices.Clone(groups)}
	sort.SliceStable(c.fields, func(i, j int) bool {
		if c.fields[i].ID != c.fields[j].ID {
			return c.fields[i].ID < c.fields[j].ID
		}
		return c.fields[i].Provider < c.fields[j].Provider
	})
	c.keys = make([][]string, len(c.fields))
	for i, f := range c.fields {
		c.keys[i], _ = config.ParseFieldPath(f.Path)
	}
	// The groups are part of what the page draws, so they change the revision.
	b, _ := json.Marshal(struct {
		Fields []config.CatalogField `json:"fields"`
		Groups []config.Group        `json:"groups"`
	}{c.fields, c.groups})
	sum := sha256.Sum256(b)
	c.revision = "sha256:" + hex.EncodeToString(sum[:])
	return c
}

// Revision changes whenever Fields would return something else.
func (c *Catalog) Revision() string { return c.revision }

// Groups lists the sections of the Settings page in display order: the
// compiled groups, then one group for each installed plugin.
func (c *Catalog) Groups() []config.Group { return slices.Clone(c.groups) }

// Fields returns every field, sorted by ID, then provider.
func (c *Catalog) Fields() []config.CatalogField {
	return append([]config.CatalogField(nil), c.fields...)
}

// Lookup finds the field for a concrete ID. provider is the role's selected
// provider: a provider settings field matches only under it, unless provider
// is empty, which matches the first provider that has the path. A path
// inside a kv-table, chips or list field returns that field and the
// remaining keys.
func (c *Catalog) Lookup(id, provider string) (config.CatalogField, []string, bool) {
	layer, path, err := config.ParseFieldID(id)
	if err != nil {
		return config.CatalogField{}, nil, false
	}
	keys, err := config.ParseFieldPath(path)
	if err != nil {
		return config.CatalogField{}, nil, false
	}
	best := -1
	for i, f := range c.fields {
		fk := c.keys[i]
		if f.Layer != layer || len(fk) > len(keys) || !config.MatchFieldPath(fk, keys[:len(fk)]) {
			continue
		}
		if f.Provider != "" && provider != "" && f.Provider != provider {
			continue
		}
		if len(fk) < len(keys) && !container(f.Schema) {
			continue
		}
		if best < 0 || len(fk) > len(c.keys[best]) {
			best = i
		}
	}
	if best < 0 {
		return config.CatalogField{}, nil, false
	}
	return c.fields[best], append([]string(nil), keys[len(c.keys[best]):]...), true
}

// Providers lists the providers that have a field at id's path, sorted.
func (c *Catalog) Providers(id string) []string {
	var out []string
	for _, f := range c.fields {
		if f.ID == id && f.Provider != "" && !slices.Contains(out, f.Provider) {
			out = append(out, f.Provider)
		}
	}
	return out
}

func container(f config.Field) bool {
	return f.Type == "array" || f.Type == "object" && (f.AdditionalProperties != nil || len(f.Properties) > 0)
}

// compiledFields is the compiled field catalog of lane 4.1.
var compiledFields = sync.OnceValue(config.Catalog)

// Groups lists the field groups, in order.
func Groups() []config.Group { return config.Groups() }
