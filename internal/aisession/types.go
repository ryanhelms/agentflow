package aisession

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/bus"
)

// ID is a session ID: "ais_" and 32 lowercase hex characters.
type ID string

// IDPrefix starts every session ID.
const IDPrefix = "ais_"

// Options is what the session layer needs from the app, command, webui and
// plugin layers ("Phase 7 package dependencies"). Lane 7.3 wires them in
// internal/command/ops/ai_sessions.go (ops.AISessionOptions).
type Options struct {
	Home    string // the agentflow home
	Version string // agentflow's version, for clientInfo
	// Emit delivers one event (command/deliver: the store.Exists check, the
	// socket, the unknown_worktree retry). nil drops events.
	Emit func(ctx context.Context, e bus.Event) error
	// WorktreeID is webui.IdentifyWorktree's ID for dir.
	WorktreeID func(ctx context.Context, dir string) (string, error)
	// PluginDeny runs the registered Invoker stages (command.CheckStages)
	// for a synthetic operation "ai.request" with the request's gate class
	// and action and the session's branch; a non-nil error is a plugin deny.
	PluginDeny func(ctx context.Context, class, action, branch string) error
	// MergeBase is policy.NeverOptions.MergeBase, as the pre-bash hook wires it.
	MergeBase func(cmd string) (string, error)
	// Detach starts the detached host; nil means re-execute os.Executable.
	Detach func(ctx context.Context, id ID) error
	// MCPServer is the server entry for session/new, or nil for none.
	MCPServer func(id ID) *acp.MCPServer
	// WorkDirs lists the repositories the console daemon serves (their
	// common git directories). PATH entries inside any worktree of them, or
	// of the session's own repository, are dropped from the session PATH,
	// and SHELL there is not passed. An error refuses the start
	// (unsafe_environment): the repositories cannot be told. nil means
	// only the session's own repository. (Integration amendment, 2026-11-27.)
	WorkDirs func(ctx context.Context) ([]string, error)
	Now      func() time.Time
}

// ---- lifecycle ("Phase 7 session lifecycle") ----

// State is a session's lifecycle state.
type State string

const (
	StateStarting     State = "starting"
	StateInitializing State = "initializing"
	StateCreating     State = "creating"
	StateRunning      State = "running"
	StateWaiting      State = "waiting" // an ask is held for a person
	StateCancelling   State = "cancelling"
	StateEnded        State = "ended"
)

// Transitions lists the states each state may move to. Every transition
// is written to session.json under the store lock and publishes
// ai.session.changed. Ended is final.
var Transitions = map[State][]State{
	StateStarting:     {StateInitializing, StateEnded},
	StateInitializing: {StateCreating, StateEnded},
	StateCreating:     {StateRunning, StateEnded},
	StateRunning:      {StateWaiting, StateCancelling, StateEnded},
	StateWaiting:      {StateRunning, StateCancelling, StateEnded},
	StateCancelling:   {StateEnded},
	StateEnded:        {},
}

// End codes (Session.Code once ended). A turn that the agent ended carries
// its stop reason: end_turn, max_tokens, max_turn_requests, refusal or
// cancelled.
const (
	CodeEndTurn         = string(acp.StopEndTurn)
	CodeMaxTokens       = string(acp.StopMaxTokens)
	CodeMaxTurnRequests = string(acp.StopMaxTurnRequests)
	CodeRefusal         = string(acp.StopRefusal)
	CodeCancelled       = string(acp.StopCancelled) // ai cancel
	CodeLaunch          = "launch"
	CodeProtocolVersion = "protocol_version"
	CodeProtocol        = "protocol"
	CodeTimeout         = "timeout" // initialize, session/new or set_mode
	CodeAuthRequired    = "auth_required"
	CodeLimitDuration   = "limit_duration"
	CodeLimitIdle       = "limit_idle"
	CodeLimitOutput     = "limit_output"
	CodeUnasked         = "unasked"
	CodeExited          = "exited"
	CodeHostLost        = "host_lost"
)

