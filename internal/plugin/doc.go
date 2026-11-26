// Package plugin is agentflow's plugin host: it runs installed plugins of
// three tiers (declarative YAML, out-of-process JSON-RPC, builtin), feeds
// them events from the bus, lets them invoke commands through the Invoker
// and the policy gate, and isolates their failures into the Dead Letter
// Channel (ADR docs/knowledge/engineering/decisions/event-driven-platform.md,
// section "Phase 5").
//
// Plugins are untrusted code. The host gives them only what their granted
// permissions allow, never a secret, a confirm token or the parent's
// environment, and never lets a permission rule loosen policy.
//
// types.go and rpc.go are the contract. Changing them needs an ADR
// amendment. Lane 5.2 implements the rest.
package plugin
