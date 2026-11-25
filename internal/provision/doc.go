// Package provision plans and reconciles provider companions in a named environment.
//
// Build a Manifest for each configured provider, then call BuildPlan with the
// destination Environment and a read-only Prober. The environment ID must
// identify the same installation, probe, and credential scope for every call.
// The planner shares a single target across providers that declare the same
// companion ID, launch or endpoint metadata, auth reference, and health probe.
// It intersects stable version ranges and checks platform support. Exact pins
// must agree on platform, version, source, and digest. Conflicts are returned as
// ConflictError values, joined when multiple companions have conflicts.
//
// Use Plan.Steps to preview actions in companion ID order. A present local
// companion that satisfies its version range can be kept. Use PinArtifact to
// require matching installed bytes as well. If installation is needed and
// there is no pin, the planner takes the highest compatible version from the
// shared declarations for the environment's platform. Different artifacts at
// that version are ambiguous. Remote MCP companions get verification steps.
//
// Apply takes the original plan, environment, prober, and installer. It can
// publish only the selected artifacts on install steps. The installer must
// validate declared sources and checksums before publishing and enforce the
// observed installation precondition atomically. Apply checks presence,
// version, measured digest where an artifact is selected, auth readiness where
// configured, and health. A failed check stops the run. After an interruption,
// call BuildPlan again to reconcile from observed state. There is no journal to
// recover, and already published, conforming artifacts are kept.
//
// Guided CLI and MCP setup use the local installer for a user-pinned gh
// executable on native macOS and Linux. The managedgh package loads the pin,
// prefers a working PATH executable and resolves verified cache receipts.
package provision