// Refusal codes of Prepare and Start (Refusal.Code), in the order ai.run
// checks them.
const (
	RefuseNested            = "nested"
	RefuseAgentStartOff     = "agent_start_off"
	RefuseRepoDisabled      = "repo_disabled"
	RefusePrimaryCheckout   = "primary_checkout"
	RefuseNotReady          = "not_ready"
	RefusePluginLaunch      = "plugin_launch"
	RefuseUnsafeEnvironment = "unsafe_environment"
	RefuseHomeUnsafe        = "home_unsafe"
	RefuseTestGuard         = "test_guard"
	RefuseLimitRunning      = "limit_running"
	RefuseLimitWorktree     = "limit_worktree"
	RefuseLimitRate         = "limit_rate"
	RefuseNotWorktree       = "not_worktree" // the directory is not in a git worktree
)

// Refusal is the error Prepare, Start, Answer and Cancel return for a rule
// that refuses. Code is fixed; Message and Fix are agentflow's own text.
type Refusal struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

func (r *Refusal) Error() string { return r.Message }

// ---- requester ("Phase 7 who may start a session and who may answer") ----

// Requester kinds and surfaces.
const (
	RequesterPerson  = "person"
	RequesterAgent   = "agent"
	RequesterSession = "session"

	SurfaceTerminal = "terminal"
	SurfaceConsole  = "console"
	SurfaceCLI      = "cli"
	SurfaceMCP      = "mcp"
)

// Requester is set by the calling surface, never by an input field.
type Requester struct {
	Kind    string `json:"kind"`              // person, agent, session
	Surface string `json:"surface,omitempty"` // terminal, console, cli, mcp
	Client  string `json:"client,omitempty"`  // the app client: "cli", "claude", "ai-session:<id>"
	Session ID     `json:"session,omitempty"` // the caller's own session when Kind is session, else ""
}

type requesterKey struct{}

// WithRequester carries r in ctx.
func WithRequester(ctx context.Context, r Requester) context.Context {
	return context.WithValue(ctx, requesterKey{}, r)
}

// RequesterFrom returns the requester WithRequester set. Without one the
// caller is treated as an agent by every rule.
func RequesterFrom(ctx context.Context) (Requester, bool) {
	r, ok := ctx.Value(requesterKey{}).(Requester)
	return r, ok
}

// ---- permission chain ("Phase 7 permission chain") ----

// Kind is what the agent asked for.
type Kind string

const (
	KindPermission Kind = "permission" // session/request_permission
	KindRead       Kind = "fs_read"    // fs/read_text_file
	KindWrite      Kind = "fs_write"   // fs/write_text_file
	KindTerminal   Kind = "terminal"   // terminal/create
)

// Request is one agent request, as agentflow resolved it. Title, ToolKind,
// ToolCallID and Options are the agent's words (sanitized, display only);
// everything else agentflow worked out itself.
type Request struct {
	Session    ID
	Kind       Kind
	ToolCallID string
	ToolKind   acp.ToolKind
	Title      string
	Options    []acp.PermissionOption
	// Paths: fs path, or the tool call's locations, resolved by the boundary.
	Paths   []Path
	Command *Command // KindTerminal
	Size    int64    // KindWrite: content bytes
	// Set by the never-list: only a person may allow this.
	PersonOnly bool
	// Reason is why it would ask: control_file, secret_file, terminal,
	// repository_program, no_locations, kind, rule, gate, ceiling.
	Reason string
}

type Path struct {
	Abs    string // cleaned absolute path as asked
	Rel    string // slash path relative to the worktree root; "" when outside
	Real   string // resolved, when it exists
	Exists bool
}

type Command struct {
	Program  string // as asked
	Resolved string // absolute, after lookup on the session PATH
	Args     []string
	Cwd      Path
	EnvNames []string // the agent's env names; values are never shown or stored
	Line     string   // POSIX-quoted Program and Args, for the never-list and the boundary
}

type Decision string

const (
	Allow Decision = "allow"
	Deny  Decision = "deny"
	Ask   Decision = "ask" // hold it for a person
)

