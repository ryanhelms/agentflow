package webui

import (
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Phase 5 plugins projection state (lane 5.2). The contract is in the ADR,
// section "Phase 5 plugins projection". Changing a shape needs an
// amendment.

// ProjectionPlugins is the plugins projection's name.
const ProjectionPlugins = "plugins"

// MaxPluginLetters caps PluginsState.DeadLetters.
const MaxPluginLetters = 20

// PluginsState is the "plugins" state. Key: the repository (Worktree
// empty). Topics: global/*/plugin.lifecycle.changed,
// global/*/plugin.delivery.failed, global/*/config.layer.changed and the
// repository's own config.layer.changed. It reads the plugin host's
// status, the registry and both configuration layers, never event
// payloads, so a missed event is repaired by the next one or a snapshot.
type PluginsState struct {
	// Host is false when the daemon runs no plugin host (it failed to
	// start); Plugins then lists the registry with state "".
	Host    bool         `json:"host"`
	Plugins []PluginItem `json:"plugins"`
	// DeadLetters are the newest letters of every plugin, newest first,
	// at most MaxPluginLetters.
	DeadLetters []PluginLetter `json:"dead_letters"`
}

// PluginItem is one installed plugin, sorted by ID.
type PluginItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version"`
	Tier        string `json:"tier"`
	// Hash is the installed hash, "sha256:<hex>".
	Hash        string    `json:"hash"`
	InstalledAt time.Time `json:"installed_at"`
	// Enabled is personal:plugins.<id>.enabled. DisabledHere is true when
	// this repository lists the plugin in repo:plugins.disabled: its
	// handlers get no events from here and cannot act here.
	Enabled      bool `json:"enabled"`
	DisabledHere bool `json:"disabled_here,omitempty"`
	// State, Code, Since, Restarts and RetryAt come from the host.
	State    bus.PluginState `json:"state"`
	Code     string          `json:"code,omitempty"`
	Since    *time.Time      `json:"since,omitempty"`
	Restarts int             `json:"restarts,omitempty"`
	RetryAt  *time.Time      `json:"retry_at,omitempty"`
	// Counters since the daemon started, as of the last plugin event.
	Delivered   uint64 `json:"delivered"`
	DeadLetters uint64 `json:"dead_letters"`
	Dropped     uint64 `json:"dropped"`
	Invocations uint64 `json:"invocations"`
	Published   uint64 `json:"published"`
	// Extensions are what the plugin adds, one entry per handler or rule.
	Extensions []PluginExtension `json:"extensions"`
	// Permissions are what it was granted at install.
	Permissions PluginPermissions `json:"permissions"`
	// SettingsGroup is the Settings page group, "plugins.<id>".
	SettingsGroup string `json:"settings_group"`
}

// PluginExtension is one contribution.
type PluginExtension struct {
	// Point is handler or permission_rule.
	Point string `json:"point"`
	Name  string `json:"name"`
	// On are a handler's event patterns.
	On []string `json:"on,omitempty"`
	// Invokes and Annotates summarise a declarative handler's actions.
	Invokes   []string `json:"invokes,omitempty"`
	Annotates bool     `json:"annotates,omitempty"`
	// Deny is a permission rule's message.
	Deny string `json:"deny,omitempty"`
}

// PluginPermissions mirrors manifest.Permissions.
type PluginPermissions struct {
	Subscribe []string `json:"subscribe"`
	Publish   []string `json:"publish"`
	Invoke    []string `json:"invoke"`
}

// PluginLetter is one dead letter, without the event's payload.
type PluginLetter struct {
	Plugin    string     `json:"plugin"`
	EventID   string     `json:"event_id"`
	EventType bus.Type   `json:"event_type"`
	EventRepo bus.RepoID `json:"event_repo"`
	Code      string     `json:"code"`
	Time      time.Time  `json:"time"`
}
