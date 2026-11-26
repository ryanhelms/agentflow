package consolecfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/flowmap"
	"github.com/ryanhelms/agentflow/internal/release"
	"github.com/ryanhelms/agentflow/internal/setup"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
	"gopkg.in/yaml.v3"
)

type Snapshot struct {
	repo      *config.Repo
	Schemas   map[string]map[string]config.Field `json:"schemas"`
	Files     []File                             `json:"files"`
	Providers *setup.ProviderList                `json:"providers"`
	Core      map[string]any                     `json:"core,omitempty"`
	Problems  []string                           `json:"problems"`
}

// Repository returns the resolved repository, or nil if its sources could not be read safely.
func (s Snapshot) Repository() *config.Repo { return s.repo }

type Change struct {
	Role   string `json:"role"`
	Path   string `json:"path"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

type Plan struct {
	Sources    []File              `json:"-"`
	Path       string              `json:"path"`
	BaseDigest string              `json:"base_digest"`
	Changes    []Change            `json:"changes"`
	Diff       string              `json:"diff"`
	Providers  *setup.ProviderList `json:"providers"`
}

func View(ctx context.Context, root string) (Snapshot, error) {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return Snapshot{}, err
	}
	defer dir.Close()
	inv, err := readInventory(dir)
	if err != nil {
		return Snapshot{}, err
	}
	return inv.snapshot(ctx, dir, root)
}

func (inv *inventory) snapshot(ctx context.Context, dir *os.Root, root string) (Snapshot, error) {
	out := Snapshot{Schemas: config.ProviderFieldSchemas(), Files: inv.files, Problems: append([]string{}, inv.problems...)}
	cfg, found, err := inv.resolve()
	if err != nil {
		out.Problems = append(out.Problems, safeError(err))
		return out, nil
	}
	providers, err := setup.ListConfiguredProviders(ctx, root, cfg, found, true)
	if err != nil {
		return out, err
	}
	raw, err := json.Marshal(providers)
	if err != nil {
		return out, err
	}
	if sensitive(string(raw)) {
		out.Problems = append(out.Problems, "Provider details withheld because they may contain credentials")
		return out, nil
	}
	out.repo = cfg
	out.Providers = providers
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return out, err
	}
	if err := yaml.Unmarshal(data, &out.Core); err != nil {
		return out, err
	}
	if tpl := cfg.Providers.TaskTemplates; tpl != nil {
		if err := inv.addReference(dir, tpl.Catalog, catalog); err != nil {
			return out, err
		}
	}
	if ref, remote := flowmap.ParseRef(cfg.FlowMap); !remote {
		if err := inv.addReference(dir, ref.Path, flow); err != nil {
			return out, err
		}
	}
	out.Files = inv.files
	return out, nil
}

func safeError(err error) string {
	if sensitive(err.Error()) {
		return "Configuration error contains a credential-like value; inspect the owning file locally"
	}
	return err.Error()
}

func (inv *inventory) resolve() (*config.Repo, bool, error) {
	if len(inv.problems) > 0 {
		return nil, false, errors.New("provider sources could not be inspected")
	}
	var main []byte
	defs := map[string][]byte{}
	for _, f := range inv.files {
		if f.Kind != core && f.Kind != def {
			continue
		}
		if !f.Editable {
			return nil, false, fmt.Errorf("%s: %s", f.Path, f.Reason)
		}
		if f.Kind == core {
			main = []byte(f.Content)
		} else {
			defs[f.Path] = []byte(f.Content)
		}
	}
	return config.InspectRepo(main, defs)
}

func Preview(ctx context.Context, root, name, content, baseDigest string) (Plan, error) {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return Plan{}, err
	}
	defer dir.Close()
	inv, err := readInventory(dir)
	if err != nil {
		return Plan{}, err
	}
	before, err := inv.snapshot(ctx, dir, root)
	if err != nil {
		return Plan{}, err
	}
	f, ok := inv.source(name)
	if !ok || !f.Editable {
		return Plan{}, errors.New("file is not editable in this repository")
	}
	if baseDigest != f.Digest {
		return Plan{}, errors.New("source changed; reload the file before previewing")
	}
	if len(content) > maxSourceBytes {
		return Plan{}, errors.New("candidate exceeds the source editor limit")
	}
	if sensitive(content) {
		return Plan{}, errors.New("candidate contains a credential-like value; use a credential reference")
	}
	if err := validateFile(f.Kind, content); err != nil {
		return Plan{}, errors.New(safeError(err))
	}
	inv.files[inv.byName[name]].Content = content
	after, err := inv.snapshot(ctx, dir, root)
	if err != nil {
		return Plan{}, err
	}
	if len(after.Problems) > 0 {
		return Plan{}, errors.New(strings.Join(after.Problems, "; "))
	}
	if err := inv.validateRefs(before.repo, after.repo, name); err != nil {
		return Plan{}, errors.New(safeError(err))
	}
	return Plan{Sources: after.Files, Path: name, BaseDigest: baseDigest, Changes: changes(before, after), Diff: diff(name, f.Content, content), Providers: after.Providers}, nil
}

// Validate changed references against the candidate. Existing unrelated,
// external resources can remain usable through their normal runtime reader.
func (inv *inventory) validateRefs(before, cfg *config.Repo, edited string) error {
	if tpl := cfg.Providers.TaskTemplates; tpl != nil && (before == nil || edited == tpl.Catalog || !reflect.DeepEqual(tpl, before.Providers.TaskTemplates)) {
		f, ok := inv.source(tpl.Catalog)
		if !ok || f.Kind != catalog || !f.Editable {
			return fmt.Errorf("task template catalog %q is unavailable or invalid", tpl.Catalog)
		}
		c, err := tasktemplate.DecodeCatalog(strings.NewReader(f.Content))
		if err != nil {
			return err
		}
		if tpl.Default != nil {
			if _, err := c.Select(*tpl.Default); err != nil {
				return err
			}
		}
	}
	if ref, remote := flowmap.ParseRef(cfg.FlowMap); !remote && (before == nil || before.FlowMap != cfg.FlowMap || edited == ref.Path) {
		f, ok := inv.source(ref.Path)
		if !ok || f.Kind != flow || !f.Editable {
			return fmt.Errorf("local flow map %q is unavailable in this workspace", ref.Path)
		}
		if _, err := flowmap.Parse([]byte(f.Content)); err != nil {
			return err
		}
	}
	return nil
}

func validateFile(kind, content string) error {
	if kind == catalog {
		_, err := tasktemplate.DecodeCatalog(strings.NewReader(content))
		return err
	}
	var doc map[string]any
	d := yaml.NewDecoder(strings.NewReader(content))
	if err := d.Decode(&doc); err != nil {
		return fmt.Errorf("expected a YAML mapping: %w", err)
	}
	if doc == nil {
		return errors.New("expected a YAML mapping")
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one YAML document")
	}
	switch kind {
	case units:
		var cfg release.Config
		if err := yaml.Unmarshal([]byte(content), &cfg); err != nil {
			return err
		}
		return cfg.Validate()
	case flow:
		_, err := flowmap.Parse([]byte(content))
		return err
	case docs:
		var cfg documentMappings
		if err := yaml.Unmarshal([]byte(content), &cfg); err != nil {
			return err
		}
		if cfg.Space == "" || cfg.Pages == nil {
			return errors.New("document mappings require space and pages")
		}
		for _, page := range cfg.Pages {
			if page.File == "" {
				return errors.New("each document mapping requires a file")
			}
		}
	case envs:
		var cfg environmentMappings
		if err := yaml.Unmarshal([]byte(content), &cfg); err != nil {
			return err
		}
		if cfg.Deployments.Mapping == nil {
			return errors.New("deployment mappings require deployments.environmentMapping")
		}
	}
	return nil
}

type property struct{ role, path string }

func effective(s Snapshot) map[property]any {
	out := map[property]any{}
	var walk func(string, map[string]any)
	walk = func(prefix string, m map[string]any) {
		for name, v := range m {
			key := prefix + name
			if children, ok := v.(map[string]any); ok && len(children) > 0 {
				walk(key+".", children)
			} else {
				out[property{"repository", key}] = v
			}
		}
	}
	walk("", s.Core)
	if s.Providers != nil {
		for _, role := range s.Providers.Roles {
			out[property{role.Role, "provider"}] = role.Selected
			for _, s := range role.Settings {
				if children, ok := s.Value.(map[string]any); ok && len(children) > 0 && s.Constraint == nil {
					continue // each map entry already has its own effective change
				}
				out[property{role.Role, "settings." + s.Path}] = s.Value
			}
		}
	}
	return out
}

func changes(a, b Snapshot) []Change {
	old, next := effective(a), effective(b)
	all := maps.Clone(old)
	maps.Copy(all, next)
	keys := slices.SortedFunc(maps.Keys(all), func(a, b property) int {
		if a.role == b.role {
			return strings.Compare(a.path, b.path)
		}
		return strings.Compare(a.role, b.role)
	})
	changes := []Change{}
	for _, k := range keys {
		a, had := old[k]
		b, has := next[k]
		if had != has || !reflect.DeepEqual(a, b) {
			changes = append(changes, Change{k.role, k.path, a, b})
		}
	}
	return changes
}

// Return one exact replacement hunk with stable line numbers. The source panes
// retain all context, including comments and whitespace outside this hunk.
func diff(name, old, next string) string {
	if old == next {
		return ""
	}
	a, b := strings.SplitAfter(old, "\n"), strings.SplitAfter(next, "\n")
	if a[len(a)-1] == "" {
		a = a[:len(a)-1]
	}
	if b[len(b)-1] == "" {
		b = b[:len(b)-1]
	}
	start := 0
	for start < len(a) && start < len(b) && a[start] == b[start] {
		start++
	}
	endA, endB := len(a), len(b)
	for endA > start && endB > start && a[endA-1] == b[endB-1] {
		endA--
		endB--
	}
	var out strings.Builder
	first := max(0, start-3)
	lastA := min(len(a), endA+3)
	lastB := min(len(b), endB+3)
	lineA, lineB := first+1, first+1
	if first == lastA {
		lineA--
	}
	if first == lastB {
		lineB--
	}
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n", name, name, lineA, lastA-first, lineB, lastB-first)
	lines := func(items []string, sign string) {
		for _, line := range items {
			out.WriteString(sign)
			out.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				out.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	lines(a[first:start], " ")
	lines(a[start:endA], "-")
	lines(b[start:endB], "+")
	lines(a[endA:lastA], " ")
	return out.String()
}