// Verdict names who decided and why.
type Verdict struct {
	Decision Decision
	Handler  string // never, boundary, rule, gate, default, person, timeout, cancel
	Code     string // e.g. outside_worktree, git_dir, never_list, rule:<name>, gate_never
}

type Next func(ctx context.Context, r *Request) Verdict

// PermissionHandler is one link of the chain. It returns Deny, or sets
// fields on r and returns next(ctx, r). Only the rule and gate links may
// return Allow or Ask.
type PermissionHandler interface {
	Decide(ctx context.Context, r *Request, next Next) Verdict
}

// Verdict handlers.
const (
	HandlerNever    = "never"
	HandlerBoundary = "boundary"
	HandlerRule     = "rule"
	HandlerGate     = "gate"
	HandlerDefault  = "default"
	HandlerPerson   = "person"
	HandlerTimeout  = "timeout"
	HandlerCancel   = "cancel"
	HandlerLimit    = "limit" // asks waiting, ask rate, terminals: before the chain
)

// Verdict codes. A rule verdict's code is "rule:" and the rule's name; a
// plugin rule's "plugin:" and the plugin ID.
const (
	// boundary and file access ("Phase 7 file access")
	VerdictRelative         = "relative"
	VerdictOutsideWorktree  = "outside_worktree"
	VerdictGitDir           = "git_dir"
	VerdictName             = "name"
	VerdictNestedRepository = "nested_repository"
	VerdictOtherRepository  = "other_repository" // boundary.Checker refused a terminal
	VerdictNotRegular       = "not_regular"
	VerdictTooLarge         = "too_large"
	VerdictNotText          = "not_text"
	VerdictLink             = "link"
	VerdictHardlink         = "hardlink"
	VerdictNotFound         = "not_found" // answered -32002
	VerdictIO               = "io"
	VerdictChanged          = "changed" // the file changed since its preview was shown
	// never-lists ("Phase 7 terminals")
	VerdictNeverList          = "never_list"
	VerdictAgentflowInSession = "agentflow_in_session"
	VerdictAgentflowHome      = "agentflow_home"
	VerdictPrivilege          = "privilege"
	VerdictEnv                = "env"
	VerdictTestGuard          = "test_guard"
	VerdictProgram            = "program" // the program is not found or not executable
	// requests and limits
	VerdictWrongSession     = "wrong_session"
	VerdictTooManyAsks      = "too_many_asks"
	VerdictRate             = "rate"
	VerdictTooManyTerminals = "too_many_terminals"
	VerdictUnknownTerminal  = "unknown_terminal"
	// gate and default
	VerdictGateAuto    = "gate_auto"
	VerdictGateConfirm = "gate_confirm"
	VerdictGateSuggest = "gate_suggest"
	VerdictGateNever   = "gate_never"
	VerdictPlugin      = "plugin"
	VerdictDefault     = "default"
	// person, timeout, cancel
	VerdictAllowed   = "allowed"
	VerdictRejected  = "rejected"
	VerdictExpired   = "expired"
	VerdictCancelled = "cancelled"
)

// Reasons a request asks a person (Request.Reason).
const (
	ReasonControlFile       = "control_file"
	ReasonSecretFile        = "secret_file"
	ReasonTerminal          = "terminal"
	ReasonRepositoryProgram = "repository_program"
	ReasonShell             = "shell" // a shell or wrapper
	ReasonNoLocations       = "no_locations"
	ReasonKind              = "kind"
	ReasonRule              = "rule"
	ReasonGate              = "gate"
	ReasonCeiling           = "ceiling"
	// ReasonPreviewTooLarge: an fs_write whose preview is cut or not text;
	// no one can allow it.
	ReasonPreviewTooLarge = "preview_too_large"
)

// Gate actions and their classes ("Per request kind").
const (
	ActionRun        = "ai.run"
	ActionRespond    = "ai.respond"
	ActionCancel     = "ai.cancel"
	ActionFSRead     = "ai.fs.read"
	ActionFSWrite    = "ai.fs.write"
	ActionTerminal   = "ai.terminal"
	ActionFetch      = "ai.fetch"
	ActionPermission = "ai.permission"
)

