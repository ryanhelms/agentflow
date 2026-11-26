package config

// Settings field metadata: the phase 4 contract (ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md, section
// "Phase 4 Field metadata"). These are types only. Lane 4.1 adds Catalog,
// Describe and the metadata table; lane 4.2 adds the repository catalog for
// provider definitions. Changing a type here needs an ADR amendment.

// FieldLayer is a configuration layer a settings field lives in. It has the
// same values as bus.ConfigLayer.
type FieldLayer string

const (
	// FieldPersonal is ~/.agentflow/config.yml (config.User).
	FieldPersonal FieldLayer = "personal"
	// FieldRepo is <worktree>/.agentflow/config.yml (config.Repo and the
	// provider role sections).
	FieldRepo FieldLayer = "repo"
)

// Widget is the control the Settings page draws for a field.
type Widget string

const (
	WidgetToggle         Widget = "toggle"          // boolean
	WidgetSelect         Widget = "select"          // string with Enum
	WidgetStepper        Widget = "stepper"         // integer, with Min, Max, Step
	WidgetDuration       Widget = "duration"        // Go duration string ("5m"), with MinDuration, MaxDuration
	WidgetChips          Widget = "chips"           // array of strings
	WidgetKVTable        Widget = "kv-table"        // map of string to a scalar or a small object (Columns)
	WidgetProviderPicker Widget = "provider-picker" // a role's provider; Picker.Role
	WidgetBranchPicker   Widget = "branch-picker"   // one branch, or a list with Picker.Multiple
	WidgetFilePicker     Widget = "file-picker"     // a path; Picker.Kind, Picker.Base, Picker.Glob
	WidgetCredentialRef  Widget = "credential-ref"  //nolint:gosec // G101: a widget name, not a credential; the field holds an environment variable name
	WidgetText           Widget = "text"            // any other string: a URL, a command, a name
	WidgetList           Widget = "list"            // array of objects; one row per item, Columns per property
)

// Widgets lists every Widget, in the order the ADR defines them.
var Widgets = []Widget{
	WidgetToggle, WidgetSelect, WidgetStepper, WidgetDuration, WidgetChips, WidgetKVTable,
	WidgetProviderPicker, WidgetBranchPicker, WidgetFilePicker, WidgetCredentialRef, WidgetText, WidgetList,
}

// Applies says when a saved change takes effect.
type Applies string

const (
	// AppliesLive: running daemons and MCP servers reload on
	// config.layer.changed (phase 2) and use it at once.
	AppliesLive Applies = "live"
	// AppliesNextRun: read when each command starts; a running command
	// keeps the old value.
	AppliesNextRun Applies = "next-run"
	// AppliesRestart: running MCP sessions keep the old value until they
	// restart (provider selections change the tool list and instructions).
	AppliesRestart Applies = "restart"
	// AppliesNewWorktrees: used for worktrees created after the save.
	AppliesNewWorktrees Applies = "new-worktrees"
)

// Loosening says which change to a field loosens policy. A loosening edit
// always needs an explicit confirmation in the Save review.
type Loosening string

const (
	LoosensNone   Loosening = ""       // no change loosens
	LoosensLevel  Loosening = "level"  // suggest < confirm < auto; a higher level loosens
	LoosensLonger Loosening = "longer" // a longer duration loosens
	LoosensEnable Loosening = "enable" // false to true loosens
	LoosensShrink Loosening = "shrink" // removing a list item or map key loosens
)

// PickerKind is what a file-picker chooses.
type PickerKind string

const (
	PickFile PickerKind = "file"
	PickDir  PickerKind = "dir"
)

// Picker configures the provider, branch and file pickers.
type Picker struct {
	// Role is the provider role of a provider-picker, e.g. "task_management".
	Role string `json:"role,omitempty"`
	// Multiple makes a branch-picker choose a list of branches.
	Multiple bool `json:"multiple,omitempty"`
	// Kind, Base and Glob configure a file-picker. Base is "repo" (paths
	// relative to the worktree root) or "home" (absolute, "~/" allowed).
	Kind PickerKind `json:"kind,omitempty"`
	Base string     `json:"base,omitempty"`
	Glob string     `json:"glob,omitempty"`
}

// FieldMeta describes how one settings field is shown and edited. Text is
// for display; it never contains a value.
type FieldMeta struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	// Group is a Group ID. Order sorts fields within the group.
	Group string `json:"group"`
	Order int    `json:"order"`
	// Widget is the control. It must suit the field's schema type (ADR
	// table "Widget per schema type").
	Widget  Widget  `json:"widget"`
	Applies Applies `json:"applies"`
	// Sensitive values are never sent to the browser, an event, an audit
	// line or a log; previews show only that they changed.
	Sensitive bool      `json:"sensitive,omitempty"`
	Loosens   Loosening `json:"loosens,omitempty"`
	// Advanced fields are hidden until the page shows advanced settings.
	Advanced bool `json:"advanced,omitempty"`
	// Stepper limits.
	Min  *int `json:"min,omitempty"`
	Max  *int `json:"max,omitempty"`
	Step int  `json:"step,omitempty"`
	// Duration limits, as Go duration strings ("1m", "1h").
	MinDuration string  `json:"min_duration,omitempty"`
	MaxDuration string  `json:"max_duration,omitempty"`
	Picker      *Picker `json:"picker,omitempty"`
	Placeholder string  `json:"placeholder,omitempty"`
}

// CatalogField is one settings field: the unit the Settings page draws one
// control for, a ChangeSet edits and the settings projection reports.
type CatalogField struct {
	// ID is "<layer>:<path>" (FieldID). Within a catalog, ID and Provider
	// together are unique.
	ID    string     `json:"id"`
	Layer FieldLayer `json:"layer"`
	// Path is the field's path in its layer file (FieldPath grammar). A "*"
	// segment stands for any map key or list index.
	Path string `json:"path"`
	// Provider is set for a provider's settings field: the field exists
	// only while the role selects this provider.
	Provider string `json:"provider,omitempty"`
	// Schema is the field's schema node, as SchemaFor builds it.
	Schema Field     `json:"schema"`
	Meta   FieldMeta `json:"meta"`
	// Columns describes the properties of a list item or of a kv-table
	// value that is an object, by property name.
	Columns map[string]FieldMeta `json:"columns,omitempty"`
}

// Group is a section of the Settings page.
type Group struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Order int    `json:"order"`
}
