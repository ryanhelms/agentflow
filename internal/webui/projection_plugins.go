package webui

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// The plugins projection (ADR, "Phase 5 plugins projection"). One
// PluginsState per repository, read from the host's status and letters,
// the registry and both configuration layers, never from event payloads.

func init() {
	projectionExtensions = append(projectionExtensions, func(r *repoEvents) Projection {
		return &pluginsProjection{repo: r, runtime: currentPlugins}
	})
}

type pluginsProjection struct {
	repo    *repoEvents
	runtime func() pluginsRuntime
}

func (p *pluginsProjection) Name() string { return ProjectionPlugins }

func (p *pluginsProjection) Topics() []bus.Topic {
	return []bus.Topic{
		{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypePluginLifecycleChanged)},
		{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypePluginDeliveryFailed)},
		{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypeConfigLayerChanged)},
		{Repo: p.repo.id, Worktree: bus.Any, Types: bus.Pattern(bus.TypeConfigLayerChanged)},
	}
}

func (p *pluginsProjection) Keys(e bus.Event) []ProjectionKey {
	if (e.Repo == bus.GlobalRepo || e.Repo == p.repo.id) && bus.MatchAny(p.Topics(), e) {
		return p.AllKeys()
	}
	return nil
}

func (p *pluginsProjection) AllKeys() []ProjectionKey { return []ProjectionKey{{Repo: p.repo.id}} }

func (p *pluginsProjection) State(ctx context.Context, key ProjectionKey) (json.RawMessage, error) {
	if key.Repo != p.repo.id || key.Worktree != "" {
		return jsonNull, nil
	}
	st, err := pluginsStateOf(ctx, p.runtime(), p.repoRoot())
	if err != nil {
		return nil, err
	}
	return json.Marshal(st)
}

// repoRoot is the root of the repository's main worktree, else any.
func (p *pluginsProjection) repoRoot() string {
	root := ""
	for _, id := range p.repo.worktreeIDs() {
		if w, ok := p.repo.worktree(id); ok && (root == "" || w.GitDir == w.Common) {
			root = w.Root
		}
	}
	return root
}

// pluginsStateOf builds the state. root is "" when the repository has no
// worktree attached.
func pluginsStateOf(ctx context.Context, rt pluginsRuntime, root string) (PluginsState, error) {
	st := PluginsState{Host: rt.host != nil, Plugins: []PluginItem{}, DeadLetters: []PluginLetter{}}
	if rt.source == nil || rt.layers == nil {
		return st, nil
	}
	installed, err := rt.source.Installed(ctx)
	if err != nil {
		return st, err
	}
	personal, err := rt.layers.Personal(ctx)
	if err != nil {
		return st, err
	}
	var disabled []string
	if root != "" {
		disabled, _ = rt.layers.RepoDisabled(ctx, root)
	}
	status := map[string]plugin.Status{}
	if rt.host != nil {
		for _, s := range rt.host.Status() {
			status[s.ID] = s
		}
	}
	for _, in := range installed {
		st.Plugins = append(st.Plugins, pluginItem(in, personal[in.ID].Enabled, slices.Contains(disabled, in.ID), status[in.ID]))
	}
	if rt.host != nil {
		letters := rt.host.Letters()
		for i := len(letters) - 1; i >= 0 && len(st.DeadLetters) < MaxPluginLetters; i-- {
			l := letters[i]
			st.DeadLetters = append(st.DeadLetters, PluginLetter{Plugin: trimPluginPrefix(l.Consumer), EventID: l.Event.ID,
				EventType: l.Event.Type, EventRepo: l.Event.Repo, Code: l.Reason, Time: l.Time})
		}
	}
	return st, nil
}

func trimPluginPrefix(consumer string) string {
	if len(consumer) > len(plugin.OriginPrefix) && consumer[:len(plugin.OriginPrefix)] == plugin.OriginPrefix {
		return consumer[len(plugin.OriginPrefix):]
	}
	return consumer
}

func pluginItem(in manifest.Installed, enabled, disabledHere bool, s plugin.Status) PluginItem {
	m := in.Manifest
	item := PluginItem{ID: in.ID, Name: m.Name, Description: m.Description, Version: in.Version, Tier: string(in.Tier), Hash: in.Hash,
		InstalledAt: in.InstalledAt, Enabled: enabled, DisabledHere: disabledHere, State: s.State, Code: s.Code, Restarts: s.Restarts,
		RetryAt: s.RetryAt, Delivered: s.Delivered, DeadLetters: s.DeadLetters, Dropped: s.Dropped, Invocations: s.Invocations,
		Published: s.Published, Extensions: []PluginExtension{}, SettingsGroup: manifest.GroupPrefix + in.ID,
		Permissions: PluginPermissions{Subscribe: orEmpty(m.Permissions.Subscribe), Publish: orEmpty(m.Permissions.Publish), Invoke: orEmpty(m.Permissions.Invoke)}}
	if !s.Since.IsZero() {
		since := s.Since
		item.Since = &since
	}
	for _, h := range m.Extensions.Handler {
		ext := PluginExtension{Point: string(manifest.PointHandler), Name: h.Name, On: h.On}
		for _, a := range h.Actions {
			if a.Invoke != nil && !slices.Contains(ext.Invokes, a.Invoke.Command) {
				ext.Invokes = append(ext.Invokes, a.Invoke.Command)
			}
			if a.Annotate != nil {
				ext.Annotates = true
			}
		}
		item.Extensions = append(item.Extensions, ext)
	}
	for _, r := range m.Extensions.PermissionRule {
		item.Extensions = append(item.Extensions, PluginExtension{Point: string(manifest.PointPermissionRule), Name: r.Name, Deny: plugin.SafeText(r.Deny, plugin.MaxDenyMessage)})
	}
	return item
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
