// Package ops registers agentflow's operations in command.Default. Each
// operation family has one file here that registers its Specs from init, so
// a new operation touches only its own file (plus its internal/app method,
// plugin skill and README section): the CLI command, the MCP tool, the
// usage text and the parity check are derived from the registry (ADR "Event
// bus, message store and event stream", section "Phase 3").
//
// internal/cli and internal/mcpserver import this package for its
// registrations. A Spec's Run calls exactly one internal/app method and
// returns its result unchanged; the input type is the MCP tool's input, so
// its json and jsonschema tags are a compatibility contract
// (internal/mcpserver/testdata/golden/tools.json pins them).
package ops
