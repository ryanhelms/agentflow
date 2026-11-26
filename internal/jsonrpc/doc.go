// Package jsonrpc is agentflow's shared JSON-RPC 2.0 over stdio: newline
// framing, a bidirectional connection, and a launcher that starts a child
// process with a scrubbed environment (ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md, section
// "Phase 5 JSON-RPC framing and launcher").
//
// The plugin host (phase 5), the harness probe (phase 6) and the ACP
// client (phase 7) use it. internal/provider/codexhost keeps its own
// framing and does not import this package.
//
// types.go is the contract: wire types, error codes, limits, the Conn and
// Handler interfaces, the launch specification and the environment
// allowlist. Changing it needs an ADR amendment. Lane 5.1 implements the
// rest.
package jsonrpc
