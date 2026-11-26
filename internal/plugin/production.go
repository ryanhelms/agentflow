package plugin

import (
	"os"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// The production readers and launcher, used by the daemon's host and by
// the permission_rule Stage in every process. The readers are nil until
// the registry reader (manifest.NewFileSource) and the settings reader are
// wired; nil means no plugin is installed.
var (
	productionSource func() manifest.Source
	productionLayers func() Layers
	productionLaunch = jsonrpc.Launch
)

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
