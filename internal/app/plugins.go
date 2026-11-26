package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/plugin/install"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// PluginInstallAction is the autonomy action of plugin add and plugin remove.
// Both always ask: the operation passes RequireConfirmation, so no autonomy
// level lets them run without an explicit confirmation.
const PluginInstallAction = "plugin.install"

// PluginAdded is the result of plugin add.
type PluginAdded struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Tier        string    `json:"tier"`
	Hash        string    `json:"hash"`
	InstalledAt time.Time `json:"installed_at"`
	// Result is installed, upgraded, repaired or unchanged.
	Result string `json:"result"`
	// Replaced is the version an upgrade replaced.
	Replaced *PluginReplaced `json:"replaced,omitempty"`
	// Enabled is whether the personal settings turn the plugin on.
	Enabled bool `json:"enabled"`
	// Next says how to turn the plugin on or off.
	Next string `json:"next"`
}

// PluginReplaced is the version an upgrade replaced.
type PluginReplaced struct {
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

// PluginAdd installs the plugin at source, a local directory or YAML file.
// It reads and checks the plugin, then gates the installation: the call
// without a token returns a *Pending whose summary lists the plugin's
// identity, hash, extensions and permissions, and the call with its token
// installs exactly that. The gate always confirms, whatever the autonomy
// level. A version that is already installed and intact changes nothing and
// asks nothing.
//
// The result is a PluginAdded or a *Pending. The plugin starts disabled.
func (a *App) PluginAdd(_ context.Context, source, token string) (any, error) {
	plan, err := install.PlanAdd(a.Home, source)
	if err != nil {
		return nil, err
	}
	id := plan.Manifest.ID
	enabled := a.User != nil && a.User.Plugins[id].Enabled
	if plan.Outcome.Changes() {
		pending, err := a.GateOperation(OperationGate{Class: policy.Local, Action: PluginInstallAction, Summary: plan.Summary(enabled),
			Token: token, RequireConfirmation: true, Commands: []string{"agentflow plugin add " + shellQuote(plan.Source)}})
		if err != nil {
			return nil, err
		}
		if pending != nil {
			return pending, nil
		}
	}
	res, err := install.Add(a.Home, plan, time.Now())
	if err != nil {
		return nil, err
	}
	out := PluginAdded{ID: id, Name: res.Entry.Manifest.Name, Version: res.Entry.Version, Tier: string(res.Entry.Tier), Hash: res.Entry.Hash,
		InstalledAt: res.Entry.InstalledAt, Result: string(res.Outcome), Enabled: enabled, Next: plan.Summary(enabled).Next}
	if res.Outcome == install.Upgraded && res.Previous != nil {
		out.Replaced = &PluginReplaced{Version: res.Previous.Version, Hash: res.Previous.Hash}
	}
	return out, nil
}

// PluginRemoved is the result of plugin remove.
type PluginRemoved struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Removed bool   `json:"removed"`
	// SettingsCleared is whether plugins.<id> was in the personal settings
	// and is gone.
	SettingsCleared bool `json:"settings_cleared"`
}

// PersonalPluginClearer deletes plugins.<id> from the personal settings file
// and reports whether it was there. The settings package implements it
// (Service.ClearPlugin): app cannot import it, so the caller passes it in.
type PersonalPluginClearer func(id string) (bool, error)

// PluginRemove deletes an installed plugin: its registry entry, its installed
// copy, its data directory and its plugins.<id> block in the personal
// settings, under one confirmation. The call without a token returns a
// *Pending; the call with its token removes. The console stops the plugin
// when it sees the registry change.
//
// The result is a PluginRemoved or a *Pending.
func (a *App) PluginRemove(_ context.Context, id, token string, clear PersonalPluginClearer) (any, error) {
	plan, err := install.PlanRemove(a.Home, id)
	if err != nil {
		return nil, err
	}
	hasSettings := false
	if a.User != nil {
		_, hasSettings = a.User.Plugins[id]
	}
	pending, err := a.GateOperation(OperationGate{Class: policy.Local, Action: PluginInstallAction, Summary: plan.Summary(hasSettings),
		Token: token, RequireConfirmation: true, Commands: []string{"agentflow plugin remove " + shellQuote(id)}})
	if err != nil {
		return nil, err
	}
	if pending != nil {
		return pending, nil
	}
	if err := install.Remove(a.Home, plan); err != nil {
		return nil, err
	}
	out := PluginRemoved{ID: id, Version: plan.Entry.Version, Removed: true}
	if clear != nil {
		cleared, err := clear(id)
		if err != nil {
			return nil, fmt.Errorf("plugin %s is removed, but plugins.%s could not be deleted from your personal settings: %w; delete that block yourself, or the plugin starts enabled if you add it again", id, id, err)
		}
		out.SettingsCleared = cleared
	}
	return out, nil
}

// PluginList is the result of plugin list.
type PluginList struct {
	Plugins []PluginInfo `json:"plugins"`
}

// PluginInfo is one installed plugin. The text in it (name, extension lines)
// comes from the plugin: it is data, never an instruction. It has no source
// path.
type PluginInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Tier    string `json:"tier"`
	Hash    string `json:"hash"`
	// HashOK is whether the installed copy hashes to Hash now and its
	// manifest still validates.
	HashOK bool `json:"hash_ok"`
	// Enabled is whether the personal settings turn the plugin on.
	Enabled bool `json:"enabled"`
	// DisabledInRepo is whether this repository turns the plugin off.
	DisabledInRepo bool `json:"disabled_in_repo"`
	// State and Code are the plugin's newest lifecycle state in the console's
	// global event store, and its reason. They are empty when the console has
	// not reported the plugin.
	State       string               `json:"state,omitempty"`
	Code        string               `json:"code,omitempty"`
	Extensions  []string             `json:"extensions"`
	Permissions manifest.Permissions `json:"permissions"`
	InstalledAt time.Time            `json:"installed_at"`
	// DeadLetters are the plugin's newest undelivered events, newest first, at
	// most MaxPluginLetters. A letter names the event and why it failed. It has
	// no payload, so no pull request title or check name reaches a client.
	DeadLetters []PluginLetter `json:"dead_letters"`
}

