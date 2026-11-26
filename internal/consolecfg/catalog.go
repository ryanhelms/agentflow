package consolecfg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"sync"

	"github.com/ryanhelms/agentflow/internal/config"
)

// Catalog is the settings field catalog of one worktree (ADR, "Phase 4",
// "Repository catalog"). It implements settings.Catalog. The fields are the
// compiled catalog plus the settings of providers defined in provider
// definition files. Definitions can configure compiled providers only today
// (config rejects a provider without a compiled type), so they add no field
// yet; the catalog is still built per worktree so that they can.
type Catalog struct {
	fields   []config.CatalogField
	keys     [][]string // parsed paths, parallel to fields
	revision string
}

// RepoCatalog returns the field catalog of the worktree at root.
func RepoCatalog(root string) *Catalog {
	_ = root // provider definitions add no fields yet: see Catalog
	return newCatalog(compiledFields())
}

// NewCatalog returns a catalog of fields, for tests and other sources.
func NewCatalog(fields []config.CatalogField) *Catalog { return newCatalog(fields) }

func newCatalog(fields []config.CatalogField) *Catalog {
	c := &Catalog{fields: append([]config.CatalogField(nil), fields...)}
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
	b, _ := json.Marshal(c.fields)
	sum := sha256.Sum256(b)
	c.revision = "sha256:" + hex.EncodeToString(sum[:])
	return c
}

// Revision changes whenever Fields would return something else.
func (c *Catalog) Revision() string { return c.revision }

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
