package config

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nscaledev/devflow/internal/contract"
)

// Provider definitions are layered YAML files. Later layers win, except
// where the release layer constrains a settings property:
//
//	release  embedded in the binary       may set constraints
//	project  .devflow/providers/*.yml     settings only
//	user     $DEVFLOW_HOME/providers/*.yml settings only
//
// .devflow/config.yml applies its role settings after the project layer and
// before the user layer.
const (
	LayerDefault = "default"
	LayerRelease = "release"
	LayerProject = "project"
	LayerUser    = "user"
)

//go:embed release/*.yml
var releaseFiles embed.FS

// releaseDefinitions is replaced in tests.
var releaseDefinitions = func() fs.FS {
	sub, err := fs.Sub(releaseFiles, "release")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory
	}
	return sub
}()

// Source identifies where a definition or setting came from.
type Source struct {
	Layer string `json:"layer"`
	File  string `json:"file,omitempty"`
}

func (s Source) String() string {
	if s.File == "" {
		return s.Layer
	}
	return s.Layer + ":" + s.File
}

// Constraint limits what later layers may set for one settings property.
// Only the release layer may declare constraints.
type Constraint struct {
	// Lock is true (no change) or "append" (lists may only gain entries).
	Lock    any    `yaml:"lock" json:"lock,omitempty"`
	Allow   []any  `yaml:"allow" json:"allow,omitempty"`
	Pattern string `yaml:"pattern" json:"pattern,omitempty"`
	pattern *regexp.Regexp
	parts   []string
	source  Source
}

type roleDefinition struct {
	Contract     string    `yaml:"contract"`
	SettingsNode yaml.Node `yaml:"settings"`
	settings     map[string]any
	Constraints  map[string]Constraint `yaml:"constraints"`
	source       Source
}

type definitionFile struct {
	Version   int                                  `yaml:"version"`
	Providers map[string]map[string]roleDefinition `yaml:"providers"`
}

// definitions holds every layer's role definitions keyed by provider, then role.
type definitionLoader func(*definitions, map[string]roleSchema) error

type definitions struct {
	trace  bool
	files  []Source
	layers map[string]map[string]map[string]roleDefinition
}

func (d *definitions) get(layer, provider, role string) (roleDefinition, bool) {
	def, ok := d.layers[layer][provider][role]
	return def, ok
}

// loadDefinitions reads the release, project and user layers. Any invalid
// file stops loading, so every command sees the same effective settings.
func loadDefinitions(project definitionLoader, schemas map[string]roleSchema, trace bool) (*definitions, error) {
	d := &definitions{trace: trace, layers: map[string]map[string]map[string]roleDefinition{}}
	if err := d.loadFS(LayerRelease, releaseDefinitions, "release", schemas); err != nil {
		return nil, err
	}
	if err := project(d, schemas); err != nil {
		return nil, err
	}
	home, err := HomeDir()
	if err != nil {
		return nil, err
	}
	if err := d.loadDir(LayerUser, filepath.Join(home, "providers"), "~/.devflow/providers", schemas); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *definitions) loadDir(layer, dir, display string, schemas map[string]roleSchema) error {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return d.loadFS(layer, os.DirFS(dir), display, schemas)
}

func (d *definitions) loadFS(layer string, fsys fs.FS, display string, schemas map[string]roleSchema) error {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("%s: %w", display, err)
	}
	for _, e := range entries {
		ext := path.Ext(e.Name())
		if e.IsDir() || (ext != ".yml" && ext != ".yaml") {
			continue
		}
		data, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return err
		}
		src := Source{Layer: layer, File: display + "/" + e.Name()}
		if err := d.add(src, data, schemas); err != nil {
			return &DefinitionError{Source: src, Err: err}
		}
		d.files = append(d.files, src)
	}
	return nil
}

// DefinitionError reports an invalid provider definition with its file.
type DefinitionError struct {
	Source Source
	Err    error
}

func (e *DefinitionError) Error() string { return e.Source.File + ": " + e.Err.Error() }
func (e *DefinitionError) Unwrap() error { return e.Err }

