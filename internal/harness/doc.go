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
// needs an ADR amendment. So far this package implements only the test
// guard (guard.go, ErrTestGuard). The registry, PATH finder, version
// command, state table, fix text and Detector come from lane 6.1's later
// work; the Prober, the Store (cache.json) and the sign-in check from lane
// 6.2. Until then nothing in this package detects or starts a harness.
//
// Tests that reach detection set their PATH with harnesstest.PATH.
package harness
