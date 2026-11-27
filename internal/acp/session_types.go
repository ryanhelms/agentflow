package acp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// Agent methods agentflow calls in phase 7 (meta.json "agentMethods").
const (
	MethodSessionNew     = "session/new"
	MethodSessionPrompt  = "session/prompt"
	MethodSessionSetMode = "session/set_mode"
	MethodSessionCancel  = "session/cancel" // a notification
)

// Client methods the agent calls on agentflow (meta.json "clientMethods").
const (
	MethodSessionUpdate       = "session/update" // a notification
	MethodRequestPermission   = "session/request_permission"
	MethodReadTextFile        = "fs/read_text_file"
	MethodWriteTextFile       = "fs/write_text_file"
	MethodTerminalCreate      = "terminal/create"
	MethodTerminalOutput      = "terminal/output"
	MethodTerminalWaitForExit = "terminal/wait_for_exit"
	MethodTerminalKill        = "terminal/kill"
	MethodTerminalRelease     = "terminal/release"
)

// MethodCancelRequest is the protocol notification "$/cancel_request".
const MethodCancelRequest = "$/cancel_request"

// NewSessionRequest. cwd must be absolute. additionalDirectories and
// systemPrompt are never sent in phase 7.
type NewSessionRequest struct {
	Cwd        string      `json:"cwd"`
	MCPServers []MCPServer `json:"mcpServers"` // required: [] when none
}

// MCPServer is a stdio MCP server (McpServerStdio). Phase 7 sends only
// agentflow's own; http and sse are never sent.
type MCPServer struct {
	Name    string        `json:"name"`
	Command string        `json:"command"` // absolute path
	Args    []string      `json:"args"`
	Env     []EnvVariable `json:"env"` // required: []
}

type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type NewSessionResponse struct {
	SessionID string            `json:"sessionId"`
	Modes     *SessionModeState `json:"modes,omitempty"` // default on error
	// configOptions is ignored in phase 7.
}

type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"` // skip invalid items
}

type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SetSessionModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"` // phase 7 sends one text block
}

type PromptResponse struct {
	StopReason StopReason `json:"stopReason"`
}

type StopReason string

const (
	StopEndTurn         StopReason = "end_turn"
	StopMaxTokens       StopReason = "max_tokens"
	StopMaxTurnRequests StopReason = "max_turn_requests"
	StopRefusal         StopReason = "refusal"
	StopCancelled       StopReason = "cancelled"
)

type CancelNotification struct {
	SessionID string `json:"sessionId"`
}

// CancelRequestNotification is "$/cancel_request". RequestID is the raw
// JSON-RPC ID (number, string or null).
type CancelRequestNotification struct {
	RequestID json.RawMessage `json:"requestId"`
}

// ContentBlock keeps text and the names of other blocks. Image and audio
// data and embedded resource contents are dropped while decoding.
type ContentBlock struct {
	Type     string `json:"type"`               // text, image, audio, resource_link, resource
	Text     string `json:"text,omitempty"`     // text
	URI      string `json:"uri,omitempty"`      // resource_link, resource
	Name     string `json:"name,omitempty"`     // resource_link
	MimeType string `json:"mimeType,omitempty"` // image, audio, resource_link
}

type SessionNotification struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

// UpdateKind is the "sessionUpdate" discriminator.
type UpdateKind string

const (
	UpdateUserMessageChunk  UpdateKind = "user_message_chunk"
	UpdateAgentMessageChunk UpdateKind = "agent_message_chunk"
	UpdateAgentThoughtChunk UpdateKind = "agent_thought_chunk"
	UpdateToolCall          UpdateKind = "tool_call"
	UpdateToolCallUpdate    UpdateKind = "tool_call_update"
	UpdatePlan              UpdateKind = "plan"
	UpdateAvailableCommands UpdateKind = "available_commands_update"
	UpdateCurrentMode       UpdateKind = "current_mode_update"
	UpdateConfigOption      UpdateKind = "config_option_update"
	UpdateSessionInfo       UpdateKind = "session_info_update"
	UpdateUsage             UpdateKind = "usage_update"
)

