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
// Phase 7 (lane 7.1, ADR sections "Phase 7 ACP v1 session types", "Phase 7
// ACP client" and "Phase 7 fake ACP agent") adds ACP sessions:
// session_types.go is their contract, and changing it needs an ADR
// amendment too. agentflow opens a session (session/new), may set its mode
// (session/set_mode), sends one prompt turn (session/prompt) and may cancel
// it (session/cancel). While the turn runs, NewClientHandler decodes every
// agent request by the same decode rules and hands session/update,
// session/request_permission, fs/* and terminal/* to a ClientHandler (the
// session host of lane 7.2): a Proxy over jsonrpc.Conn with Request-Reply,
// and a Message Translator for session/update. Every other agent method
// gets -32601. A session connection uses SessionLimits, whose
// OrderedResponses guarantees that a returned Prompt has seen every update
// the agent sent before its answer.
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
