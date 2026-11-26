package settings

import (
	"encoding/json"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
)

// Limits of one ChangeSet.
const (
	MaxEdits      = 200      // edits in one ChangeSet
	MaxValueBytes = 64 << 10 // one Edit's Value or Expect, as JSON
	MaxBytes      = 1 << 20  // the whole request, as JSON
	MaxStateValue = 4 << 10  // a FieldValue larger than this is sent as TooLarge
)

// JournalDir is the undo journal's directory in the agentflow home
// (config.HomeDir()). It holds one file per scope:
// "<policy.ScopeKey(scope)>.json", or "personal.json" outside a repository.
const JournalDir = "settings"

// ChangeSet is an ordered list of field edits across both layers, saved
// together or not at all (Unit of Work).
type ChangeSet struct {
	// Edits apply in order: select a provider before its settings. Two
	// edits of one ID, or of an ID and a path inside it, are an error.
	Edits []Edit `json:"edits"`
	// Base holds the layer revisions the client last saw. Empty means "no
	// check" (CLI). A stale base is not an error by itself: only edits
	// whose Expect no longer holds conflict.
	Base Revisions `json:"base,omitzero"`
	// Repo says how repository-layer edits are saved. Ignored without any.
	Repo RepoTarget `json:"repo,omitzero"`
}

// Op is what an Edit does.
type Op string

const (
	// OpSet writes Value at the field's path in its layer file.
	OpSet Op = "set"
	// OpUnset removes the path from its layer file, so the next layer or
	// the default applies.
	OpUnset Op = "unset"
)

// Edit changes one field in one layer.
type Edit struct {
	// ID is "<layer>:<path>" (config.FieldID). The path is concrete: no
	// wildcard. It names a catalog field, or a map key or list index
	// inside a kv-table, chips or list field.
	ID string `json:"id"`
	Op Op     `json:"op"`
	// Value is the new value as JSON, for OpSet.
	Value json.RawMessage `json:"value,omitempty"`
	// Expect is the layer value the client last saw: absent means no
	// check, JSON null means "was not set". When the current layer value
	// differs, the edit conflicts (optimistic lock per field).
	Expect json.RawMessage `json:"expect,omitempty"`
}

// Revisions are the digests of the layer files: "sha256:<hex>" of the
// bytes, or "absent" for a missing file.
type Revisions struct {
	Personal string `json:"personal,omitempty"`
	Repo     string `json:"repo,omitempty"`
}

// RevisionAbsent is the revision of a missing layer file.
const RevisionAbsent = "absent"

// RepoMode is how repository-layer edits are saved.
type RepoMode string

const (
	// RepoDraft publishes a draft pull request through the existing
	// console publication flow (consolepublish). The default.
	RepoDraft RepoMode = "draft"
	// RepoDirect applies to the default branch through the same flow, as
	// agentflow_config_apply's "direct" mode.
	RepoDirect RepoMode = "direct"
	// RepoWorktree writes the file in the current worktree for the
	// developer to commit with their task. Refused on a protected branch.
	RepoWorktree RepoMode = "worktree"
)

// RepoTarget is the repository half of a save.
type RepoTarget struct {
	Mode RepoMode `json:"mode,omitempty"`
	// Title is the conventional commit and pull request title for draft
	// and direct. Empty takes Preview.Repo.Title.
	Title string `json:"title,omitempty"`
	// TaskRef is an optional task reference for the publication.
	TaskRef string `json:"task_ref,omitempty"`
}

// Severity of a Diagnostic. Only an error blocks a save.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Diagnostic codes. The ADR table "Diagnostic codes" gives each code's
// severity and meaning.
const (
	CodeSyntax     = "syntax"     // the ChangeSet itself: limits, ID grammar, op, duplicate or overlapping edits
	CodeUnknown    = "unknown"    // no catalog field at this ID
	CodeLayer      = "layer"      // the field cannot be set in this layer
	CodeInactive   = "inactive"   // a provider setting of a provider the role does not select (warning)
	CodeType       = "type"       // the value has the wrong JSON type
	CodeEnum       = "enum"       // not one of the allowed values
	CodeRange      = "range"      // outside Min/Max, MinDuration/MaxDuration, or negative
	CodeRequired   = "required"   // a required field is unset
	CodeLocked     = "locked"     // a release-layer constraint forbids the change
	CodeReference  = "reference"  // names a provider, action, branch, file or variable that does not exist
	CodeCredential = "credential" // a secret where only a reference is allowed, or an unset credential variable (warning)
	CodeConfig     = "config"     // the layer file does not load after the edits
	CodeCapped     = "capped"     // a personal level above the repository ceiling (warning)
	CodeConflict   = "conflict"   // Expect no longer holds: the field changed outside this client
	CodeLoosens    = "loosens"    // the edit loosens policy and needs explicit confirmation (info)
	CodeApplies    = "applies"    // takes effect only after a restart or for new worktrees (info)
	CodeProtected  = "protected"  // RepoWorktree on a protected branch
	CodePublished  = "published"  // undo: the repository part is in an open pull request
	CodeUnchanged  = "unchanged"  // the edit sets the value the layer already has (info)
)

