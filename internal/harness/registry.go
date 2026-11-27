package harness

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ryanhelms/agentflow/internal/harness/acpregistry"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

//go:embed overlay.yml
var overlayYAML []byte

// LoadOverlay decodes the embedded agentflow overlay (overlay.yml) strictly.
func LoadOverlay() (map[string]harnessdef.Definition, error) {
	return decodeOverlay(overlayYAML)
}

func decodeOverlay(b []byte) (map[string]harnessdef.Definition, error) {
	out := map[string]harnessdef.Definition{}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&out); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("harness: overlay.yml: %w", err)
	}
	return out, nil
}

// PluginHarness is one harness definition from an enabled declarative
// plugin's extensions.harness.
type PluginHarness struct {
	Plugin     string // the plugin ID
	ID         string
	Definition harnessdef.Definition
}

// Layers are the inputs of the definition Composite, lowest first.
type Layers struct {
	Catalog  *acpregistry.Catalog
	Overlay  map[string]harnessdef.Definition
	Plugins  []PluginHarness
	Personal map[string]harnessdef.Definition
	// Disabled IDs are left out: never listed or started.
	Disabled []string
	// Platform is the binary distribution key (acpregistry.CurrentPlatform).
	Platform string
}

// Layered is one harness definition after every layer.
type Layered struct {
	ID         string
	Definition harnessdef.Definition
	// Sources are the layers that shaped it, lowest first.
	Sources []string
	// Entry is the registry entry, nil for an ID a plugin or the person
	// added.
	Entry *acpregistry.Entry
	// PluginLaunch is the source ("plugin:<id>") of a plugin that chose
	// what is started: the command, args or version_args. A plugin adds
	// only new IDs, so it chose all three unless the person's override set
	// each one again (no_version counts for version_args). Such a harness
	// is started only by a person's own probe. "" otherwise.
	PluginLaunch string
	launch       launchOrigin
}

// launchOrigin records which layer last chose each launch field.
type launchOrigin struct{ command, args, version string }

