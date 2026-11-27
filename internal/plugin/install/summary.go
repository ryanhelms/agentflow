package install

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// Summary is what the confirmation of plugin add shows (ADR "Phase 5
// install", step 4): the plugin's identity and content hash, every
// extension, every permission, the executable of a process plugin, and what
// an upgrade replaces. The confirmation token is bound to it, so a change to
// the files between the preview and the apply fails with operation_changed.
//
// Name and the extension lines come from the plugin and are display text for
// the person reading the confirmation, never instructions.
type Summary struct {
	// Operation is "install", "upgrade" or "repair".
	Operation string `json:"operation"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Tier      string `json:"tier"`
	Hash      string `json:"hash"`
	// Source is the absolute path that was given.
	Source string `json:"source"`
	// Exec is the executable of a process plugin, relative to its
	// directory, with its arguments.
	Exec *manifest.Exec `json:"exec,omitempty"`
	// Extensions has one line per handler and permission rule.
	Extensions []string `json:"extensions"`
	// Permissions is everything the plugin may do: the events it may
	// receive, the events it may publish and the commands it may run.
	Permissions manifest.Permissions `json:"permissions"`
	// Settings lists the keys of the plugin's own settings.
	Settings []string `json:"settings"`
	// Replaces names the version an upgrade replaces.
	Replaces *Replaced `json:"replaces,omitempty"`
	// Warnings say what the person must know before they agree.
	Warnings []string `json:"warnings"`
	// Next says how the plugin is turned on or off.
	Next string `json:"next"`
}

// Replaced is the version an upgrade replaces.
type Replaced struct {
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

// Summary builds the confirmation summary. enabled says whether the personal
// layer already enables a plugin with this ID: a new copy then starts as soon
// as the console sees it.
func (p *AddPlan) Summary(enabled bool) Summary {
	m := p.Manifest
	s := Summary{Operation: "install", ID: m.ID, Name: m.Name, Version: m.Version, Tier: string(m.Tier), Hash: p.Hash,
		Source: p.Source, Exec: m.Exec, Extensions: DescribeExtensions(m), Permissions: listsNotNil(m.Permissions),
		Settings: []string{}, Warnings: []string{}}
	for _, st := range m.Settings {
		s.Settings = append(s.Settings, st.Key)
	}
	switch p.Outcome {
	case Upgraded:
		s.Operation = "upgrade"
		s.Replaces = &Replaced{Version: p.Previous.Version, Hash: p.Previous.Hash}
	case Repaired:
		s.Operation = "repair"
		s.Warnings = append(s.Warnings, "The installed copy no longer matches its recorded hash: files were changed after the plugin was added. This writes the copy again from the path above.")
	}
	if m.Tier == manifest.TierProcess {
		s.Warnings = append(s.Warnings, fmt.Sprintf("This plugin is a program (%s) that runs as you, with your files and network. "+
			"agentflow gives it a clean environment, but that is not a sandbox. Add it only if you trust its author and have read its code.", m.Exec.Command))
	} else {
		s.Warnings = append(s.Warnings, "This plugin runs no code of its own: agentflow reads its rules and does only what its permissions allow.")
	}
	if writes := writeCommands(m.Permissions.Invoke); len(writes) > 0 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("Writes the plugin starts (%s) wait for your approval before they run.", strings.Join(writes, ", ")))
	}
	if enabled {
		s.Warnings = append(s.Warnings, fmt.Sprintf("plugins.%s.enabled is already true in your settings, so this version runs as soon as the console sees it.", m.ID))
		s.Next = fmt.Sprintf("It is turned on. To turn it off, run: agentflow config set plugins.%s.enabled=false", m.ID)
	} else {
		s.Next = fmt.Sprintf("It stays turned off until you turn it on: open Settings and tick plugins.%s.enabled, or run: agentflow config set plugins.%s.enabled=true", m.ID, m.ID)
	}
	return s
}

// listsNotNil returns p with empty lists instead of nil, so a summary and
// its fingerprint do not depend on how the manifest was written.
func listsNotNil(p manifest.Permissions) manifest.Permissions {
	for _, l := range []*[]string{&p.Subscribe, &p.Publish, &p.Invoke} {
		if *l == nil {
			*l = []string{}
		}
	}
	return p
}

// writeCommands are the commands in invoke that write.
func writeCommands(invoke []string) []string {
	var out []string
	for _, c := range []string{"notify", "comment"} {
		if slices.Contains(invoke, c) {
			out = append(out, c)
		}
	}
	return out
}

// DescribeExtensions describes each handler, permission rule and harness of
// m in one line. The lines are display text and stay stable for one manifest.
func DescribeExtensions(m manifest.Manifest) []string {
	out := []string{}
	for _, h := range m.Extensions.Handler {
		line := fmt.Sprintf("handler %s: on %s", h.Name, strings.Join(h.On, ", "))
		if m.Tier == manifest.TierDeclarative {
			var acts []string
			for _, a := range h.Actions {
				switch {
				case a.Invoke != nil:
					acts = append(acts, "invoke "+a.Invoke.Command)
				case a.Annotate != nil:
					acts = append(acts, "annotate")
				}
			}
			line += fmt.Sprintf(" (%d conditions) then %s", len(h.When), strings.Join(acts, ", "))
		} else {
			line += " (the program decides what to do)"
		}
		out = append(out, line)
	}
	for _, r := range m.Extensions.PermissionRule {
		out = append(out, fmt.Sprintf("permission rule %s: denies %s with %q", r.Name, matchText(r.Match), r.Deny))
	}
	// A harness is data, but its command and arguments are what agentflow
	// starts when you probe it: show all of them. A plugin's harness is
	// never probed automatically or for an agent, only when you ask.
	for _, h := range m.Extensions.Harness {
		if h.Command == "" {
			out = append(out, "harness "+h.ID+": names no command")
			continue
		}
		line := "harness " + h.ID + ": when you probe it, runs " + strings.Join(append([]string{h.Command}, h.Args...), " ")
		switch {
		case h.NoVersion:
			line += ", no version command"
		default:
			version := h.VersionArgs
			if len(version) == 0 {
				version = []string{"--version"}
			}
			line += ", and for its version " + strings.Join(append([]string{h.Command}, version...), " ")
		}
		if len(h.BaseCommands) > 0 {
			line += " (adapter for " + strings.Join(h.BaseCommands, ", ") + ")"
		}
		out = append(out, line+"; found on your PATH, only when you probe it yourself")
	}
	return out
}

func matchText(m manifest.RuleMatch) string {
	var parts []string
	for _, kv := range []struct {
		name string
		list []string
	}{{"operation", m.Operation}, {"class", m.Class}, {"action", m.Action}, {"origin", m.Origin}, {"branch", m.Branch}} {
		if len(kv.list) > 0 {
			parts = append(parts, kv.name+" "+strings.Join(kv.list, " or "))
		}
	}
	return strings.Join(parts, ", ")
}

// RemoveSummary is what the confirmation of plugin remove shows.
type RemoveSummary struct {
	Operation string `json:"operation"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Tier      string `json:"tier"`
	Hash      string `json:"hash"`
	// Deletes says what is removed, one line each.
	Deletes  []string `json:"deletes"`
	Warnings []string `json:"warnings"`
}

