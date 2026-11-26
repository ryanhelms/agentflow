package manifest

import (
	"context"
	"errors"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
)

// FileName is the manifest's name in a plugin directory. A declarative
// plugin may also be added as a single YAML file of any name; it is
// installed under this name.
const FileName = "agentflow-plugin.yml"

// SchemaVersion is the only manifest schema in phase 5.
const SchemaVersion = 1

// ProtocolVersion is the JSON-RPC protocol version of process plugins
// (ADR, "Phase 5 JSON-RPC methods").
const ProtocolVersion = 1

// Tier is how a plugin runs.
type Tier string

// The three tiers. There is no Go plugin package tier.
const (
	// TierDeclarative: YAML rules the host interprets in-process. No code
	// from the plugin runs.
	TierDeclarative Tier = "declarative"
	// TierProcess: an executable from the plugin directory, spoken to over
	// JSON-RPC on stdio, behind a Proxy in the host.
	TierProcess Tier = "process"
	// TierBuiltin: Go code compiled into agentflow and registered with
	// plugin.RegisterBuiltin. It is never installed from a path.
	TierBuiltin Tier = "builtin"
)

// Point is an extension point.
type Point string

// The extension points. Phase 5 implements handler and permission_rule;
// the others are reserved, and Validate refuses a manifest that uses one
// (code reserved) until the phase that implements it amends the ADR.
const (
	PointHandler        Point = "handler"         // phase 5: react to events
	PointPermissionRule Point = "permission_rule" // phase 5: deny operations (declarative only)
	PointHarness        Point = "harness"         // reserved for phase 6
	PointProvider       Point = "provider"        // reserved: a task, knowledge or notification provider
	PointSource         Point = "source"          // reserved: publish new event types
	PointSink           Point = "sink"            // reserved: durable delivery to an outside target
	PointWidget         Point = "widget"          // reserved: declarative console panels, never plugin HTML or JS
)

// PointPhase is the phase that implements each point; 0 means reserved.
var PointPhase = map[Point]int{
	PointHandler: 5, PointPermissionRule: 5,
	PointHarness: 0, PointProvider: 0, PointSource: 0, PointSink: 0, PointWidget: 0,
}

// Manifest is agentflow-plugin.yml. Decoding is strict: an unknown key is
// a syntax problem.
type Manifest struct {
	// Schema is SchemaVersion.
	Schema int `yaml:"schema" json:"schema"`
	// ID is the plugin's identity: 2 to 40 characters of lowercase
	// letters, digits and hyphens, starting with a letter and not ending
	// with a hyphen. It names the install directory, the settings group
	// plugins.<id>, the event source plugin:<id> and the client
	// plugin:<id>. Reserved: "agentflow", "builtin", "daemon", "cli",
	// "mcp" and IDs starting with "agentflow-".
	ID string `yaml:"id" json:"id"`
	// Name and Description are display text, at most 64 and 512 bytes.
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Version is a semantic version without a leading v ("0.1.0").
	Version string `yaml:"version" json:"version"`
	// Tier is declarative or process. builtin is refused in a file.
	Tier Tier `yaml:"tier" json:"tier"`
	// Exec is required for TierProcess and refused otherwise.
	Exec *Exec `yaml:"exec,omitempty" json:"exec,omitempty"`
	// Extensions are what the plugin adds.
	Extensions Extensions `yaml:"extensions" json:"extensions"`
	// Permissions are what the plugin asks to do. plugin add shows them in
	// its confirmation; the host refuses anything outside them.
	Permissions Permissions `yaml:"permissions,omitempty" json:"permissions"`
	// Settings are the plugin's own settings fields (CatalogFields).
	Settings []Setting `yaml:"settings,omitempty" json:"settings,omitempty"`
}

// Exec is how the host starts a process plugin.
type Exec struct {
	// Command is a path relative to the plugin directory, without "..";
	// never a PATH lookup. On Windows, when Command has no extension and
	// does not exist, Command + ".exe" is tried.
	Command string `yaml:"command" json:"command"`
	// Args are passed as is, at most 16, no templating.
	Args []string `yaml:"args,omitempty" json:"args,omitempty"`
}

