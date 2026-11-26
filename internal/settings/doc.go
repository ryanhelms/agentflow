// Package settings holds the phase 4 settings contract: the ChangeSet a
// client edits, the Diagnostics of live checks, the Preview, the Save and
// Undo results, the undo journal record and the field values the settings
// projection reports.
//
// The contract is the ADR docs/knowledge/engineering/decisions/
// event-driven-platform.md, section "Phase 4". types.go is its code form and
// holds types only. Lane 4.2 adds the service that checks, previews, saves
// and undoes a ChangeSet, in other files of this package. Changing a type in
// types.go needs an ADR amendment first.
package settings
