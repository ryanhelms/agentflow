package plugin

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// Instance is one plugin as the host drives it, whatever its tier. The
// process tier's Instance is a Proxy for the child; the declarative tier's
// is an interpreter of its rules; a builtin's is its own Go value. The host
// calls Deliver for one event at a time, in bus order.
type Instance interface {
	// Start brings the plugin up: for a process, launch it and complete
	// initialize. It returns when the plugin is ready or failed.
	Start(ctx context.Context, env Env) error
	// Deliver hands over one event. A non-nil error sends the event to the
	// Dead Letter Channel with the error's code (Code).
	Deliver(ctx context.Context, e bus.Event) error
	// Settings sends changed settings (settings.changed).
	Settings(ctx context.Context, settings map[string]any) error
	// Stop asks the plugin to shut down, then ends it.
	Stop(ctx context.Context) error
	// Done is closed when the plugin ends on its own (a process exited or
	// broke the protocol); Err then gives the reason, with a code.
	Done() <-chan struct{}
	Err() error
}

// Env is what the host offers an Instance: a Facade that checks every call
// against the plugin's granted permissions, limits and repository rules
// before it reaches the bus or the Invoker.
type Env interface {
	// Plugin is the plugin's manifest ID.
	Plugin() string
	// Settings are the plugin's effective settings: manifest defaults,
	// then the personal layer's plugins.<id>.settings.
	Settings() map[string]any
	// Subscribe replaces the plugin's subscriptions. Each pattern must be
	// granted (Permissions.Subscribe). It returns the accepted patterns.
	Subscribe(ctx context.Context, patterns []string) ([]string, error)
	// Invoke runs a command (command.invoke).
	Invoke(ctx context.Context, r InvokeRequest) (InvokeResult, error)
	// Publish publishes one event (event.publish).
	Publish(ctx context.Context, r PublishRequest) (PublishResult, error)
	// Log writes to the plugin's log file, never to an event.
	Log(level, message string)
}

// Factory makes Instances of one tier: the Abstract Factory. The host keeps
// one Factory per tier in a Registry and never switches on the tier itself.
type Factory interface {
	Tier() manifest.Tier
	New(p manifest.Installed) (Instance, error)
}

// Builtin is a plugin compiled into agentflow (TierBuiltin), registered from
// an init function with RegisterBuiltin (lane 5.2). Builtins are trusted
// code, but the host still runs them through Env with their manifest's
// permissions, and they start disabled like any plugin. Phase 5 ships none;
// phase 6 harness definitions are the first.
type Builtin struct {
	Manifest manifest.Manifest
	New      func() Instance
}

// InvokeRequest is command.invoke's request, as the host checks it.
type InvokeRequest struct {
	// Command is a Spec name in manifest.InvocableCommands and the
	// plugin's Permissions.Invoke.
	Command string `json:"command"`
	// Input is the command's MCP input object. It must not contain
	// confirm_token; use Resume.
	Input json.RawMessage `json:"input,omitempty"`
	// Cause is the ID of the event the plugin reacts to. It becomes the
	// op.requested CausationID, and its repository and worktree are the
	// call's unless Repo and Worktree are set.
	Cause string `json:"cause,omitempty"`
	// Repo and Worktree pick the worktree to run in: one the daemon
	// serves, in a repository that does not disable the plugin.
	Repo     bus.RepoID     `json:"repo,omitempty"`
	Worktree bus.WorktreeID `json:"worktree,omitempty"`
	// Resume is the approval ID of an earlier awaiting_approval result. The
	// gate lets the call proceed only after a person approved that record.
	Resume string `json:"resume,omitempty"`
}

// InvokeResult is command.invoke's result. It never carries a confirm
// token, an approval fingerprint or error text.
type InvokeResult struct {
	// OperationID is the operation's ID on the timeline.
	OperationID string `json:"operation_id"`
	// Outcome is applied, failed, suggested or awaiting_approval (a
	// needs_confirmation result is reported as awaiting_approval: its token
	// is dropped and a person answers on the console).
	Outcome string `json:"outcome"`
	// Result is an applied command's result, at most MaxResultBytes, or
	// null when larger (Truncated).
	Result    json.RawMessage `json:"result,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
	// ApprovalID and ExpiresAt are set for awaiting_approval.
	ApprovalID string     `json:"approval_id,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	// Code is set for failed and suggested: command.ErrorCode values or a
	// refusal status.
	Code string `json:"code,omitempty"`
}

// PublishRequest is event.publish's request.
type PublishRequest struct {
	// Type is in manifest.PublishableTypes and Permissions.Publish.
	Type bus.Type `json:"type"`
	// Payload is the type's payload. For plugin.annotation.added the host
	// sets plugin, checks operation_id, url (https only) and level, and
	// clamps it.
	Payload json.RawMessage `json:"payload"`
	// Cause, Repo and Worktree as in InvokeRequest. One of Cause or Repo
	// is required.
	Cause    string         `json:"cause,omitempty"`
	Repo     bus.RepoID     `json:"repo,omitempty"`
	Worktree bus.WorktreeID `json:"worktree,omitempty"`
}

// PublishResult is event.publish's result.
type PublishResult struct {
	ID string `json:"id"`
}

