// Package harness detects the ACP harnesses installed on this machine: it
// layers their definitions, finds them on PATH, reads their versions,
// probes them with one ACP initialize request, checks sign-in where
// agentflow has a verified status command, and reports each one with its
// state and the exact fix (ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md, section
// "Phase 6").
//
// Phase 6 never installs anything and never starts a session. It starts
// only a binary the person put on their own PATH or named in their own
// settings, through internal/jsonrpc with a scrubbed environment and a
// timeout.
//
// types.go is the contract: states, codes, limits, Harness, Report, Found,
// ProbeRecord and the Store, Prober and Detector interfaces. Changing it
// needs an ADR amendment. Lane 6.1 implements the definition layers
// (acpregistry, overlay.yml, registry.go), the PATH finder (find.go), the
// version command and probe launch profile (version.go), the state table
// (state.go), fix text (fix.go) and the Detector (detect.go: New,
// NewForWorkDir, Production). Lane 6.2 implements the Prober (probe.go),
// the Store (cache.go) and the SignInChecker (signin.go). Production wires
// all three over one agentflow home; the CLI (internal/app) and the console
// daemon (internal/webui) both build their Detector with it.
//
// Tests that reach detection set their PATH with harnesstest.PATH.
package harness