// CeilingConfirm is Session.Ceiling for a session an agent started: every
// gate action except ai.fs.read is capped at confirm.
const CeilingConfirm = "confirm"

// ---- asks and answers ----

// AskRecord is asks/<ask>.json: a request held for a person. Only the
// session's host writes it. Fingerprint is the SHA-256 of the canonical
// ask; every answer is bound to it and to ExpiresAt.
type AskRecord struct {
	// ID is "ask_" and 16 lowercase hex characters (^ask_[0-9a-f]{16}$).
	ID         string                 `json:"id"`
	Session    ID                     `json:"session"`
	Kind       Kind                   `json:"kind"`
	ToolCallID string                 `json:"tool_call,omitempty"`
	ToolKind   acp.ToolKind           `json:"tool_kind,omitempty"`
	Title      string                 `json:"title,omitempty"` // the agent's words, sanitized
	Options    []acp.PermissionOption `json:"options,omitempty"`
	// Paths are worktree-relative, or "~/..." outside the worktree.
	Paths    []string `json:"paths,omitempty"`
	Line     string   `json:"line,omitempty"` // POSIX-quoted command line
	Cwd      string   `json:"cwd,omitempty"`  // worktree-relative
	EnvNames []string `json:"env_names,omitempty"`
	Size     int64    `json:"size,omitempty"`
	// Diff, for an fs_write ask, is what the write would change: the
	// current content and the exact bytes that will be written, each at
	// most MaxPreview. It is not redacted (the person judges the real
	// content), lives only in this 0600 file, and is removed when the ask is
	// answered, expires or the session ends: never in the transcript, an
	// event, a log or the audit. NewSHA256 is the SHA-256 of the content
	// (covered by the fingerprint); agentflow writes exactly those bytes.
	Diff      *AskDiff `json:"diff,omitempty"`
	NewSHA256 string   `json:"new_sha256,omitempty"`
	Reason    string   `json:"reason"`
	// CanAllow is false for a permission request without an allow_once
	// option: the ask then shows only Reject.
	CanAllow  bool      `json:"can_allow"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Fingerprint is the SHA-256 of the canonical ask as 64 lowercase hex
	// characters (^[0-9a-f]{64}$).
	Fingerprint string `json:"fingerprint"`
}

// AskDiff is an fs_write ask's preview. Cut is set when either text was
// longer than MaxPreview or is not valid UTF-8: the ask then cannot be
// allowed (CanAllow false, reason preview_too_large).
type AskDiff struct {
	Path    string `json:"path"`
	OldText string `json:"old_text,omitempty"` // empty for a new file
	NewText string `json:"new_text"`
	Cut     bool   `json:"cut,omitempty"`
}

// MaxPreview bounds each text of an AskDiff.
const MaxPreview = 256 << 10

// AnswerRecord is asks/<ask>.answer.json, created with O_EXCL: the first
// answer wins. An allow carries MAC, the hex HMAC-SHA256 of the
// fingerprint under the session's answer.key; without a valid MAC the host
// treats the answer as a reject.
type AnswerRecord struct {
	Ask         string    `json:"ask"`
	Decision    Decision  `json:"decision"` // allow (allow_once) or deny
	By          Requester `json:"by"`
	At          time.Time `json:"at"`
	Fingerprint string    `json:"fingerprint"`
	MAC         string    `json:"mac,omitempty"`
}

// AnswerRequest is one answer to an ask (aisession.Answer applies the rules
// of "who may answer"; the requester comes from the context).
type AnswerRequest struct {
	Session ID
	Ask     string
	// Decision is Allow or Deny.
	Decision Decision
	// Fingerprint, when set, must match the ask's (the console sends the
	// one it showed).
	Fingerprint string
}

// AnswerResult is what Answer reports.
type AnswerResult struct {
	Session  ID       `json:"session"`
	Ask      string   `json:"ask"`
	Decision Decision `json:"decision"`
	State    State    `json:"state"`
}

// Answer refusal codes (Refusal.Code).
const (
	AnswerNeedsPerson = "needs_person" // an agent may only reject
	AnswerSelf        = "self_answer"  // a session never answers its own requests
	AnswerNotFound    = "not_found"    // no such session or waiting ask
	AnswerAnswered    = "answered"     // another answer came first
	AnswerExpired     = "expired"
	AnswerFingerprint = "fingerprint" // the ask changed since it was shown
	AnswerCannotAllow = "cannot_allow"
	AnswerEnded       = "ended"
)

// ---- session store ("Phase 7 session store and retention") ----

// Session is session.json.
type Session struct {
	ID           ID         `json:"id"`
	Harness      string     `json:"harness"`
	HarnessName  string     `json:"harness_name"`
	Identity     string     `json:"identity"` // harness.Found.Identity at start
	Repo         string     `json:"repo"`     // bus repository ID
	WorktreeID   string     `json:"worktree_id"`
	Worktree     string     `json:"worktree"` // absolute; shown with "~"
	Branch       string     `json:"branch,omitempty"`
	State        State      `json:"state"`
	Code         string     `json:"code,omitempty"`
	StartedBy    Requester  `json:"started_by"`
	Ceiling      string     `json:"ceiling,omitempty"`       // "confirm" for an agent's session
	Home         string     `json:"home"`                    // real, isolated
	EnvNames     []string   `json:"env_names"`               // names passed to the harness, never values
	MCP          bool       `json:"mcp"`                     // agentflow's MCP server passed
	AgentSession string     `json:"agent_session,omitempty"` // the ACP sessionId, sanitized, 128 bytes
	Mode         string     `json:"mode,omitempty"`
	Host         HostInfo   `json:"host"`
	CreatedAt    time.Time  `json:"created_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	Usage        *Usage     `json:"usage,omitempty"`
	Counters     Counters   `json:"counters"`
	ChangedFiles []string   `json:"changed_files,omitempty"`
}

