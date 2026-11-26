// Package command is agentflow's command registry and Invoker: the code form
// of the ADR "Event bus, message store and event stream", section "Phase 3".
//
// Every operation agentflow offers is one Spec: its name, CLI binding, MCP
// tool, policy class and action, how it describes itself on the timeline, and
// a Run function that calls one internal/app method. A Spec registers itself
// from its own file's init (Register), so a new operation touches only that
// file. The CLI dispatch, the MCP tool list, the usage text and the parity
// test are derived from the registry.
//
// An Invocation is a Command Message: an imperative request addressed to one
// handler, which may refuse it. It is never stored or broadcast. The Invoker
// runs it through a fixed chain (validate, gate, run, audit, publish) and
// publishes op.* Event Messages: facts in the past tense, broadcast to every
// subscriber and stored in the repository's message store.
//
// The Invoker returns the value Run returned, unchanged: a *app.Pending, a
// result or an error. So the CLI and MCP output of a migrated operation stays
// byte for byte what it was (the golden tests in internal/cli and
// internal/app pin it).
//
// Dependencies point one way: internal/command imports internal/app,
// internal/bus and internal/policy; internal/cli and internal/mcpserver import
// internal/command. internal/app never imports it. The production Emitter,
// which needs internal/webui to find the worktree's console ID, lives in a
// subpackage, so internal/webui can import this package in later phases.
package command