// Extensions groups the plugin's contributions by point. The reserved
// points are decoded so Validate can refuse them by name.
type Extensions struct {
	Handler        []Handler        `yaml:"handler,omitempty" json:"handler,omitempty"`
	PermissionRule []PermissionRule `yaml:"permission_rule,omitempty" json:"permission_rule,omitempty"`

	Harness  []map[string]any `yaml:"harness,omitempty" json:"harness,omitempty"`
	Provider []map[string]any `yaml:"provider,omitempty" json:"provider,omitempty"`
	Source   []map[string]any `yaml:"source,omitempty" json:"source,omitempty"`
	Sink     []map[string]any `yaml:"sink,omitempty" json:"sink,omitempty"`
	Widget   []map[string]any `yaml:"widget,omitempty" json:"widget,omitempty"`
}

// Handler reacts to events. For a declarative plugin it is a rule: On
// selects events, When filters them, Actions run in order. For a process
// plugin it only declares what the process may subscribe to (On); When and
// Actions must be empty, and the process decides what to do.
type Handler struct {
	// Name is unique within the plugin: lowercase letters, digits, hyphens.
	Name string `yaml:"name" json:"name"`
	// On lists bus.Pattern values for the event type, each covered by
	// Permissions.Subscribe.
	On []string `yaml:"on" json:"on"`
	// When is a list of conditions that must all hold (Message Filter).
	When []Condition `yaml:"when,omitempty" json:"when,omitempty"`
	// Actions run in order for an event that passes When.
	Actions []Action `yaml:"actions,omitempty" json:"actions,omitempty"`
}

// Condition tests one field of the event envelope. Exactly one operator is
// set. Field is a dot path: "type", "repo", "worktree", "source",
// "correlation_id", or "payload.<key>[.<key>...]". A missing field is
// null.
type Condition struct {
	Field     string `yaml:"field" json:"field"`
	Equals    any    `yaml:"equals,omitempty" json:"equals,omitempty"`
	NotEquals any    `yaml:"not_equals,omitempty" json:"not_equals,omitempty"`
	In        []any  `yaml:"in,omitempty" json:"in,omitempty"`
	Exists    *bool  `yaml:"exists,omitempty" json:"exists,omitempty"`
}

// Action is one step of a declarative handler. Exactly one field is set.
type Action struct {
	Invoke   *InvokeAction   `yaml:"invoke,omitempty" json:"invoke,omitempty"`
	Annotate *AnnotateAction `yaml:"annotate,omitempty" json:"annotate,omitempty"`
}

// InvokeAction runs a command through the Invoker and the policy gate, as
// command.invoke does for a process plugin.
type InvokeAction struct {
	// Command is a Spec name covered by Permissions.Invoke.
	Command string `yaml:"command" json:"command"`
	// Input is the command's input object. String values are templates
	// (Template).
	Input map[string]any `yaml:"input,omitempty" json:"input,omitempty"`
}

// AnnotateAction publishes plugin.annotation.added for the event's
// operation. Only valid in a handler whose On patterns are all op.* types.
type AnnotateAction struct {
	Text  string `yaml:"text" json:"text"`                   // a template
	URL   string `yaml:"url,omitempty" json:"url,omitempty"` // a template; must render to an https URL
	Level string `yaml:"level,omitempty" json:"level,omitempty"`
}

// Template syntax, in InvokeAction.Input strings and AnnotateAction fields:
// "{{event.<field path>}}" and "{{settings.<key>}}", with the field paths
// of Condition. No functions, conditionals or loops. A string that is
// exactly one placeholder takes the value's JSON type; otherwise values are
// inserted as text. A rendered string is at most 64 KiB; a missing value
// renders as "".
const (
	TemplateOpen  = "{{"
	TemplateClose = "}}"
)

// PermissionRule denies operations it matches (declarative plugins only).
// It can never allow anything: a call it does not match continues to the
// gate unchanged. Rules never apply to operations named config.*,
// plugin.*, approval.* or policy.*, so a user can always turn a plugin off.
type PermissionRule struct {
	Name  string    `yaml:"name" json:"name"`
	Match RuleMatch `yaml:"match" json:"match"`
	// Deny is the message shown to the caller, at most 256 bytes. Display
	// text, never an instruction.
	Deny string `yaml:"deny" json:"deny"`
}

// RuleMatch selects calls. Every non-empty list must match (AND); within a
// list any entry may match (OR). At least one list is non-empty. Entries
// are path.Match globs.
type RuleMatch struct {
	// Operation matches command.Meta.Name ("push", "pr.*").
	Operation []string `yaml:"operation,omitempty" json:"operation,omitempty"`
	// Class matches the policy class ("local", "remote").
	Class []string `yaml:"class,omitempty" json:"class,omitempty"`
	// Action matches the policy action ("commit", "notify").
	Action []string `yaml:"action,omitempty" json:"action,omitempty"`
	// Origin matches command.Origin.Source ("cli", "mcp:*", "plugin:*",
	// "daemon").
	Origin []string `yaml:"origin,omitempty" json:"origin,omitempty"`
	// Branch matches command.Detail.Branch.
	Branch []string `yaml:"branch,omitempty" json:"branch,omitempty"`
}