type HostInfo struct {
	PID        int       `json:"pid"`
	Mode       string    `json:"mode"` // foreground, detached
	StartedAt  time.Time `json:"started_at"`
	Heartbeat  time.Time `json:"heartbeat"`
	HarnessPID int       `json:"harness_pid,omitempty"`
}

// Host modes (HostInfo.Mode).
const (
	HostForeground = "foreground"
	HostDetached   = "detached"
)

type Usage struct {
	Used, Size   uint64
	CostAmount   float64 `json:"cost_amount,omitempty"`
	CostCurrency string  `json:"cost_currency,omitempty"`
}

type Counters struct {
	Updates         int   `json:"updates"`
	ToolCalls       int   `json:"tool_calls"`
	Asks            int   `json:"asks"`
	Allowed         int   `json:"allowed"`
	Denied          int   `json:"denied"`
	Unasked         int   `json:"unasked"`
	Terminals       int   `json:"terminals"`
	BytesRead       int64 `json:"bytes_read"`
	BytesWritten    int64 `json:"bytes_written"`
	TranscriptBytes int64 `json:"transcript_bytes"`
}

// Item is one transcript line.
type Item struct {
	Seq      int64           `json:"seq"`
	Time     time.Time       `json:"time"`
	Kind     string          `json:"kind"`           // prompt, message, thought, tool, plan, usage, mode, ask, answer, terminal, file, unasked, limit, info, unknown
	Text     string          `json:"text,omitempty"` // sanitized, at most 64 KiB
	ToolCall string          `json:"tool_call,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"` // the kind's view, sanitized
}

// Item kinds.
const (
	ItemPrompt   = "prompt"
	ItemMessage  = "message"
	ItemThought  = "thought"
	ItemTool     = "tool"
	ItemPlan     = "plan"
	ItemUsage    = "usage"
	ItemMode     = "mode"
	ItemAsk      = "ask"
	ItemAnswer   = "answer"
	ItemTerminal = "terminal"
	ItemFile     = "file"
	ItemUnasked  = "unasked"
	ItemLimit    = "limit"
	ItemInfo     = "info"
	ItemUnknown  = "unknown"
)

// Caps are the store-counted start limits, checked with the new record
// under the store lock.
type Caps struct {
	MaxRunning    int // per user
	PerWorktree   int // 1
	StartsPerHour int
}

