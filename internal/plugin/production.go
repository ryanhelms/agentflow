package plugin

import (
	"context"
	"os"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// The production readers and launcher, used by the daemon's host and by
// the permission_rule Stage in every process.
var (
	productionSource = fileSource
	productionLayers = func() Layers { return configLayers{} }
	productionLaunch = jsonrpc.Launch
)

// fileSource is the registry of the agentflow home plus the builtins, or
// nil when the home cannot be found.
func fileSource() manifest.Source {
	home, err := config.HomeDir()
	if err != nil {
		return nil
	}
	return manifest.NewFileSource(home).WithBuiltins(builtinInstalled)
}

// builtinInstalled lists the registered builtins as registry entries.
func builtinInstalled() []manifest.Installed {
	var out []manifest.Installed
	for _, b := range Builtins() {
		m := b.Manifest
		out = append(out, manifest.Installed{ID: m.ID, Version: m.Version, Tier: manifest.TierBuiltin, Manifest: m})
	}
	return out
}

// configLayers reads the plugin sections of the personal and repository
// configuration files on every call.
type configLayers struct{}

func (configLayers) Personal(context.Context) (map[string]PersonalPlugin, error) {
	u, err := config.LoadUser()
	if err != nil {
		return nil, err
	}
	out := make(map[string]PersonalPlugin, len(u.Plugins))
	for id, p := range u.Plugins {
		out[id] = PersonalPlugin{Enabled: p.Enabled, Settings: p.Settings}
	}
	return out, nil
}

func (configLayers) RepoDisabled(_ context.Context, root string) ([]string, error) {
	cfg, _, err := config.LoadRepoCore(root)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	return cfg.Plugins.Disabled, nil
}

// productionEnviron is the scrubbed environment of a process plugin: the
// daemon's own environment never passes except jsonrpc.EnvAllowlist.
func productionEnviron(data string, extra map[string]string) ([]string, error) {
	return jsonrpc.ScrubbedEnv(os.Environ(), data, extra)
}

// DefaultSource returns the production registry reader, or nil.
func DefaultSource() manifest.Source {
	if productionSource == nil {
		return nil
	}
	return productionSource()
}

// DefaultLayers returns the production configuration reader, or nil.
func DefaultLayers() Layers {
	if productionLayers == nil {
		return nil
	}
	return productionLayers()
}

// DefaultOptions fills the production readers and launcher into o.
func DefaultOptions(o Options) Options {
	if o.Source == nil {
		o.Source = DefaultSource()
	}
	if o.Layers == nil {
		o.Layers = DefaultLayers()
	}
	if o.Launch == nil {
		o.Launch = productionLaunch
	}
	if o.Environ == nil {
		o.Environ = productionEnviron
	}
	return o
}
