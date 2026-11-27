package acp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// ErrNoDeadline is returned by Prompt when its context has no deadline.
var ErrNoDeadline = errors.New("acp: session/prompt needs a context with a deadline")

// sessionCallTimeout bounds initialize, session/new and session/set_mode,
// whatever Limits the connection has.
var sessionCallTimeout = SessionLimits.CallTimeout

// SessionInitialize is the initialize request of a session. It differs
// from the probe's: fs and terminal are true, auth.terminal is false.
func SessionInitialize(version string) InitializeRequest {
	return InitializeRequest{
		ProtocolVersion: ProtocolVersion,
		ClientCapabilities: ClientCapabilities{
			FS:       FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
			Terminal: true,
			Auth:     AuthCapabilities{Terminal: false},
		},
		ClientInfo: &Implementation{Name: "agentflow", Title: "agentflow", Version: version},
	}
}

// NewSession sends session/new and decodes the answer by the decode rules:
// sessionId is required (else ErrProtocol); modes that fail to decode are
// nil, and an invalid mode is skipped. A nil MCPServers, Args or Env is
// sent as [], which the schema requires. A peer error (for example -32000,
// authentication required) is returned as *jsonrpc.Error. It waits at most
// 30 s.
func NewSession(ctx context.Context, c jsonrpc.Conn, r NewSessionRequest) (NewSessionResponse, error) {
	servers := make([]MCPServer, 0, len(r.MCPServers))
	for _, s := range r.MCPServers {
		if s.Args == nil {
			s.Args = []string{}
		}
		if s.Env == nil {
			s.Env = []EnvVariable{}
		}
		servers = append(servers, s)
	}
	r.MCPServers = servers
	ctx, cancel := context.WithTimeout(ctx, sessionCallTimeout)
	defer cancel()
	var raw json.RawMessage
	if err := c.Call(ctx, MethodSessionNew, r, &raw); err != nil {
		return NewSessionResponse{}, err
	}
	return decodeNewSession(raw)
}

// SetMode sends session/set_mode. Its answer carries nothing agentflow
// reads. It waits at most 30 s.
func SetMode(ctx context.Context, c jsonrpc.Conn, r SetSessionModeRequest) error {
	ctx, cancel := context.WithTimeout(ctx, sessionCallTimeout)
	defer cancel()
	return c.Call(ctx, MethodSessionSetMode, r, nil)
}

// Prompt sends session/prompt and waits until ctx ends. ctx must have a
// deadline (ErrNoDeadline): jsonrpc's CallTimeout never applies to it.
//
// stopReason is required (else ErrProtocol). A value outside the
// StopReason constants is returned as it is; the caller treats it as an
// unknown reason. On a connection with SessionLimits (OrderedResponses),
// every session/update the agent sent before its answer has been handled
// when Prompt returns.
func Prompt(ctx context.Context, c jsonrpc.Conn, r PromptRequest) (PromptResponse, error) {
	if _, ok := ctx.Deadline(); !ok {
		return PromptResponse{}, ErrNoDeadline
	}
	if r.Prompt == nil {
		r.Prompt = []ContentBlock{}
	}
	var raw json.RawMessage
	if err := c.Call(ctx, MethodSessionPrompt, r, &raw); err != nil {
		return PromptResponse{}, err
	}
	return decodePrompt(raw)
}

// Cancel sends session/cancel.
func Cancel(ctx context.Context, c jsonrpc.Conn, sessionID string) error {
	return c.Notify(ctx, MethodSessionCancel, CancelNotification{SessionID: sessionID})
}