// Store is the session store under <home>/ai (OpenStore). Every method
// that changes session.json holds the store lock (<home>/ai/sessions.lock)
// and replaces the file atomically. Files are 0600 in 0700 directories.
type Store interface {
	// Dir is the session's directory.
	Dir(id ID) string
	// Create counts the running sessions (not ended, fresh heartbeat), the
	// sessions in s.Worktree and the starts in the last hour, refuses past
	// caps (*Refusal limit_running, limit_worktree, limit_rate), and writes
	// s (state starting), answer.key and the prompt item, all under one
	// lock.
	Create(s Session, prompt string, caps Caps) (Session, error)
	Get(id ID) (Session, error)
	// List returns every session, newest first. Sessions whose host
	// heartbeat is older than 30 s are ended host_lost first.
	List() ([]Session, error)
	// Update applies fn under the lock. A state change must be in
	// Transitions; fn may not change ID.
	Update(id ID, fn func(*Session) error) (Session, error)
	// Append adds items to transcript.jsonl, numbering them, and returns
	// them with their Seq. Past the transcript limit text is dropped.
	Append(id ID, items ...Item) ([]Item, error)
	// Items returns up to max items with Seq > since.
	Items(id ID, since int64, max int) ([]Item, error)
	// Tail returns the newest items, at most max and maxBytes, oldest
	// first, and whether older ones exist.
	Tail(id ID, max, maxBytes int) ([]Item, bool, error)
	PutAsk(a AskRecord) error
	// Asks returns the asks of id that have no answer yet.
	Asks(id ID) ([]AskRecord, error)
	GetAsk(id ID, ask string) (AskRecord, error)
	RemoveAsk(id ID, ask string) error
	// WriteAnswer creates asks/<ask>.answer.json with O_EXCL.
	WriteAnswer(id ID, a AnswerRecord) error
	ReadAnswer(id ID, ask string) (AnswerRecord, bool, error)
	AnswerKey(id ID) ([]byte, error)
	// RequestCancel records a cancel request for the host.
	RequestCancel(id ID, by Requester) error
	CancelRequested(id ID) bool
	// Prune applies the retention rules; running sessions are never deleted.
	Prune(keepDays int, keepTranscripts bool) error
}

// ---- host and manager ----

// Host runs one claimed session: it is the acp.ClientHandler the harness
// talks to, and Run drives the lifecycle until the session ends.
type Host interface {
	acp.ClientHandler
	Run(ctx context.Context) (Session, error)
}

// StartRequest asks for a session. Dir is the starting process's own
// working directory (the CLI's, or the MCP server's); the session runs in
// the worktree that contains it. No caller input chooses another.
type StartRequest struct {
	Harness string
	Prompt  string
	Dir     string
	// Detach hosts the session in a detached `ai host` process. A session
	// an agent starts is always detached.
	Detach bool
}

// Plan is what a start would do: the confirmation summary. It holds
// variable names, never values.
type Plan struct {
	Harness     string    `json:"harness"`
	HarnessName string    `json:"harness_name"`
	Version     string    `json:"version,omitempty"`
	Command     string    `json:"command"`
	Path        string    `json:"path"` // "~/..."
	Args        []string  `json:"args"`
	Identity    string    `json:"identity"`
	Worktree    string    `json:"worktree"` // "~/..."
	Branch      string    `json:"branch,omitempty"`
	StartedBy   Requester `json:"started_by"`
	Ceiling     string    `json:"ceiling,omitempty"`
	Home        string    `json:"home"` // real, isolated
	EnvNames    []string  `json:"env_names"`
	// CredentialEnv are the definition's credential_env names that are set
	// and pass to the harness (never to its terminals).
	CredentialEnv []string `json:"credential_env,omitempty"`
	MCP           bool     `json:"mcp"`
	MCPSkipped    string   `json:"mcp_skipped,omitempty"` // mcp_skipped, off
	Limits        Limits   `json:"limits"`
	// ControlFiles differ from the merge base with the default branch, or
	// are untracked: the harness loads them at start (a warning).
	ControlFiles []string `json:"control_files,omitempty"`
	Prompt       string   `json:"prompt"` // the first 4 KiB
	Detached     bool     `json:"detached"`
}

