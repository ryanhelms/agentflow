// Package manifest describes an agentflow plugin: its manifest file, the
// registry of installed plugins, the content hash, and the settings fields
// a plugin adds to the Settings catalog (ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md, section
// "Phase 5").
//
// It imports only internal/config and internal/bus, so internal/consolecfg
// can build the Settings catalog from it without importing the plugin host.
//
// types.go is the contract. Changing it needs an ADR amendment. Lane 5.3
// implements the rest: Parse, Validate, HashDir, the file registry
// (FileSource) and CatalogFields.
package manifest
