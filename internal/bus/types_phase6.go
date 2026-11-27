package bus

// Phase 6 event types: harness detection (ADR, "Phase 6 events"). Only the
// daemon's harness source (internal/watch/harness.go) produces them, on its
// own bus, in GlobalRepo: harnesses are per user, not per repository. None
// of them is a socket type (notifysock.Allowed), so no other process can
// forge one. Payloads are codes and clamped versions: no path, no
// arguments, no agent text, no stderr.
const (
	TypeHarnessDetected Type = "harness.detected"
	TypeHarnessChanged  Type = "harness.changed"
	TypeHarnessRemoved  Type = "harness.removed"
)

var phase6Types = []TypeInfo{
	{Type: TypeHarnessDetected, Scope: ScopeRepo, Phase: 6, Global: GlobalOnly},
	{Type: TypeHarnessChanged, Scope: ScopeRepo, Phase: 6, Global: GlobalOnly},
	{Type: TypeHarnessRemoved, Scope: ScopeRepo, Phase: 6, Global: GlobalOnly},
}

// HarnessChanged is the payload of harness.detected and harness.changed.
type HarnessChanged struct {
	Harness     string `json:"harness"` // definition ID
	Kind        string `json:"kind"`    // native, adapter
	State       string `json:"state"`   // a harness.State value
	Previous    string `json:"previous,omitempty"`
	Code        string `json:"code,omitempty"`
	ProbeStatus string `json:"probe_status"`
	// SignIn is the sign-in verdict: signed_in, signed_out, unknown.
	SignIn  string `json:"sign_in,omitempty"`
	Version string `json:"version,omitempty"` // clamped
	// Source is the highest layer: registry, agentflow, plugin:<id>, personal.
	Source string `json:"source"`
	// Trigger: start, poll, focus, probe, config.
	Trigger string `json:"trigger"`
}

// HarnessRemoved is the payload of harness.removed.
type HarnessRemoved struct {
	Harness  string `json:"harness"`
	Previous string `json:"previous"`
	Trigger  string `json:"trigger"`
}
