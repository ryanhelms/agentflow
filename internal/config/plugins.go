package config

// Plugin configuration (ADR docs/knowledge/engineering/decisions/event-driven-platform.md,
// section "Phase 5 settings"). A plugin is installed per user (agentflow
// plugin add) and enabled in the personal layer; a repository can only turn
// a plugin's handlers off for itself. Neither section installs, enables or
// loosens anything for a repository.

// UserPlugin is the personal layer's section of one installed plugin:
// plugins.<id> in ~/.agentflow/config.yml.
type UserPlugin struct {
	// Enabled starts the plugin. It is false until the person turns it on:
	// enabling is a loosening edit in the Settings page.
	Enabled bool `yaml:"enabled"`
	// Settings are the values of the plugin's own settings (plugins.<id>.settings.<key>).
	// The plugin's manifest declares and validates them, so they are untyped here.
	Settings map[string]any `yaml:"settings,omitempty"`
}

// RepoPlugins is the repository layer's plugins section.
type RepoPlugins struct {
	// Disabled lists plugin IDs whose handlers do not run for this
	// repository. It can only turn plugins off, never on, and never drops a
	// person's permission rules.
	Disabled []string `yaml:"disabled,omitempty"`
}