func (d *definitions) add(src Source, data []byte, schemas map[string]roleSchema) error {
	var file definitionFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	var trailing yaml.Node
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("expected exactly one YAML document")
	}
	if file.Version != ProviderSchemaVersion {
		return fmt.Errorf("version: expected integer %d", ProviderSchemaVersion)
	}
	if d.layers[src.Layer] == nil {
		d.layers[src.Layer] = map[string]map[string]roleDefinition{}
	}
	for _, id := range slices.Sorted(maps.Keys(file.Providers)) {
		for _, role := range slices.Sorted(maps.Keys(file.Providers[id])) {
			def := file.Providers[id][role]
			def.source = src
			p := "providers." + id + "." + role
			schema, ok := schemas[role]
			if !ok {
				return fmt.Errorf("%s: unknown role; expected one of %s", p, strings.Join(ProviderRoles, ", "))
			}
			factory, ok := schema.providers[id]
			if !ok {
				// Configuration-only providers need the operation engine.
				return fmt.Errorf("%s: %s is not available for %s; expected one of %s", p, id, role, strings.Join(schema.choices()[:len(schema.choices())-1], ", "))
			}
			c, err := contract.Resolve(role, def.Contract)
			if err != nil {
				return fmt.Errorf("%s.contract: %w", p, err)
			}
			if def.Contract != "" {
				def.Contract = c.ID()
			}
			if def.SettingsNode.Kind != 0 {
				if err := def.SettingsNode.Decode(&def.settings); err != nil {
					return fmt.Errorf("%s.settings: %w", p, err)
				}
				if def.settings == nil {
					return fmt.Errorf("%s.settings: expected an object with string keys", p)
				}
				if err := applySettings(factory(), def.settings, p+".settings"); err != nil {
					return err
				}
			}
			if len(def.Constraints) > 0 && src.Layer != LayerRelease {
				return fmt.Errorf("%s.constraints: only release definitions may set constraints", p)
			}
			for _, name := range slices.Sorted(maps.Keys(def.Constraints)) {
				c := def.Constraints[name]
				if err := c.compile(reflect.TypeOf(factory()).Elem(), name); err != nil {
					return fmt.Errorf("%s.constraints.%s: %w", p, name, err)
				}
				c.source = src
				def.Constraints[name] = c
			}
			if d.layers[src.Layer][id] == nil {
				d.layers[src.Layer][id] = map[string]roleDefinition{}
			}
			if prev, dup := d.layers[src.Layer][id][role]; dup {
				return fmt.Errorf("%s: already defined in %s", p, prev.source.File)
			}
			d.layers[src.Layer][id][role] = def
		}
	}
	return nil
}

func (c *Constraint) compile(settings reflect.Type, name string) error {
	parts, err := splitSettingPath(name)
	if err != nil {
		return err
	}
	c.parts = parts
	t, err := settingsType(settings, name, parts)
	if err != nil {
		return err
	}
	switch c.Lock {
	case nil, false, true:
	case "append":
		if t.Kind() != reflect.Slice {
			return errors.New("lock: append applies only to lists")
		}
	default:
		return errors.New(`lock: expected true or "append"`)
	}
	if c.Pattern != "" {
		if c.pattern, err = regexp.Compile(c.Pattern); err != nil {
			return fmt.Errorf("pattern: %w", err)
		}
	}
	if c.Lock == nil && c.Allow == nil && c.Pattern == "" {
		return errors.New("expected lock, allow or pattern")
	}
	return nil
}

