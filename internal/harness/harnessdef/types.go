// Package harnessdef is the shape of one ACP harness definition: how
// agentflow finds and probes a harness. The same shape is used in every
// definition layer (the pinned ACP registry, the agentflow overlay, plugin
// extensions and the person's overrides), so internal/config,
// internal/plugin/manifest and internal/harness all import this leaf
// package (ADR docs/knowledge/engineering/decisions/event-driven-platform.md,
// section "Phase 6 harness definitions").
//
// types.go is the contract. Changing it needs an ADR amendment. validate.go
// implements Validate and the name and argument rules it uses.
package harnessdef

// Kind says whether a harness speaks ACP itself or through an adapter.
type Kind string

const (
	KindNative  Kind = "native"  // the product's own CLI speaks ACP (gemini --acp, goose acp)
	KindAdapter Kind = "adapter" // a separate program bridges a CLI to ACP (claude-agent-acp)
)

// Definition is how agentflow finds and probes one harness. Empty fields
// inherit from the layer below.
type Definition struct {
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	Kind Kind   `yaml:"kind,omitempty" json:"kind,omitempty"`
	// Command is the executable: a bare name looked up on PATH, or (personal
	// layer only) an absolute path.
	Command string `yaml:"command,omitempty" json:"command,omitempty"`
	// Args start the ACP server on stdio.
	Args []string `yaml:"args,omitempty" json:"args,omitempty"`
	// VersionArgs print the version. Empty means ["--version"]; NoVersion
	// means never run a version command.
	VersionArgs []string `yaml:"version_args,omitempty" json:"version_args,omitempty"`
	NoVersion   bool     `yaml:"no_version,omitempty" json:"no_version,omitempty"`
	// BaseName and BaseCommands name the CLI an adapter bridges. When the
	// adapter is missing and a base command is found, the state is
	// adapter_available.
	BaseName     string   `yaml:"base_name,omitempty" json:"base_name,omitempty"`
	BaseCommands []string `yaml:"base_commands,omitempty" json:"base_commands,omitempty"`
	// MinVersion is the oldest version agentflow accepts (X.Y.Z).
	MinVersion string `yaml:"min_version,omitempty" json:"min_version,omitempty"`

	// Sign-in check ("Phase 6 sign-in check"). Only the agentflow overlay
	// and the personal layer may set these; Validate refuses them in a
	// plugin definition.
	//
	// StatusFrom is "self" (the found command) or "base" (the adapter's
	// found base CLI); empty means no check. StatusArgs are its arguments.
	StatusFrom string   `yaml:"status_from,omitempty" json:"status_from,omitempty"`
	StatusArgs []string `yaml:"status_args,omitempty" json:"status_args,omitempty"`
	// StatusSignedIn and StatusSignedOut are exit codes. Empty means [0]
	// and [1]. StatusSignedOutLine, when set, must also appear as one
	// whole trimmed line of output for a signed-out verdict.
	StatusSignedIn      []int  `yaml:"status_signed_in,omitempty" json:"status_signed_in,omitempty"`
	StatusSignedOut     []int  `yaml:"status_signed_out,omitempty" json:"status_signed_out,omitempty"`
	StatusSignedOutLine string `yaml:"status_signed_out_line,omitempty" json:"status_signed_out_line,omitempty"`
	// CredentialEnv names variables that also sign the harness in; with one
	// set, a signed-out answer is "unknown". ConfigEnv names directory
	// variables passed through to the status command.
	CredentialEnv []string `yaml:"credential_env,omitempty" json:"credential_env,omitempty"`
	ConfigEnv     []string `yaml:"config_env,omitempty" json:"config_env,omitempty"`
	// StatusSource cites the vendor documentation or source (overlay only).
	StatusSource string `yaml:"status_source,omitempty" json:"status_source,omitempty"`
}

// Layer is the definition layer a Definition comes from, lowest first. A
// plugin's Source ("plugin:<id>") carries its ID; its Layer is LayerPlugin.
type Layer string

const (
	LayerRegistry  Layer = "registry"  // the pinned ACP registry copy
	LayerAgentflow Layer = "agentflow" // internal/harness/overlay.yml
	LayerPlugin    Layer = "plugin"    // extensions.harness of an enabled declarative plugin
	LayerPersonal  Layer = "personal"  // personal:ai.harnesses.overrides.<id>
)

// Status sources (Definition.StatusFrom).
const (
	StatusFromSelf = "self"
	StatusFromBase = "base"
)

// Problem codes Validate returns (ValidationError.Code). id_taken is the
// registry's, since Validate does not know the other layers' IDs.
const (
	CodeID               = "id"                 // the ID does not match ^[a-z][a-z0-9-]{0,63}$
	CodeIDTaken          = "id_taken"           // a plugin ID equals a registry or overlay ID
	CodeRule             = "rule"               // unknown layer or kind, or a field the layer may not set
	CodeName             = "name"               // name or base_name is not one line of at most 64 bytes
	CodeCommand          = "command"            // command or a base command is not a bare name (or a personal absolute path)
	CodeLauncher         = "launcher"           // command or a base command is a refused launcher
	CodeArgs             = "args"               // args, version_args or base_commands break the arg rule
	CodeVersion          = "version"            // min_version is not X.Y.Z
	CodeStatus           = "status"             // a sign-in check field is invalid
	CodeStatusNotAllowed = "status_not_allowed" // a sign-in check field in the registry or a plugin
)

// Limits of the definition rules.
const (
	MaxIDLen          = 64
	MaxNameLen        = 64
	MaxCommandLen     = 64
	MaxArgs           = 16
	MaxArgLen         = 128
	MaxSignedOutLine  = 128
	MaxEnvNameLen     = 64
	MaxStatusExitCode = 255
)
