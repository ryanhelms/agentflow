// Package install adds a plugin to an agentflow home from a local path and
// removes it again (ADR docs/knowledge/engineering/decisions/event-driven-platform.md,
// section "Phase 5 install").
//
// It reads a local directory or a local YAML file and nothing else. It never
// fetches anything, never runs git or a package manager, and never runs the
// plugin. PlanAdd reads the tree once into memory, so the bytes that are
// hashed, shown in the confirmation and written are the same bytes. Add writes
// them under <home>/plugins/<id>/<hash prefix>/ with read-only modes, hashes
// the copy again, and records the plugin in the registry. A new plugin is not
// enabled: enabling is a personal setting.
//
// The package does no confirmation. internal/app gates PlanAdd's Summary and
// PlanRemove's Summary through the policy gate, then calls Add or Remove.
package install