// Compose layers the definitions (Composite: a non-empty field of a higher
// layer replaces the field below; lists replace). A layer's definition that
// fails harnessdef.Validate, or that would leave an inconsistent result, is
// left out whole with a Problem, never half-applied. The result is sorted by
// ID.
func Compose(l Layers) ([]Layered, []Problem) {
	var problems []Problem
	byID := map[string]*Layered{}
	add := func(layer, id, code, msg string) {
		problems = append(problems, Problem{Layer: layer, ID: harnessdef.SafeID(id), Code: code, Message: msg})
	}
	if l.Catalog != nil {
		for _, p := range l.Catalog.Problems {
			add(SourceRegistry, p.ID, ProblemRegistry, "The pinned registry entry has a value that breaks agentflow's rules ("+p.Field+"): "+p.Message+". agentflow ignores that value.")
		}
		for i := range l.Catalog.Entries {
			e := &l.Catalog.Entries[i]
			d := harnessdef.Definition{Name: e.Name, Kind: harnessdef.KindNative}
			if cmd, args, err := acpregistry.DefaultCommand(e.Distribution, l.Platform); err == nil {
				if harnessdef.ValidCommandName(cmd) && !harnessdef.RefusedLauncher(cmd) {
					d.Command, d.Args = cmd, args
				} else {
					add(SourceRegistry, e.ID, ProblemRegistry, "The registry's command name for this platform is not one agentflow can look up on PATH. Name the command in Harness overrides on the Settings page.")
				}
			}
			byID[e.ID] = &Layered{ID: e.ID, Definition: d, Sources: []string{SourceRegistry}, Entry: e}
		}
	}

	apply := func(layer harnessdef.Layer, source, id string, d harnessdef.Definition, mayAdd bool) {
		if err := harnessdef.Validate(id, d, layer); err != nil {
			var ve *harnessdef.ValidationError
			code := harnessdef.CodeRule
			if errors.As(err, &ve) {
				code = ve.Code
			}
			add(source, id, code, err.Error())
			return
		}
		cur, ok := byID[id]
		if !ok {
			if !mayAdd {
				add(source, id, harnessdef.CodeRule, fmt.Sprintf("harness %q (%s): no pinned registry entry has this ID", id, source))
				return
			}
			cur = &Layered{ID: id, Definition: harnessdef.Definition{Kind: harnessdef.KindNative}}
		}
		merged := Merge(cur.Definition, d)
		if merged.StatusFrom == harnessdef.StatusFromBase && merged.Kind != harnessdef.KindAdapter {
			add(source, id, harnessdef.CodeStatus, fmt.Sprintf("harness %q (%s): status_from: base is only for kind adapter", id, source))
			return
		}
		cur.Definition = merged
		cur.Sources = append(cur.Sources, source)
		switch layer {
		case harnessdef.LayerPlugin:
			// A new ID: the plugin chose every launch field, empty or not.
			cur.launch = launchOrigin{command: source, args: source, version: source}
		default:
			if d.Command != "" {
				cur.launch.command = source
			}
			if len(d.Args) > 0 {
				cur.launch.args = source
			}
			if len(d.VersionArgs) > 0 || d.NoVersion {
				cur.launch.version = source
			}
		}
		cur.PluginLaunch = ""
		for _, o := range []string{cur.launch.command, cur.launch.args, cur.launch.version} {
			if strings.HasPrefix(o, SourcePluginPrefix) {
				cur.PluginLaunch = o
				break
			}
		}
		byID[id] = cur
	}

	for _, id := range sortedDefKeys(l.Overlay) {
		apply(harnessdef.LayerAgentflow, SourceAgentflow, id, l.Overlay[id], false)
	}
	taken := map[string]bool{}
	for id := range byID {
		taken[id] = true
	}
	plugins := append([]PluginHarness(nil), l.Plugins...)
	sort.SliceStable(plugins, func(i, j int) bool {
		if plugins[i].Plugin != plugins[j].Plugin {
			return plugins[i].Plugin < plugins[j].Plugin
		}
		return plugins[i].ID < plugins[j].ID
	})
	for _, p := range plugins {
		source := SourcePluginPrefix + p.Plugin
		if taken[p.ID] {
			add(source, p.ID, harnessdef.CodeIDTaken, fmt.Sprintf("harness %q (%s): the ID is already used by the registry, agentflow or another plugin", p.ID, source))
			continue
		}
		taken[p.ID] = true
		apply(harnessdef.LayerPlugin, source, p.ID, p.Definition, true)
	}
	for _, id := range sortedDefKeys(l.Personal) {
		apply(harnessdef.LayerPersonal, SourcePersonal, id, l.Personal[id], true)
	}

	disabled := map[string]bool{}
	for _, id := range l.Disabled {
		disabled[id] = true
	}
	out := make([]Layered, 0, len(byID))
	for id, v := range byID {
		if !disabled[id] {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, problems
}

// Merge returns base with every non-empty field of top: strings and lists
// replace, NoVersion is set when either sets it.
func Merge(base, top harnessdef.Definition) harnessdef.Definition {
	str := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	list := func(dst *[]string, v []string) {
		if len(v) > 0 {
			*dst = append([]string(nil), v...)
		}
	}
	ints := func(dst *[]int, v []int) {
		if len(v) > 0 {
			*dst = append([]int(nil), v...)
		}
	}
	out := base
	str(&out.Name, top.Name)
	if top.Kind != "" {
		out.Kind = top.Kind
	}
	str(&out.Command, top.Command)
	list(&out.Args, top.Args)
	list(&out.VersionArgs, top.VersionArgs)
	out.NoVersion = out.NoVersion || top.NoVersion
	str(&out.BaseName, top.BaseName)
	list(&out.BaseCommands, top.BaseCommands)
	str(&out.MinVersion, top.MinVersion)
	str(&out.StatusFrom, top.StatusFrom)
	list(&out.StatusArgs, top.StatusArgs)
	ints(&out.StatusSignedIn, top.StatusSignedIn)
	ints(&out.StatusSignedOut, top.StatusSignedOut)
	str(&out.StatusSignedOutLine, top.StatusSignedOutLine)
	list(&out.CredentialEnv, top.CredentialEnv)
	list(&out.ConfigEnv, top.ConfigEnv)
	str(&out.StatusSource, top.StatusSource)
	return out
}

// versionArgs are the definition's version arguments, or ["--version"].
func versionArgs(d harnessdef.Definition) []string {
	if len(d.VersionArgs) > 0 {
		return d.VersionArgs
	}
	return []string{"--version"}
}

func sortedDefKeys(m map[string]harnessdef.Definition) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// HighestSource is the highest layer that shaped a harness (the last of
// Harness.Sources), or "registry": the Source of a harness.* event payload.
func HighestSource(sources []string) string {
	if len(sources) == 0 {
		return SourceRegistry
	}
	return sources[len(sources)-1]
}