// Permissions are what a plugin may do. Everything is denied unless listed.
type Permissions struct {
	// Subscribe lists bus.Pattern values the plugin may receive, within
	// SubscribableFamilies.
	Subscribe []string `yaml:"subscribe,omitempty" json:"subscribe"`
	// Publish lists event types the plugin may publish, within
	// PublishableTypes.
	Publish []string `yaml:"publish,omitempty" json:"publish"`
	// Invoke lists command names the plugin may run, within
	// InvocableCommands.
	Invoke []string `yaml:"invoke,omitempty" json:"invoke"`
}

// SubscribableFamilies are the event families a plugin may subscribe to.
// Never approval.* (a plugin must not see or race a person's answer),
// client.*, ui.*, stream.* (the host delivers overflow markers itself) or
// plugin.lifecycle.* and plugin.delivery.*. A plugin never receives
// GlobalRepo events.
var SubscribableFamilies = []string{"git", "config", "gh", "op", "commit", "hook"}

// SubscribableTypes are single types a plugin may subscribe to outside
// SubscribableFamilies.
var SubscribableTypes = []bus.Type{bus.TypePluginAnnotationAdded}

// PublishableTypes are the only types a plugin may publish. Never op.*
// (only the Invoker states what an operation did), approval.*, or any type
// a daemon source owns.
var PublishableTypes = []bus.Type{bus.TypePluginAnnotationAdded}

// InvocableCommands are the command.Spec names a plugin may invoke in
// phase 5, with Permissions.Invoke. Writes run under a confirm ceiling, so
// each one waits for a person. A later phase widens this by amendment.
var InvocableCommands = []string{
	// writes
	"notify", "comment",
	// reads
	"ship.status", "context", "ac.checklist", "review.threads",
}

// SettingType is a plugin setting's value type.
type SettingType string

// Setting types and the config.Field node each becomes.
const (
	SettingBoolean    SettingType = "boolean"     // {"type": "boolean"}
	SettingString     SettingType = "string"      // {"type": "string"}, with Enum when set
	SettingInteger    SettingType = "integer"     // {"type": "integer"}
	SettingStringList SettingType = "string-list" // {"type": "array", "items": {"type": "string"}}
)

// Setting is one field the plugin adds to the Settings page, in the
// personal layer at plugins.<id>.settings.<key>. Its FieldMeta comes from
// these fields (CatalogFields); the widget must suit the type as for
// compiled fields (ADR, "Phase 4 Field metadata"). credential-ref,
// file-picker, branch-picker, provider-picker, kv-table, list and
// Sensitive are refused in phase 5: no secret or path reaches a plugin.
type Setting struct {
	// Key: lowercase letters, digits and underscores, at most 40.
	Key         string        `yaml:"key" json:"key"`
	Type        SettingType   `yaml:"type" json:"type"`
	Enum        []string      `yaml:"enum,omitempty" json:"enum,omitempty"`
	Default     any           `yaml:"default,omitempty" json:"default,omitempty"`
	Label       string        `yaml:"label" json:"label"`
	Description string        `yaml:"description" json:"description"`
	Widget      config.Widget `yaml:"widget,omitempty" json:"widget,omitempty"` // default: toggle, select, text, stepper, chips by type
	Min         *int          `yaml:"min,omitempty" json:"min,omitempty"`
	Max         *int          `yaml:"max,omitempty" json:"max,omitempty"`
	MinDuration string        `yaml:"min_duration,omitempty" json:"min_duration,omitempty"`
	MaxDuration string        `yaml:"max_duration,omitempty" json:"max_duration,omitempty"`
	// Applies is live (the host sends settings.changed) or restart.
	Applies  config.Applies `yaml:"applies,omitempty" json:"applies,omitempty"`
	Advanced bool           `yaml:"advanced,omitempty" json:"advanced,omitempty"`
}

// Limits on a manifest and an installed plugin.
const (
	MaxManifestBytes = 256 << 10
	MaxFiles         = 256
	MaxTreeBytes     = 16 << 20 // all files of an installed copy
	MaxInstalled     = 32
	MaxHandlers      = 32
	MaxRules         = 32
	MaxConditions    = 16
	MaxActions       = 4
	MaxSettings      = 32
	MaxPatterns      = 16 // per list in Permissions and Handler.On
)