// RemovePlan is what plugin remove would delete.
type RemovePlan struct {
	Entry manifest.Installed
}

// PlanRemove finds the installed plugin id. It returns manifest.ErrNotFound
// (wrapped, naming the ID) when no such plugin is installed, and an error
// for an ID that cannot name a plugin.
func PlanRemove(home, id string) (*RemovePlan, error) {
	if manifest.RootPath(home, id) == "" {
		return nil, fmt.Errorf("%q is not a plugin ID: IDs are lowercase letters, digits and hyphens", id)
	}
	reg, err := manifest.ReadRegistry(home)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(reg.Plugins, func(p manifest.Installed) bool { return p.ID == id })
	if i < 0 {
		return nil, fmt.Errorf("%w: %s", manifest.ErrNotFound, id)
	}
	return &RemovePlan{Entry: reg.Plugins[i]}, nil
}

// Summary builds the confirmation summary. settings says whether the
// personal layer holds a plugins.<id> block that the removal also deletes.
func (p *RemovePlan) Summary(settings bool) RemoveSummary {
	e := p.Entry
	s := RemoveSummary{Operation: "remove", ID: e.ID, Name: e.Manifest.Name, Version: e.Version, Tier: string(e.Tier), Hash: e.Hash,
		Deletes: []string{
			fmt.Sprintf("the installed copy of %s %s", e.ID, e.Version),
			"the plugin's data directory, and everything the plugin stored in it",
		},
		Warnings: []string{"A running plugin is stopped when the console sees the change. Removing cannot be undone: add the plugin again to get it back."}}
	if settings {
		s.Deletes = append(s.Deletes, fmt.Sprintf("plugins.%s in your personal settings: whether it was enabled and its settings", e.ID))
	}
	return s
}