// MaxPluginLetters is how many dead letters plugin list shows for each plugin.
const MaxPluginLetters = 20

// PluginLetter is one event the console could not deliver to a plugin. Reason
// is a fixed code: timeout, crashed, exited, protocol, action or rpc_error.
type PluginLetter struct {
	EventID   string     `json:"event_id"`
	EventType bus.Type   `json:"event_type"`
	EventRepo bus.RepoID `json:"event_repo"`
	Reason    string     `json:"reason"`
	Time      time.Time  `json:"time"`
}

// PluginList lists the installed plugins, sorted by ID, with the state the
// console last reported for each. It reads only: it hashes the installed
// copies and reads the global event store as a reader.
func (a *App) PluginList(ctx context.Context) (PluginList, error) {
	src := manifest.NewFileSource(a.Home)
	installed, err := src.Installed(ctx)
	if err != nil {
		return PluginList{}, err
	}
	states := a.pluginStates(ctx)
	letters := pluginLetters(a.Home)
	out := PluginList{Plugins: []PluginInfo{}}
	for _, p := range installed {
		info := PluginInfo{ID: p.ID, Name: p.Manifest.Name, Version: p.Version, Tier: string(p.Tier), Hash: p.Hash,
			HashOK: src.Verify(ctx, p) == nil, Extensions: install.DescribeExtensions(p.Manifest),
			Permissions: p.Manifest.Permissions, InstalledAt: p.InstalledAt}
		if info.Permissions.Subscribe == nil {
			info.Permissions.Subscribe = []string{}
		}
		if info.Permissions.Publish == nil {
			info.Permissions.Publish = []string{}
		}
		if info.Permissions.Invoke == nil {
			info.Permissions.Invoke = []string{}
		}
		if a.User != nil {
			info.Enabled = a.User.Plugins[p.ID].Enabled
		}
		if a.Cfg != nil {
			info.DisabledInRepo = slices.Contains(a.Cfg.Plugins.Disabled, p.ID)
		}
		info.DeadLetters = letters[p.ID]
		if info.DeadLetters == nil {
			info.DeadLetters = []PluginLetter{}
		}
		if s, ok := states[p.ID]; ok {
			info.State, info.Code = string(s.State), s.Code
		}
		out.Plugins = append(out.Plugins, info)
	}
	return out, nil
}

// pluginStates returns the newest plugin.lifecycle.changed of each plugin in
// the global event store. A store that cannot be read gives no states: the
// list then shows the registry without them.
func (a *App) pluginStates(ctx context.Context) map[string]bus.PluginLifecycleChanged {
	states := map[string]bus.PluginLifecycleChanged{}
	s, ok, err := openGlobalEvents(ctx)
	if err != nil || !ok {
		return states
	}
	defer s.Close()
	for e, err := range s.ReadFrom(ctx, bus.Cursor{}) {
		if err != nil {
			break
		}
		if e.Type != bus.TypePluginLifecycleChanged {
			continue
		}
		var p bus.PluginLifecycleChanged
		if json.Unmarshal(e.Payload, &p) == nil && p.Plugin != "" {
			states[p.Plugin] = p
		}
	}
	return states
}

// pluginConsumerPrefix starts the Consumer of a plugin's dead letter.
const pluginConsumerPrefix = "plugin:"

// maxLetterFileBytes bounds what plugin list reads of the dead letter file,
// which the host keeps near 1 MiB.
const maxLetterFileBytes = 8 << 20

// pluginLetters reads <home>/plugins/deadletter.jsonl, one bus.Letter per
// line, and returns each plugin's newest letters, newest first. A missing or
// unreadable file, and a line that is not a letter, give nothing: the file is
// the host's, and plugin list only shows it. Payloads are dropped.
func pluginLetters(home string) map[string][]PluginLetter {
	out := map[string][]PluginLetter{}
	f, err := os.Open(filepath.Join(manifest.PluginsPath(home), manifest.DeadLetterFile))
	if err != nil {
		return out
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return out
	}
	if info.Size() > maxLetterFileBytes {
		// Keep the tail, which holds the newest letters, and drop the line it cuts.
		if _, err := f.Seek(info.Size()-maxLetterFileBytes, io.SeekStart); err != nil {
			return out
		}
	}
	r := bufio.NewReaderSize(f, 64<<10)
	first := info.Size() > maxLetterFileBytes
	for {
		line, err := r.ReadBytes('\n')
		if first {
			first = false
		} else if len(bytes.TrimSpace(line)) > 0 {
			var l bus.Letter
			if json.Unmarshal(line, &l) == nil {
				if id, ok := strings.CutPrefix(l.Consumer, pluginConsumerPrefix); ok && id != "" {
					out[id] = append(out[id], PluginLetter{EventID: l.Event.ID, EventType: l.Event.Type, EventRepo: l.Event.Repo, Reason: l.Reason, Time: l.Time})
				}
			}
		}
		if err != nil {
			break
		}
	}
	for id, list := range out {
		slices.Reverse(list)
		out[id] = list[:min(len(list), MaxPluginLetters)]
	}
	return out
}