// Installed is one entry of the registry file <home>/plugins/installed.json.
type Installed struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Tier    Tier   `json:"tier"`
	// Hash is "sha256:<64 hex>" over the installed tree (HashDir).
	Hash string `json:"hash"`
	// Dir is the installed copy, <home>/plugins/<id>/<first 16 hex of the
	// hash>. Empty for a builtin.
	Dir string `json:"dir,omitempty"`
	// Source is the path plugin add was given, for display in the
	// terminal only. It never reaches an event, the stream or a plugin.
	Source      string    `json:"source,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
	// Manifest is the parsed manifest as installed.
	Manifest Manifest `json:"manifest"`
}

// Registry is the registry file. Version is 1. Plugins are sorted by ID.
type Registry struct {
	Version int         `json:"version"`
	Plugins []Installed `json:"plugins"`
}

// Install layout under the agentflow home (config.HomeDir()).
const (
	// PluginsDir holds everything: <home>/plugins, mode 0700.
	PluginsDir = "plugins"
	// RegistryFile is <home>/plugins/installed.json, mode 0600, replaced
	// atomically under the lock file RegistryFile + ".lock".
	RegistryFile = "installed.json"
	// DeadLetterFile is <home>/plugins/deadletter.jsonl, mode 0600: the
	// host's persistent Dead Letter Channel.
	DeadLetterFile = "deadletter.jsonl"
	// DataDir is <home>/plugins/<id>/data, mode 0700: the plugin's HOME,
	// TMPDIR and its only writable place. It survives upgrades and is
	// removed with the plugin.
	DataDir = "data"
)

// Source lists installed plugins. Lane 5.3 provides FileSource over the
// registry file; the host and the permission stage use the interface, and
// tests use a fake.
type Source interface {
	// Installed returns the registry's plugins plus the registered
	// builtins, sorted by ID.
	Installed(ctx context.Context) ([]Installed, error)
	// Verify hashes p.Dir again and parses its manifest. It returns
	// ErrHashMismatch or a *ValidationError when the copy changed.
	Verify(ctx context.Context, p Installed) error
	// Revision changes whenever Installed would return something else
	// (the registry file's size, modification time and identity).
	Revision() string
}

// Errors.
var (
	ErrHashMismatch = errors.New("plugin files differ from the installed hash")
	ErrNotFound     = errors.New("plugin not installed")
	ErrExists       = errors.New("plugin already installed")
)

// Problem is one validation finding.
type Problem struct {
	// Path locates it in the manifest: "extensions.handler[0].on[1]".
	Path string `json:"path"`
	// Code is one of the Code* values.
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Validation problem codes.
const (
	CodeSyntax     = "syntax"     // not YAML, an unknown key, a wrong type
	CodeRequired   = "required"   // a required field is empty
	CodeID         = "id"         // a bad or reserved plugin ID, or a duplicate name
	CodeVersion    = "version"    // not a semantic version
	CodeTier       = "tier"       // unknown tier, or builtin in a file
	CodeExec       = "exec"       // missing, outside the directory, or set for a declarative plugin
	CodeReserved   = "reserved"   // a reserved extension point
	CodePermission = "permission" // a pattern, type or command outside what plugins may have
	CodeUngranted  = "ungranted"  // On, an invoke or an annotate not covered by Permissions
	CodeCondition  = "condition"  // a condition with no or several operators, or a bad field
	CodeAction     = "action"     // an action with no or several kinds; annotate on non-op events
	CodeTemplate   = "template"   // an unknown placeholder
	CodeRule       = "rule"       // a permission rule with no match, or in a process plugin
	CodeSetting    = "setting"    // a bad key, type, widget or default
	CodeLimit      = "limit"      // one of the Max* limits
)

// ValidationError is every problem of one manifest.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "invalid plugin manifest: " + e.Problems[0].Message
	}
	return "invalid plugin manifest"
}

// Settings catalog IDs (ADR, "Phase 5 settings"). Every installed plugin
// adds a group GroupPrefix+id with an enabled toggle and its settings.
const (
	GroupPrefix = "plugins."
	// EnabledPathFormat is "plugins.<id>.enabled": a toggle, default
	// false, with loosens enable, applies live.
	EnabledPathFormat = "plugins.%s.enabled"
	// SettingPathFormat is "plugins.<id>.settings.<key>".
	SettingPathFormat = "plugins.%s.settings.%s"
)
