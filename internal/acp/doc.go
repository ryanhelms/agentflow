// Package acp is agentflow's client side of the Agent Client Protocol (ACP)
// v1: the types and methods agentflow sends to an ACP agent over a
// jsonrpc.Conn (ADR docs/knowledge/engineering/decisions/event-driven-platform.md,
// section "Phase 6 ACP initialize slice").
//
// Phase 6 (lane 6.2) creates the smallest slice: the initialize request and
// answer, the ACP error codes and Initialize. Phase 7 (lane 7.1) adds the
// session methods and keeps these names, fields and behaviour. Shapes are
// from the ACP v1 schema at schema-v1.35.0 (commit
// 065b57951b6b2a91ac5eddd249e145d1f6d54545).
//
// types.go is the contract. Changing it needs an ADR amendment.
//
// Decoding follows the ACP schema: unknown fields are ignored (ACP is
// extensible), a capability object that fails to decode becomes its zero
// value ("default on error") and an invalid list item is dropped ("skip
// invalid items"). An error from an ACP peer is always read by the ACP
// codes here, never by the jsonrpc.Code* constants, which agentflow sends
// only to its own plugins.
//
// Package acptest holds the fake ACP agent tests use.
package acp