// Diagnostic is one finding of a live check.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	// ID is the field the finding is about, or "" for the ChangeSet or a
	// whole file.
	ID string `json:"id,omitempty"`
	// Edit is the index of the edit in ChangeSet.Edits, or -1.
	Edit    int    `json:"edit"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Status of a preview, save or undo.
type Status string

const (
	StatusOK        Status = "ok"        // valid; a save may proceed
	StatusInvalid   Status = "invalid"   // an error Diagnostic other than conflict
	StatusConflict  Status = "conflict"  // a conflict Diagnostic, and no other error
	StatusUnchanged Status = "unchanged" // nothing would change
	StatusSaved     Status = "saved"     // SaveResult: every layer was written or published
	StatusPartial   Status = "partial"   // SaveResult: the repository part was published, the personal write failed
)

// Source is where a field's effective value comes from.
type Source string

const (
	SourceDefault    Source = "default"
	SourcePersonal   Source = "personal"
	SourceRepo       Source = "repo"
	SourceDefinition Source = "definition" // a provider definition's default
)

// Value is a field's value at one moment. Sensitive values carry no JSON:
// Redacted is true and Digest tells whether two values differ.
type Value struct {
	JSON     json.RawMessage `json:"json,omitempty"`
	Set      bool            `json:"set"`
	Redacted bool            `json:"redacted,omitempty"`
	Digest   string          `json:"digest,omitempty"`
}

// FieldChange is one edited field in a Preview.
type FieldChange struct {
	ID       string         `json:"id"`
	Provider string         `json:"provider,omitempty"`
	Edit     int            `json:"edit"`
	Before   Value          `json:"before"`
	After    Value          `json:"after"`
	Applies  config.Applies `json:"applies"`
	Loosens  bool           `json:"loosens,omitempty"`
	Effect   *Effect        `json:"effective,omitempty"`
}

// Effect is a change of a field's effective value, after defaults, the
// other layer and the repository ceiling.
type Effect struct {
	ID           string `json:"id"`
	Before       Value  `json:"before"`
	After        Value  `json:"after"`
	SourceBefore Source `json:"source_before"`
	SourceAfter  Source `json:"source_after"`
}

// FileChange is one layer file a save writes.
type FileChange struct {
	Layer config.FieldLayer `json:"layer"`
	// Path is ".agentflow/config.yml" or "~/.agentflow/config.yml".
	Path   string `json:"path"`
	Before string `json:"before_revision"`
	After  string `json:"after_revision"`
	// Diff is a unified diff of the repository file. Always empty for the
	// personal file: personal YAML never leaves the disk.
	Diff string `json:"diff,omitempty"`
}

// Confirmation is why a save needs an explicit confirmation.
type Confirmation struct {
	ID string `json:"id"`
	// Reason is "loosens" for a loosening edit.
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// RepoPlan is what the repository half of a save would do.
type RepoPlan struct {
	Mode        RepoMode `json:"mode"`
	Title       string   `json:"title,omitempty"` // the given title, or a suggestion
	TaskRef     string   `json:"task_ref,omitempty"`
	Branch      string   `json:"branch,omitempty"`      // RepoWorktree: the worktree's branch
	Destination string   `json:"destination,omitempty"` // draft, direct: the repository and base branch
	CI          string   `json:"ci,omitempty"`          // CI impact, as PublicationPreview.CI
	Warning     string   `json:"warning,omitempty"`
}

// Preview is what saving a ChangeSet would do. It writes nothing.
type Preview struct {
	Status Status `json:"status"`
	// Base is the revisions the preview was computed from. A save binds
	// them: when a layer file changes before the write, the save fails and
	// asks for a fresh preview.
	Base        Revisions      `json:"base"`
	Changes     []FieldChange  `json:"changes"`
	Effects     []Effect       `json:"effects"`
	Files       []FileChange   `json:"files"`
	Diagnostics []Diagnostic   `json:"diagnostics"`
	Confirm     []Confirmation `json:"confirm"`
	Repo        *RepoPlan      `json:"repo,omitempty"`
	// Restart is true when a change applies only after MCP sessions
	// restart.
	Restart bool `json:"restart,omitempty"`
}

// Publication is the pull request or commit a repository save produced.
type Publication struct {
	Mode   RepoMode `json:"mode"`
	URL    string   `json:"url,omitempty"`
	Number int      `json:"number,omitempty"`
	Branch string   `json:"branch,omitempty"`
	// State is consolepublish.Result.State.
	State string `json:"state,omitempty"`
}

// SaveResult is the applied result of config.set and config.undo.
type SaveResult struct {
	Status      Status        `json:"status"`
	Operation   string        `json:"operation_id"`
	Changes     []FieldChange `json:"changes"`
	Revisions   Revisions     `json:"revisions"`
	Publication *Publication  `json:"publication,omitempty"`
	// Error says what failed in a partial save. Text for display.
	Error   string `json:"error,omitempty"`
	Restart bool   `json:"restart,omitempty"`
	// Undo is true when the journal now holds this save, so config.undo
	// can reverse it.
	Undo bool `json:"undo"`
}

// UndoRequest is the input of config.undo.
type UndoRequest struct {
	// SkipConflicts reverts the fields that did not change since the save
	// and leaves the others. Without it a conflict refuses the undo.
	SkipConflicts bool `json:"skip_conflicts,omitempty"`
	// Repo says how an inverse repository change is saved.
	Repo RepoTarget `json:"repo,omitzero"`
}

// JournalEdit is one field a save changed, with the layer values before
// and after it (Memento).
type JournalEdit struct {
	ID     string          `json:"id"`
	Before json.RawMessage `json:"before"` // JSON null: was not set
	After  json.RawMessage `json:"after"`  // JSON null: unset by the save
}

// JournalRecord is the undo journal: the last save of one scope.
type JournalRecord struct {
	Version     int           `json:"version"` // 1
	Operation   string        `json:"operation_id"`
	Time        time.Time     `json:"time"`
	Client      string        `json:"client"`
	Edits       []JournalEdit `json:"edits"`
	Before      Revisions     `json:"before"`
	After       Revisions     `json:"after"`
	Publication *Publication  `json:"publication,omitempty"`
	// UndoOf is the operation ID of the save this record reverses, so a
	// second undo redoes it.
	UndoOf string `json:"undo_of,omitempty"`
}

// SaveSummary is the journal record as the settings projection shows it:
// no values.
type SaveSummary struct {
	Operation   string       `json:"operation_id"`
	Time        time.Time    `json:"time"`
	Client      string       `json:"client"`
	IDs         []string     `json:"ids"`
	Publication *Publication `json:"publication,omitempty"`
	UndoOf      string       `json:"undo_of,omitempty"`
	// Undoable is false while the repository part waits in an open pull
	// request, or when every field changed since.
	Undoable bool `json:"undoable"`
}

// FieldValue is one field's current value in a worktree.
type FieldValue struct {
	ID       string `json:"id"`
	Provider string `json:"provider,omitempty"`
	// Layer is this field's own layer value; Effective the resolved one.
	Layer     Value  `json:"layer"`
	Effective Value  `json:"effective"`
	Source    Source `json:"source"`
	// Active is false for a provider setting of a provider the role does
	// not select.
	Active bool `json:"active"`
	// CappedBy is the repository ceiling's ID when it lowers a personal
	// level.
	CappedBy string `json:"capped_by,omitempty"`
	// TooLarge replaces Layer and Effective JSON above MaxStateValue in
	// the projection; read the field with config.fields.
	TooLarge bool `json:"too_large,omitempty"`
}

// View is the result of config.fields: the catalog with current values.
type View struct {
	CatalogRevision string                `json:"catalog_revision"`
	Revisions       Revisions             `json:"revisions"`
	Groups          []config.Group        `json:"groups"`
	Fields          []config.CatalogField `json:"fields"`
	Values          []FieldValue          `json:"values"`
	Problems        []Diagnostic          `json:"problems"`
}

// FieldsRequest is the input of config.fields.
type FieldsRequest struct {
	Group    string   `json:"group,omitempty"`
	IDs      []string `json:"ids,omitempty"`
	Query    string   `json:"query,omitempty"`
	Modified bool     `json:"modified,omitempty"`
}

// Catalog resolves fields for one worktree: the compiled catalog of lane
// 4.1 (config.Catalog) plus the provider definitions of the repository and
// home. Lane 4.2 implements it; tests use a fake.
type Catalog interface {
	// Revision changes whenever Fields would return something else.
	Revision() string
	Fields() []config.CatalogField
	// Lookup finds the field for a concrete ID under the role's selected
	// provider. A path inside a kv-table, chips or list field returns that
	// field and the remaining keys.
	Lookup(id, provider string) (config.CatalogField, []string, bool)
}
