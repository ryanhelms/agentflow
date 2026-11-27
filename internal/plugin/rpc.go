package plugin

import (
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// JSON-RPC methods between the host and a process plugin (ADR, "Phase 5
// JSON-RPC methods"). Framing is internal/jsonrpc: JSON-RPC 2.0, one
// object per line on the child's stdin and stdout.

// Host to plugin.
const (
	// MethodInitialize: request, InitializeParams -> InitializeResult. The
	// first message. Nothing else is sent before its answer.
	MethodInitialize = "initialize"
	// MethodEventDeliver: request, EventDeliverParams -> {}. One at a time,
	// in bus order; the next waits for the answer (back-pressure).
	MethodEventDeliver = "event.deliver"
	// MethodSettingsChanged: notification, SettingsChangedParams.
	MethodSettingsChanged = "settings.changed"
	// MethodPing: request, {} -> {}.
	MethodPing = "ping"
	// MethodShutdown: request, {} -> {}. After the answer the host closes
	// stdin; the plugin exits within ShutdownTimeout or is killed.
	MethodShutdown = "shutdown"
)

// Plugin to host. Allowed only after initialize succeeded.
const (
	// MethodSubscribe: request, SubscribeParams -> SubscribeResult.
	MethodSubscribe = "subscribe"
	// MethodCommandInvoke: request, InvokeRequest -> InvokeResult.
	MethodCommandInvoke = "command.invoke"
	// MethodEventPublish: request, PublishRequest -> PublishResult.
	MethodEventPublish = "event.publish"
	// MethodLog: notification, LogParams.
	MethodLog = "log"
)

// InitializeParams is the host's initialize request.
type InitializeParams struct {
	ProtocolVersion int `json:"protocol_version"` // manifest.ProtocolVersion
	// Agentflow is the agentflow version, for display.
	Agentflow string      `json:"agentflow"`
	Plugin    PluginIdent `json:"plugin"`
	// Settings are Env.Settings.
	Settings map[string]any `json:"settings"`
	// Permissions are the granted permissions, as installed.
	Permissions manifest.Permissions `json:"permissions"`
	Limits      WireLimits           `json:"limits"`
}

// PluginIdent names the plugin being initialized.
type PluginIdent struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// WireLimits tells the plugin the host's limits.
type WireLimits struct {
	MaxFrame           int `json:"max_frame"`
	DeliverTimeoutMS   int `json:"deliver_timeout_ms"`
	InvokesPerMinute   int `json:"invokes_per_minute"`
	PublishesPerMinute int `json:"publishes_per_minute"`
}

// InitializeResult is the plugin's answer. A ProtocolVersion other than
// the host's fails the start with code protocol.
type InitializeResult struct {
	ProtocolVersion int `json:"protocol_version"`
}

// EventDeliverParams carries one event: the full envelope, as stored.
type EventDeliverParams struct {
	Event bus.Event `json:"event"`
}

// SettingsChangedParams carries the new effective settings.
type SettingsChangedParams struct {
	Settings map[string]any `json:"settings"`
}

// SubscribeParams replaces the subscription set. Types are bus.Pattern
// values, each granted.
type SubscribeParams struct {
	Types []string `json:"types"`
}

// SubscribeResult lists the accepted patterns.
type SubscribeResult struct {
	Types []string `json:"types"`
}

// LogParams is one log line. Level is debug, info, warn or error; Message
// is at most 4 KiB and written to the plugin's log file only.
type LogParams struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}