// Host limits (ADR, "Phase 5 limits").
const (
	InitializeTimeout = 5 * time.Second
	DeliverTimeout    = 10 * time.Second
	ShutdownTimeout   = 2 * time.Second
	PingInterval      = 30 * time.Second
	PingTimeout       = 5 * time.Second
	// MissedPingsDegraded and MissedPingsRestart: consecutive missed pings.
	MissedPingsDegraded = 2
	MissedPingsRestart  = 4
	// InvokeTimeout bounds one command.invoke.
	InvokeTimeout = 60 * time.Second
	// InvokesPerMinute and PublishesPerMinute are token buckets per
	// plugin; InvokeBurst and PublishBurst their sizes.
	InvokesPerMinute   = 10
	InvokeBurst        = 5
	PublishesPerMinute = 60
	PublishBurst       = 20
	// SubscriptionBuffer is each plugin's bus subscription buffer.
	SubscriptionBuffer = 256
	// MaxResultBytes caps InvokeResult.Result.
	MaxResultBytes = 256 << 10
	// Restart backoff: RestartMin doubling to RestartMax. CrashLoopCount
	// restarts within CrashLoopWindow move the plugin to failed.
	RestartMin      = time.Second
	RestartMax      = 5 * time.Minute
	CrashLoopCount  = 5
	CrashLoopWindow = 10 * time.Minute
	// DeclarativeBudget bounds one declarative handler's work per event,
	// actions excluded.
	DeclarativeBudget = 100 * time.Millisecond
	// LogFileBytes is the size at which <data>/plugin.log rotates (one old
	// file kept).
	LogFileBytes = 1 << 20
	// DeadLetterKeep is how many letters the persistent channel keeps.
	DeadLetterKeep = 1000
)

// Transitions is the lifecycle state machine (ADR, "Phase 5 lifecycle"):
// from each state, the states the host may move a plugin to. A declarative
// or builtin plugin skips degraded and backoff: it has no process.
var Transitions = map[bus.PluginState][]bus.PluginState{
	"":                  {bus.PluginInstalled, bus.PluginStarting, bus.PluginFailed},
	bus.PluginInstalled: {bus.PluginStarting, bus.PluginFailed, bus.PluginRemoved},
	bus.PluginStarting:  {bus.PluginRunning, bus.PluginBackoff, bus.PluginFailed, bus.PluginStopping},
	bus.PluginRunning:   {bus.PluginDegraded, bus.PluginBackoff, bus.PluginFailed, bus.PluginStopping},
	bus.PluginDegraded:  {bus.PluginRunning, bus.PluginBackoff, bus.PluginFailed, bus.PluginStopping},
	bus.PluginBackoff:   {bus.PluginStarting, bus.PluginFailed, bus.PluginStopping},
	bus.PluginFailed:    {bus.PluginStarting, bus.PluginStopping, bus.PluginRemoved},
	bus.PluginStopping:  {bus.PluginStopped, bus.PluginRemoved},
	bus.PluginStopped:   {bus.PluginStarting, bus.PluginInstalled, bus.PluginRemoved},
	bus.PluginRemoved:   nil,
}

// Status is the host's view of one plugin, for the plugins projection and
// plugin list.
type Status struct {
	ID       string          `json:"id"`
	State    bus.PluginState `json:"state"`
	Code     string          `json:"code,omitempty"`
	Since    time.Time       `json:"since"`
	Restarts int             `json:"restarts,omitempty"`
	RetryAt  *time.Time      `json:"retry_at,omitempty"`
	// Counters since the daemon started.
	Delivered   uint64 `json:"delivered"`
	DeadLetters uint64 `json:"dead_letters"`
	Dropped     uint64 `json:"dropped"`
	Invocations uint64 `json:"invocations"`
	Published   uint64 `json:"published"`
	// Subscriptions are the accepted patterns.
	Subscriptions []string `json:"subscriptions"`
}

// Origin values of plugin calls. A plugin's command calls have source and
// client OriginPrefix + id; its events have source OriginPrefix + id.
const OriginPrefix = "plugin:"

// Codes in a jsonrpc ErrorData, and of a host refusal.
const (
	RefuseNotGranted   = "not_granted"   // outside the plugin's Permissions
	RefuseNotAllowed   = "not_allowed"   // outside what any plugin may do
	RefuseConfirmToken = "confirm_token" // input carried confirm_token
	RefuseRateLimited  = "rate_limited"  // token bucket empty
	RefuseRepoDisabled = "repo_disabled" // the repository disables the plugin
	RefuseUnknownTree  = "unknown_worktree"
	RefuseInvalid      = "invalid"   // bad input or payload
	RefuseNotReady     = "not_ready" // called before initialize finished
)

// CodeResumedByHost is the failed Code of a plugin's resume that the host
// already resumed after a person approved it: the write ran, or runs, once,
// and this call ran nothing.
const CodeResumedByHost = "resumed_by_host"

// Layers reads the plugin sections of the two configuration layers. Lane
// 5.3 adds config.User.Plugins (personal: plugins.<id>.enabled and
// plugins.<id>.settings) and config.Repo.Plugins (repo: plugins.disabled);
// lane 5.2's production Layers reads them, and its tests use a fake.
type Layers interface {
	// Personal returns the personal layer's plugins section, by plugin ID.
	Personal(ctx context.Context) (map[string]PersonalPlugin, error)
	// RepoDisabled returns repo:plugins.disabled of the worktree at root.
	RepoDisabled(ctx context.Context, root string) ([]string, error)
}

// PersonalPlugin is personal:plugins.<id>.
type PersonalPlugin struct {
	Enabled  bool           `json:"enabled" yaml:"enabled"`
	Settings map[string]any `json:"settings,omitempty" yaml:"settings,omitempty"`
}