// Paths use backslash to quote literal dots and backslashes in map keys.
var escapeSettingKey = strings.NewReplacer(`\`, `\\`, ".", `\.`)

func splitSettingPath(name string) ([]string, error) {
	var parts []string
	var part strings.Builder
	for i := 0; i < len(name); i++ {
		switch name[i] {
		case '.':
			parts = append(parts, part.String())
			part.Reset()
		case '\\':
			i++
			if i == len(name) || (name[i] != '.' && name[i] != '\\') {
				return nil, fmt.Errorf("invalid escape in settings property %q", name)
			}
			part.WriteByte(name[i])
		default:
			part.WriteByte(name[i])
		}
	}
	return append(parts, part.String()), nil
}

// settingsType follows a dotted yaml path through a settings struct.
func settingsType(t reflect.Type, name string, parts []string) (reflect.Type, error) {
	for _, part := range parts {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Struct:
			found := false
			for _, f := range reflect.VisibleFields(t) {
				if tag := f.Tag.Get("yaml"); tag != "" && tag != ",inline" && tag == part {
					t, found = f.Type, true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("unknown settings property %q", name)
			}
		case reflect.Map:
			t = t.Elem()
		default:
			return nil, fmt.Errorf("unknown settings property %q", name)
		}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t, nil
}

// lookup finds a dotted path in plain YAML data.
func lookup(data any, parts []string) (any, bool) {
	for _, part := range parts {
		m, ok := data.(map[string]any)
		if !ok {
			return nil, false
		}
		if data, ok = m[part]; !ok {
			return nil, false
		}
	}
	return data, true
}

// plain converts a settings struct to the YAML data model used for comparison.
func plain(settings any) (map[string]any, error) {
	b, err := yaml.Marshal(settings)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	return out, yaml.Unmarshal(b, &out)
}

// check validates a later layer's value against the release baseline.
func (c Constraint) check(value, baseline any) error {
	if c.Lock == true && !reflect.DeepEqual(value, baseline) {
		return errors.New("is locked")
	}
	items := []any{value}
	if list, ok := value.([]any); ok {
		items = list
	}
	if c.Lock == "append" {
		for _, want := range asList(baseline) {
			if !slices.ContainsFunc(items, func(v any) bool { return reflect.DeepEqual(v, want) }) {
				return fmt.Errorf("must keep %v (append only)", want)
			}
		}
	}
	if c.Allow != nil {
		for _, v := range items {
			if !slices.ContainsFunc(c.Allow, func(a any) bool { return reflect.DeepEqual(a, v) }) {
				return fmt.Errorf("value %v is not allowed; expected one of %v", v, c.Allow)
			}
		}
	}
	if c.pattern != nil {
		for _, v := range items {
			s, ok := v.(string)
			if !ok || !c.pattern.MatchString(s) {
				return fmt.Errorf("value %v must match %s", v, c.Pattern)
			}
		}
	}
	return nil
}

func asList(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	if v == nil {
		return nil
	}
	return []any{v}
}

// visitSettings uses the same escaped property names for provenance and explain.
func visitSettings(prefix string, fields map[string]any, visit func(string, any)) {
	for key, value := range fields {
		path := escapeSettingKey.Replace(key)
		if prefix != "" {
			path = prefix + "." + path
		}
		visit(path, value)
		if children, ok := value.(map[string]any); ok {
			visitSettings(path, children, visit)
		}
	}
}

// Contribution is one input to the resolution of a provider property.
type Contribution struct {
	Source Source `json:"source"`
	Value  any    `json:"value"`
	Step   string `json:"step"`
}

// Setting is one effective settings value with the layer that set it.
type Setting struct {
	History    []Contribution `json:"history,omitempty"`
	Path       string         `json:"path"`
	Value      any            `json:"value"`
	Source     Source         `json:"source"`
	Constraint *Constraint    `json:"constraint,omitempty"`
}

// Explain lists the selection's effective settings in path order. Values the
// provider supplies itself report the default layer.
func (s Selection) Explain() ([]Setting, error) {
	if s.Settings == nil {
		return nil, nil
	}
	data, err := plain(s.Settings)
	if err != nil {
		return nil, err
	}
	leaves := map[string]any{}
	visitSettings("", data, func(path string, v any) {
		children, object := v.(map[string]any)
		_, constrained := s.Constraints[path]
		if !object || len(children) == 0 || constrained {
			leaves[path] = v
		}
	})
	out := make([]Setting, 0, len(leaves))
	for _, p := range slices.Sorted(maps.Keys(leaves)) {
		src, ok := s.Sources[p]
		if !ok {
			src = Source{Layer: LayerDefault}
		}
		setting := Setting{Path: p, Value: leaves[p], Source: src, History: s.history[p]}
		if c, ok := s.Constraints[p]; ok {
			setting.Constraint = &c
		}
		out = append(out, setting)
	}
	return out, nil
}