// SessionUpdate is one decoded update. Exactly the field for its Kind is
// set. Any other kind (notice, compaction_*, plan_update, plan_removed,
// subagent_update, session_message*, or a future one) keeps only Kind and
// Raw: it is recorded as "unknown" and never acted on.
type SessionUpdate struct {
	Kind           UpdateKind
	Content        *ContentBlock   // the three *_chunk kinds
	MessageID      string          // the three *_chunk kinds, optional
	ToolCall       *ToolCall       // tool_call
	ToolCallUpdate *ToolCallUpdate // tool_call_update
	Plan           *Plan           // plan
	CurrentModeID  string          // current_mode_update
	Title          string          // session_info_update
	Usage          *UsageUpdate    // usage_update
	Raw            json.RawMessage // unknown kinds, at most 64 KiB, else dropped
}

type ToolKind string

const (
	ToolRead       ToolKind = "read"
	ToolEdit       ToolKind = "edit"
	ToolDelete     ToolKind = "delete"
	ToolMove       ToolKind = "move"
	ToolSearch     ToolKind = "search"
	ToolExecute    ToolKind = "execute"
	ToolThink      ToolKind = "think"
	ToolFetch      ToolKind = "fetch"
	ToolSwitchMode ToolKind = "switch_mode"
	ToolOther      ToolKind = "other" // also the value for an unknown kind
)

type ToolCallStatus string

const (
	ToolPending    ToolCallStatus = "pending"
	ToolInProgress ToolCallStatus = "in_progress"
	ToolCompleted  ToolCallStatus = "completed"
	ToolFailed     ToolCallStatus = "failed"
)

type ToolCall struct {
	ToolCallID string             `json:"toolCallId"`
	Title      string             `json:"title"`
	Kind       ToolKind           `json:"kind,omitempty"`   // default on error: other
	Status     ToolCallStatus     `json:"status,omitempty"` // default on error: pending
	Content    []ToolCallContent  `json:"content,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
	RawInput   json.RawMessage    `json:"rawInput,omitempty"` // at most 64 KiB, else dropped
}

// ToolCallUpdate: nil or empty fields are unchanged.
type ToolCallUpdate struct {
	ToolCallID string             `json:"toolCallId"`
	Title      *string            `json:"title,omitempty"`
	Kind       *ToolKind          `json:"kind,omitempty"`
	Status     *ToolCallStatus    `json:"status,omitempty"`
	Content    []ToolCallContent  `json:"content,omitempty"`
	Locations  []ToolCallLocation `json:"locations,omitempty"`
	RawInput   json.RawMessage    `json:"rawInput,omitempty"`
}

// ToolCallContent: Type "content" (Content), "diff" (Path, OldText,
// NewText) or "terminal" (TerminalID). "session" and unknown types are
// skipped.
type ToolCallContent struct {
	Type       string        `json:"type"`
	Content    *ContentBlock `json:"content,omitempty"`
	Path       string        `json:"path,omitempty"`
	OldText    *string       `json:"oldText,omitempty"`
	NewText    string        `json:"newText,omitempty"`
	TerminalID string        `json:"terminalId,omitempty"`
}

type ToolCallLocation struct {
	Path string  `json:"path"`
	Line *uint32 `json:"line,omitempty"`
}

type Plan struct {
	Entries []PlanEntry `json:"entries"` // skip invalid items
}

type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority"` // high, medium, low
	Status   string `json:"status"`   // pending, in_progress, completed
}

type UsageUpdate struct {
	Used uint64 `json:"used"`
	Size uint64 `json:"size"`
	Cost *Cost  `json:"cost,omitempty"` // agent-reported; never trusted
}

type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type RequestPermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

type PermissionOptionKind string

const (
	AllowOnce    PermissionOptionKind = "allow_once"
	AllowAlways  PermissionOptionKind = "allow_always"
	RejectOnce   PermissionOptionKind = "reject_once"
	RejectAlways PermissionOptionKind = "reject_always"
)

type PermissionOption struct {
	OptionID string               `json:"optionId"`
	Name     string               `json:"name"`
	Kind     PermissionOptionKind `json:"kind"`
}

type RequestPermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// PermissionOutcome is {"outcome":"cancelled"} or
// {"outcome":"selected","optionId":"..."}.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"` // OutcomeSelected or OutcomeCancelled
	OptionID string `json:"optionId,omitempty"`
}

const (
	OutcomeSelected  = "selected"
	OutcomeCancelled = "cancelled"
)