// Limits are one session's effective limits ("Phase 7 limits").
type Limits struct {
	MaxRunning  int           `json:"max_running"`
	MaxDuration time.Duration `json:"max_duration"`
	IdleTimeout time.Duration `json:"idle_timeout"`
	AskTimeout  time.Duration `json:"ask_timeout"`
}

// Fixed limits.
const (
	DefaultMaxRunning   = 2
	MinMaxRunning       = 1
	MaxMaxRunning       = 4
	PerWorktree         = 1
	StartsPerHour       = 10
	DefaultMaxDuration  = 30 * time.Minute
	MinMaxDuration      = time.Minute
	MaxMaxDuration      = 4 * time.Hour
	DefaultIdleTimeout  = 10 * time.Minute
	MinIdleTimeout      = time.Minute
	MaxIdleTimeout      = time.Hour
	DefaultAskTimeout   = 5 * time.Minute
	MinAskTimeout       = 30 * time.Second
	MaxAskTimeout       = 30 * time.Minute
	MaxAsksWaiting      = 4
	MaxAsksPerMinute    = 30
	MaxFileBytes        = 4 << 20
	MaxTerminalsOpen    = 4
	MaxTerminals        = 64
	DefaultTerminalOut  = 256 << 10
	MaxTerminalOut      = 1 << 20
	TranscriptTermOut   = 64 << 10
	MaxTranscript       = 32 << 20
	MaxItemText         = 64 << 10
	MaxPrompt           = 4 << 10
	MaxAgentEnv         = 32
	MaxAgentEnvValue    = 4 << 10
	MaxChangedFiles     = 200
	SetupTimeout        = 30 * time.Second // initialize, session/new, set_mode
	CancelWait          = 10 * time.Second
	StopGrace           = 2 * time.Second
	HeartbeatEvery      = 5 * time.Second
	HeartbeatLost       = 30 * time.Second
	OutputEventInterval = 250 * time.Millisecond // at most 4 a second
	DefaultKeepDays     = 7
	MinKeepDays         = 1
	MaxKeepDays         = 90
	MaxEndedKept        = 50
	MaxStoreBytes       = 200 << 20
)

// HostOptions configure Manager.Host.
type HostOptions struct {
	// Mode is HostForeground (the ai run process) or HostDetached.
	Mode string
	// OnItems receives new transcript items as they are stored (the
	// foreground terminal). It must not block for long.
	OnItems func([]Item)
	// OnAsk is called when an ask is held, for the foreground terminal to
	// prompt; the answer goes through Manager.Answer.
	OnAsk func(AskRecord)
}

// Manager is the Process Manager every surface uses (lane 7.3's app
// methods call it; the console projects the store and answers through the
// ai.respond and ai.cancel commands). The requester comes from ctx
// (WithRequester); without one every rule treats the caller as an agent.
type Manager interface {
	// Prepare runs every ai.run check and returns the confirmation summary.
	// A failed check is a *Refusal.
	Prepare(ctx context.Context, r StartRequest) (Plan, error)
	// Start runs Prepare again, writes the record under the store lock and,
	// when detached, starts the detached host. A foreground caller then
	// calls Host.
	Start(ctx context.Context, r StartRequest) (Session, error)
	// Host claims a starting session and runs it until it ends.
	Host(ctx context.Context, id ID, o HostOptions) (Session, error)
	// List returns the sessions, newest first.
	List(ctx context.Context) ([]Session, error)
	// Get returns one session and its waiting asks.
	Get(ctx context.Context, id ID) (Session, []AskRecord, error)
	// Items returns transcript items after since, and the next since.
	Items(ctx context.Context, id ID, since int64, max int) ([]Item, int64, error)
	// Answer applies the answer rules and writes the answer file.
	Answer(ctx context.Context, r AnswerRequest) (AnswerResult, error)
	// Cancel asks the host to cancel the session.
	Cancel(ctx context.Context, id ID) (Session, error)
	// Prune applies the retention rules.
	Prune(ctx context.Context) error
}
