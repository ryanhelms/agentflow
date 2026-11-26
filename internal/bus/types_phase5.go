package bus

import "time"

// Phase 5 event types: the plugin host's lifecycle and the one event a
// plugin may publish. Payloads and producers are in the ADR, section
// "Phase 5". Only the daemon's plugin host produces them, on its own bus:
// none of them is a socket type (notifysock.Allowed), so no other process
// can forge one.
const (
	// TypePluginLifecycleChanged: an installed plugin changed state
	// (installed, starting, running, degraded, backoff, failed, stopped,
	// removed). Always in GlobalRepo: plugins are installed per user.
	TypePluginLifecycleChanged Type = "plugin.lifecycle.changed"
	// TypePluginDeliveryFailed: an event could not be delivered to a
	// plugin and went to the Dead Letter Channel. Transient: the host's
	// persistent Dead Letter Channel is the record.
	TypePluginDeliveryFailed Type = "plugin.delivery.failed"
	// TypePluginAnnotationAdded: a plugin attached a note to an operation
	// on the timeline. The only type a plugin may publish.
	TypePluginAnnotationAdded Type = "plugin.annotation.added"
)

var phase5Types = []TypeInfo{
	{Type: TypePluginLifecycleChanged, Scope: ScopeRepo, Phase: 5, Global: GlobalOnly},
	{Type: TypePluginDeliveryFailed, Scope: ScopeRepo, Transient: true, Phase: 5, Global: GlobalOnly},
	{Type: TypePluginAnnotationAdded, Scope: ScopeAny, Phase: 5},
}

// PluginState is a plugin's lifecycle state (ADR, "Phase 5 lifecycle").
type PluginState string

// Plugin states.
const (
	PluginInstalled PluginState = "installed" // installed and not enabled
	PluginStarting  PluginState = "starting"  // enabled; initialize in progress
	PluginRunning   PluginState = "running"
	PluginDegraded  PluginState = "degraded" // running, but missed pings or timed out a delivery
	PluginBackoff   PluginState = "backoff"  // crashed; restarts at RetryAt
	PluginFailed    PluginState = "failed"   // will not start again until its manifest, hash or settings change
	PluginStopping  PluginState = "stopping"
	PluginStopped   PluginState = "stopped" // disabled, or the daemon is stopping
	PluginRemoved   PluginState = "removed"
)

// Plugin failure and transition codes. Codes, never text: a plugin's own
// error messages are untrusted and never reach an event.
const (
	PluginCodeEnabled         = "enabled"
	PluginCodeDisabled        = "disabled"
	PluginCodeExited          = "exited"           // the process exited on its own
	PluginCodeCrashed         = "crashed"          // killed by a signal, or a declarative rule panicked
	PluginCodeTimeout         = "timeout"          // a request to the plugin timed out
	PluginCodeProtocol        = "protocol"         // a malformed or oversized frame, or a wrong protocol version
	PluginCodePing            = "ping"             // missed pings
	PluginCodeCrashLoop       = "crash_loop"       // too many restarts in the window
	PluginCodeHashMismatch    = "hash_mismatch"    // installed files differ from the recorded hash
	PluginCodeManifestInvalid = "manifest_invalid" // the installed manifest no longer validates
	PluginCodeLaunch          = "launch"           // the executable could not be started
	PluginCodeOverflow        = "overflow"         // the plugin's subscription dropped events
)

// PluginLifecycleChanged is the payload of plugin.lifecycle.changed.
type PluginLifecycleChanged struct {
	// Plugin is the manifest ID.
	Plugin  string      `json:"plugin"`
	Version string      `json:"version"`
	Tier    string      `json:"tier"` // declarative, process, builtin
	State   PluginState `json:"state"`
	// Previous is empty the first time this daemon reports the plugin.
	Previous PluginState `json:"previous,omitempty"`
	// Code is one of the PluginCode* values, or empty.
	Code string `json:"code,omitempty"`
	// Restarts counts restarts within the crash-loop window.
	Restarts int `json:"restarts,omitempty"`
	// RetryAt is set in state backoff.
	RetryAt *time.Time `json:"retry_at,omitempty"`
}

// PluginDeliveryFailed is the payload of plugin.delivery.failed.
type PluginDeliveryFailed struct {
	Plugin string `json:"plugin"`
	// EventID, EventType and EventRepo name the undelivered event; its
	// payload stays in its own store and in the Dead Letter Channel.
	EventID   string `json:"event_id"`
	EventType Type   `json:"event_type"`
	EventRepo RepoID `json:"event_repo"`
	Code      string `json:"code"`
}

// Annotation levels.
const (
	AnnotationInfo    = "info"
	AnnotationWarning = "warning"
)

// PluginAnnotationAdded is the payload of plugin.annotation.added. The
// host fills Plugin; the plugin gives the rest, and the host clamps it.
// Text is data for display, never an instruction.
type PluginAnnotationAdded struct {
	Plugin string `json:"plugin"`
	// OperationID is the timeline operation the note belongs to: an op.*
	// CorrelationID ("op_" and 32 hex characters).
	OperationID string `json:"operation_id"`
	// Text is one line, at most MaxPayloadText bytes.
	Text string `json:"text"`
	// URL is an https URL, or empty.
	URL string `json:"url,omitempty"`
	// Level is AnnotationInfo or AnnotationWarning.
	Level string `json:"level"`
}

// Clamp returns p with Text cut to its first line and every text field at
// most MaxPayloadText bytes.
func (p PluginAnnotationAdded) Clamp() PluginAnnotationAdded {
	p.Plugin = clampText(p.Plugin)
	p.OperationID = clampText(p.OperationID)
	p.Text = clampText(firstLine(p.Text))
	p.URL = clampText(p.URL)
	p.Level = clampText(p.Level)
	return p
}