type ReadTextFileRequest struct {
	SessionID string  `json:"sessionId"`
	Path      string  `json:"path"`
	Line      *uint32 `json:"line,omitempty"`  // 1-based
	Limit     *uint32 `json:"limit,omitempty"` // lines
}

type ReadTextFileResponse struct {
	Content string `json:"content"`
}

type WriteTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

type WriteTextFileResponse struct{}

type CreateTerminalRequest struct {
	SessionID       string        `json:"sessionId"`
	Command         string        `json:"command"`
	Args            []string      `json:"args,omitempty"`
	Env             []EnvVariable `json:"env,omitempty"`
	Cwd             *string       `json:"cwd,omitempty"`
	OutputByteLimit *uint64       `json:"outputByteLimit,omitempty"`
}

type CreateTerminalResponse struct {
	TerminalID string `json:"terminalId"`
}

// TerminalRequest is the params of terminal/output, wait_for_exit, kill
// and release.
type TerminalRequest struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

type TerminalOutputResponse struct {
	Output     string              `json:"output"`
	Truncated  bool                `json:"truncated"`
	ExitStatus *TerminalExitStatus `json:"exitStatus,omitempty"`
}

// TerminalExitStatus is also the result of terminal/wait_for_exit.
type TerminalExitStatus struct {
	ExitCode *uint32 `json:"exitCode"`
	Signal   *string `json:"signal"`
}

// ClientHandler is what agentflow does for each agent request. Lane 7.2
// implements it (the session host); lane 7.1 decodes and dispatches.
// Returning a *jsonrpc.Error sends it as is.
type ClientHandler interface {
	SessionUpdate(ctx context.Context, n SessionNotification)
	RequestPermission(ctx context.Context, r RequestPermissionRequest) (RequestPermissionResponse, error)
	ReadTextFile(ctx context.Context, r ReadTextFileRequest) (ReadTextFileResponse, error)
	WriteTextFile(ctx context.Context, r WriteTextFileRequest) (WriteTextFileResponse, error)
	CreateTerminal(ctx context.Context, r CreateTerminalRequest) (CreateTerminalResponse, error)
	TerminalOutput(ctx context.Context, r TerminalRequest) (TerminalOutputResponse, error)
	WaitForTerminalExit(ctx context.Context, r TerminalRequest) (TerminalExitStatus, error)
	KillTerminal(ctx context.Context, r TerminalRequest) error
	ReleaseTerminal(ctx context.Context, r TerminalRequest) error
}

// agentflow's codes in answers to an agent. ACP reserves -32000, -32002
// and -32800; -32001 is in its "Other" range.
const (
	// CodeDenied: the chain denied the request. Data is {"code": "<verdict code>"}.
	CodeDenied = -32001
)

// SessionLimits are the jsonrpc limits of a session connection.
var SessionLimits = jsonrpc.Limits{
	MaxFrame:         8 << 20,
	MaxInFlight:      16,
	CallTimeout:      30 * time.Second, // initialize, session/new, set_mode; never prompt
	OrderedResponses: true,
}

// Lane 7.1 adds these functions (session.go and client.go):
//
//	// SessionInitialize is the initialize request of a session. It differs
//	// from the probe's: fs and terminal are true, auth.terminal is false.
//	func SessionInitialize(version string) InitializeRequest
//
//	// NewClientHandler decodes every agent request by the phase 6 decode rules
//	// and calls h. Unknown methods, ext methods ("_..."), elicitation/* and
//	// every method agentflow does not advertise get -32601. A
//	// "$/cancel_request" for a request h is still handling cancels its context;
//	// the request is then answered -32800, or {"outcome":"cancelled"} for a
//	// permission request.
//	func NewClientHandler(h ClientHandler) jsonrpc.Handler
//
//	func NewSession(ctx context.Context, c jsonrpc.Conn, r NewSessionRequest) (NewSessionResponse, error)
//	func SetMode(ctx context.Context, c jsonrpc.Conn, r SetSessionModeRequest) error
//
//	// Prompt sends session/prompt and waits until ctx ends. ctx must have a
//	// deadline (ErrNoDeadline): jsonrpc's CallTimeout never applies to it.
//	func Prompt(ctx context.Context, c jsonrpc.Conn, r PromptRequest) (PromptResponse, error)
//
//	// Cancel sends session/cancel.
//	func Cancel(ctx context.Context, c jsonrpc.Conn, sessionID string) error
