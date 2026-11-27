package harness

import (
	"context"
	"time"

	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// State is a harness's detection state ("Phase 6 detection states").
type State string

const (
	StateNative           State = "native"            // found; the product's own CLI speaks ACP
	StateAdapterInstalled State = "adapter_installed" // found; an ACP adapter for a CLI
	StateAdapterAvailable State = "adapter_available" // adapter missing; its base CLI found
	StateNeedsAuth        State = "needs_auth"        // found; the harness said it needs sign-in
	StateIncompatible     State = "incompatible"      // found; it does not speak ACP v1, or is too old
	StateNotInstalled     State = "not_installed"     // nothing found; listed only with --all
)

// State codes (Harness.Code), from the state table.
const (
	CodeBaseOnly        = "base_only"        // adapter_available: only the base CLI was found
	CodeMissing         = "missing"          // not_installed
	CodeProtocolVersion = "protocol_version" // incompatible: initialize answered a version other than 1
	CodeProtocol        = "protocol"         // incompatible: an initialize error, or not JSON-RPC
	CodeVersionTooOld   = "version_too_old"  // incompatible: below min_version
	CodeVersionUnknown  = "version_unknown"  // min_version is set but no version was read
	CodeSignedOut       = "signed_out"       // needs_auth: the sign-in check said signed out
	CodeAuthRequired    = "auth_required"    // needs_auth: initialize or a session answered -32000
)

// Probe statuses (Probe.Status, ProbeRecord.Status).
const (
	ProbeNone    = "none"
	ProbeOK      = "ok"
	ProbeFailed  = "failed"
	ProbeRefused = "refused"
	ProbeCached  = "cached"
)

// Probe codes (Probe.Code, ProbeRecord.Code). A failed probe can also have
// CodeProtocol or CodeProtocolVersion.
const (
	CodeNotProbed   = "not_probed"
	CodeOK          = "ok"
	CodeTimeout     = "timeout"
	CodeExited      = "exited"
	CodeLaunch      = "launch"
	CodeNeedsPerson = "needs_person"
	CodeShim        = "shim"
	CodeTestGuard   = "test_guard"
	// CodePluginLaunch: a plugin chose what is started, so only a person's
	// own probe runs it (never the daemon or an agent).
	CodePluginLaunch = "plugin_launch"
)

// Version codes (Harness.VersionCode).
const (
	VersionCodeNone = "no_version" // the version command timed out or printed no version
)

// Sign-in verdicts (SignInStatus.Status).
const (
	SignedIn      = "signed_in"
	SignedOut     = "signed_out"
	SignInUnknown = "unknown"
)

// Sign-in codes (SignInStatus.Code). signed_in has CodeOK and signed_out
// has CodeSignedOut; the rest go with SignInUnknown. CodeTimeout,
// CodeLaunch, CodeNeedsPerson and CodeTestGuard are shared with probes.
const (
	CodeNoCheck           = "no_check"           // no verified status command
	CodeEnvCredential     = "env_credential"     //nolint:gosec // G101: a code, not a credential. signed_out, but a credential variable is set
	CodeExitCode          = "exit_code"          // an exit code the definition does not map
	CodeOff               = "off"                // sign_in_check is off
	CodeBaseMissing       = "base_missing"       // status_from base, but no base CLI was found
	CodeUnsafeEnvironment = "unsafe_environment" // a home or config directory is inside a repository
)

// Reasons a PATH entry or found file is skipped (SkippedDir.Reason).
const (
	SkipRelative          = "relative"
	SkipMissing           = "missing" // never reported
	SkipInsideRepository  = "inside_repository"
	SkipNodeModules       = "node_modules"
	SkipUnsafePermissions = "unsafe_permissions"
	SkipShim              = "shim"
	SkipTestGuard         = "test_guard"
)

// Problem codes beyond harnessdef's (Problem.Code).
const (
	ProblemRegistry   = "registry"    // a registry value failed the rules and was dropped
	ProblemHomeUnsafe = "home_unsafe" // personal overrides and plugin definitions were ignored
)

// Notes (Harness.Notes).
const (
	NoteRegistryEnvIgnored = "registry_env_ignored"
	NoteQuarantined        = "quarantined"
	// NotePluginLaunch: a plugin chose the command or its arguments, so
	// agentflow starts it only when a person probes it.
	NotePluginLaunch = "plugin_launch"
)

// Requesters (DetectOptions.Requester; see "Phase 6 who may probe").
const (
	RequesterPerson = "person"
	RequesterAgent  = "agent"
	RequesterDaemon = "daemon"
)

// Sources (Harness.Sources): the layers that shaped a definition.
const (
	SourceRegistry     = "registry"
	SourceAgentflow    = "agentflow"
	SourcePersonal     = "personal"
	SourcePluginPrefix = "plugin:" // followed by the plugin ID
)

// Limits ("Phase 6 harness launch profile", "Phase 6 sign-in check", the
// cache and the sanitizer).
const (
	DefaultVersionTimeout = 3 * time.Second
	DefaultVersionStdout  = 4 << 10
	DefaultProbeTimeout   = 5 * time.Second
	MinProbeTimeout       = 1 * time.Second
	MaxProbeTimeout       = 30 * time.Second
	DefaultStopGrace      = 2 * time.Second
	DefaultMaxProbes      = 2 // probes at once, per process
	DefaultProbeCooldown  = 30 * time.Second
	DefaultProbeStderr    = 64 << 10
	DefaultProbeLogRotate = 1 << 20

	DefaultStatusTimeout  = 5 * time.Second
	DefaultStatusOutput   = 4 << 10         // each of stdout and stderr
	DefaultStatusInterval = 5 * time.Minute // the same identity, unless a person asks

	DefaultPollInterval = 30 * time.Second
	MinPollInterval     = 10 * time.Second
	MaxPollInterval     = 10 * time.Minute

	DefaultRecordTTL   = 24 * time.Hour // a probe record, and a sign-in verdict
	DefaultMaxRecords  = 256
	DefaultMaxCacheLen = 256 << 10

	// Sanitized text: registry names and descriptions, and agent-supplied
	// IDs, names, descriptions and sign-in methods.
	MaxIDLen          = 64
	MaxNameLen        = 64
	MaxDescriptionLen = 256
	MaxSignInMethods  = 8
	MaxSignInArgs     = 8
	MaxSignInEnvValue = 128
)

// Fix is what the person can do about a state. Command is one line to
// copy; URL is https or empty. The templates are agentflow's own
// ("Phase 6 fix text").
type Fix struct {
	Text    string `json:"text"`
	Command string `json:"command,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Harness is one detected (or, with All, known) harness.
type Harness struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Kind        harnessdef.Kind `json:"kind"`
	State       State           `json:"state"`
	Code        string          `json:"code,omitempty"`
	// Sources lists the layers that shaped the definition, lowest first:
	// "registry", "agentflow", "plugin:<id>", "personal".
	Sources     []string `json:"sources"`
	Quarantined bool     `json:"quarantined,omitempty"`
	// QuarantineReason is the registry's reason, clamped and sanitized like
	// a description.
	QuarantineReason string `json:"quarantine_reason,omitempty"`
	// Command, Args: what is (or would be) started. Path: where Command was
	// found; a path under the OS home is shown as "~/...". Never in events.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// VersionArgs are the arguments of the version command a probe runs
	// first ("--version" when the definition sets none); NoVersion means
	// it runs none. Both are set only with Command. Never in events.
	VersionArgs []string `json:"version_args,omitempty"`
	NoVersion   bool     `json:"no_version,omitempty"`
	Path        string   `json:"path,omitempty"`
	// Base is the base CLI's command and where it was found, for adapters.
	Base *Base `json:"base,omitempty"`
	// Version: from the version command. RegistryVersion: the pinned copy's.
	Version         string `json:"version,omitempty"`
	VersionCode     string `json:"version_code,omitempty"` // "", "no_version"
	RegistryVersion string `json:"registry_version,omitempty"`
	Probe           Probe  `json:"probe"`
	// ACP is the sanitized initialize answer for the current identity.
	ACP    *ACPInfo `json:"acp,omitempty"`
	SignIn []SignIn `json:"sign_in,omitempty"`
	// Auth is the sign-in check's verdict ("Phase 6 sign-in check").
	Auth  SignInStatus `json:"auth"`
	Fix   *Fix         `json:"fix,omitempty"`
	Notes []string     `json:"notes,omitempty"` // codes: registry_env_ignored, quarantined
}

// SignInStatus is the sign-in check's verdict. It holds no output.
type SignInStatus struct {
	Status    string     `json:"status"` // signed_in, signed_out, unknown
	Code      string     `json:"code"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

type Base struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Path    string `json:"path,omitempty"`
}

type Probe struct {
	Status   string     `json:"status"` // none, ok, failed, refused, cached
	Code     string     `json:"code"`
	At       *time.Time `json:"probed_at,omitempty"`
	Duration int64      `json:"duration_ms,omitempty"`
}

type ACPInfo struct {
	ProtocolVersion int    `json:"protocol_version"`
	AgentName       string `json:"agent_name,omitempty"`
	AgentTitle      string `json:"agent_title,omitempty"`
	AgentVersion    string `json:"agent_version,omitempty"`
	LoadSession     bool   `json:"load_session"`
	Image           bool   `json:"image"`
	Audio           bool   `json:"audio"`
	EmbeddedContext bool   `json:"embedded_context"`
	MCPHTTP         bool   `json:"mcp_http"`
	MCPSSE          bool   `json:"mcp_sse"`
	Logout          bool   `json:"logout"`
}

type SignIn struct {
	Method      string `json:"method"`
	Type        string `json:"type"` // agent, terminal
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command,omitempty"`
	Unsafe      bool   `json:"unsafe,omitempty"`
}

// Report is one detection pass.
type Report struct {
	Registry    RegistryInfo `json:"registry"` // commit, committed_at, agents
	AutoProbe   bool         `json:"auto_probe"`
	Harnesses   []Harness    `json:"harnesses"` // sorted by state group (ready, attention, not installed), then ID
	SkippedPath []SkippedDir `json:"skipped_path,omitempty"`
	Problems    []Problem    `json:"problems,omitempty"`
	// ProbeRefused is set when a probe was asked for and not allowed.
	ProbeRefused string    `json:"probe_refused,omitempty"` // needs_person
	CheckedAt    time.Time `json:"checked_at"`
}

type RegistryInfo struct {
	Commit      string    `json:"commit"`
	CommittedAt time.Time `json:"committed_at"`
	Agents      int       `json:"agents"`
}

type SkippedDir struct {
	Dir    string `json:"dir"` // shown with "~"
	Reason string `json:"reason"`
}

type Problem struct {
	Layer   string `json:"layer"` // registry, agentflow, plugin:<id>, personal
	ID      string `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Found is what the finder saw for one definition.
type Found struct {
	ID         string
	Definition harnessdef.Definition
	Sources    []string
	Path       string // absolute, as found on PATH
	Target     string // resolved
	PathDir    string // the PATH entry
	Identity   string // "sha256:<hex>" of the identity fields
	// BaseFound is the adapter's base CLI as found on PATH, by the same
	// rules as the main file: Path absolute, PathDir its PATH entry, Target
	// resolved, Identity IdentityOf(Path, StatusArgs, nil). Its ID is the
	// harness's; its Definition holds only Name (the base name) and Command
	// (the base command that matched). nil when no base command was found.
	BaseFound *Found
}

// ProbeRecord is the stored result for one identity.
type ProbeRecord struct {
	ID             string     `json:"id"`
	Identity       string     `json:"identity"`
	Version        string     `json:"version,omitempty"`
	VersionCode    string     `json:"version_code,omitempty"`
	Status         string     `json:"status"`
	Code           string     `json:"code"`
	ACP            *ACPInfo   `json:"acp,omitempty"`
	SignIn         []SignIn   `json:"sign_in,omitempty"`
	ProbedAt       time.Time  `json:"probed_at"`
	DurationMS     int64      `json:"duration_ms"`
	AuthRequiredAt *time.Time `json:"auth_required_at,omitempty"`
	// Auth is the last sign-in verdict for this identity, with its time;
	// never any output. BaseIdentity is the base CLI's identity
	// (Found.BaseFound.Identity) when that verdict was taken, "" without a
	// base: a later pass checks again when it changes.
	Auth         *SignInStatus `json:"auth,omitempty"`
	BaseIdentity string        `json:"base_identity,omitempty"`
}

// Store is the probe cache (lane 6.2: cache.go).
type Store interface {
	Get(id, identity string) (ProbeRecord, bool, error)
	Put(r ProbeRecord) error
	MarkAuthRequired(id, identity string, at time.Time) error
	// Published is the last state the daemon published per ID.
	Published() (map[string]PublishedState, error)
	SetPublished(map[string]PublishedState) error
}

type PublishedState struct {
	State   State  `json:"state"`
	Code    string `json:"code,omitempty"`
	Version string `json:"version,omitempty"`
	// ProbeStatus is Harness.Probe.Status, so a probe-status change is seen
	// after a daemon restart.
	ProbeStatus string `json:"probe_status,omitempty"`
}

// Prober runs one initialize probe (lane 6.2: probe.go). The version
// command is the Detector's own (lane 6.1: version.go).
type Prober interface {
	Probe(ctx context.Context, f Found, timeout time.Duration) ProbeRecord
}

// DetectOptions select a pass.
type DetectOptions struct {
	All   bool     // include not_installed
	IDs   []string // only these
	Probe bool     // run probes now (subject to "who may probe")
	// Requester is "person" or "agent" (see "who may probe"); the daemon
	// uses "daemon".
	Requester string
}

// Detector is the one entry point CLI, MCP and daemon use (lane 6.1:
// detect.go).
type Detector interface {
	Detect(ctx context.Context, o DetectOptions) (Report, error)
}
