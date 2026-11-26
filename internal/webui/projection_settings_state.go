package webui

import "github.com/ryanhelms/agentflow/internal/settings"

// Phase 4 settings projection state (lane 4.2 builds the projection, lane
// 4.3 draws it). The contract is in the ADR, section "Phase 4 settings
// projection". Changing a shape needs an amendment.

// ProjectionSettings is the settings projection's name.
const ProjectionSettings = "settings"

// SettingsState is the "settings" state. Key: the worktree. Topics:
// config.layer.changed of the repository and of global (the personal
// layer), git.head.changed, op.applied and op.failed of config.*
// operations. It is computed from the layer files, the field catalog and
// the undo journal, never from event payloads.
type SettingsState struct {
	// Revisions are the current layer file digests. A client compares them
	// with its draft's ChangeSet.Base to find outside edits.
	Revisions settings.Revisions `json:"revisions"`
	// CatalogRevision changes when the field catalog changes (a provider
	// definition was added, or a new binary). The client then reads the
	// catalog again with config.fields.
	CatalogRevision string `json:"catalog_revision"`
	// Values holds every field that is set in a layer or active, sorted
	// by ID, then provider.
	Values []settings.FieldValue `json:"values"`
	// Problems are the load problems of the current files.
	Problems []settings.Diagnostic `json:"problems"`
	// LastSave is the undo journal of this worktree's scope, or null.
	LastSave *settings.SaveSummary `json:"last_save"`
	// Branch is the worktree's branch; Protected is true when RepoWorktree
	// saves are refused on it.
	Branch    string `json:"branch,omitempty"`
	Protected bool   `json:"protected,omitempty"`
}
