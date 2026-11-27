package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// errCancelRequest is the cause of a held request's context when the
// agent cancelled it with "$/cancel_request".
var errCancelRequest = errors.New("acp: the agent cancelled the request")

// maxEarlyCancels bounds the "$/cancel_request" IDs kept for requests not
// yet seen (the notification can be handled before its request starts).
const maxEarlyCancels = 64

// NewClientHandler decodes every agent request by the phase 6 decode rules
// and calls h. Unknown methods, ext methods ("_..."), elicitation/* and
// every method agentflow does not advertise get -32601. A
// "$/cancel_request" for a request h is still handling cancels its context;
// the request is then answered -32800, or {"outcome":"cancelled"} for a
// permission request.
//
// It is the Proxy between the jsonrpc connection and the session host:
// Request-Reply for each client method, and session/update translated into
// a SessionNotification (Message Translator). A request whose required
// params are missing or of the wrong type is answered -32602 and h is not
// called; an invalid session/update or an unknown notification is dropped.
func NewClientHandler(h ClientHandler) jsonrpc.Handler {
	return &clientHandler{h: h, held: map[string]context.CancelCauseFunc{}, early: map[string]struct{}{}}
}

type clientHandler struct {
	h ClientHandler

	mu         sync.Mutex
	held       map[string]context.CancelCauseFunc
	early      map[string]struct{}
	earlyOrder []string
}

func (c *clientHandler) Handle(ctx context.Context, r *jsonrpc.Request) (any, error) {
	if r.Notification() {
		switch r.Method {
		case MethodSessionUpdate:
			if n, ok := decodeSessionNotification(r.Params); ok {
				c.h.SessionUpdate(ctx, n)
			}
		case MethodCancelRequest:
			if id, ok := decodeCancelRequest(r.Params); ok {
				c.cancel(string(id))
			}
		}
		return nil, nil
	}
	switch r.Method {
	case MethodRequestPermission:
		req, ok := decodePermissionRequest(r.Params)
		if !ok {
			return nil, invalidParams()
		}
		return c.run(ctx, r.ID, true, func(ctx context.Context) (any, error) {
			return c.h.RequestPermission(ctx, req)
		})
	case MethodReadTextFile:
		req, ok := decodeReadRequest(r.Params)
		if !ok {
			return nil, invalidParams()
		}
		return c.run(ctx, r.ID, false, func(ctx context.Context) (any, error) {
			return c.h.ReadTextFile(ctx, req)
		})
	case MethodWriteTextFile:
		req, ok := decodeWriteRequest(r.Params)
		if !ok {
			return nil, invalidParams()
		}
		return c.run(ctx, r.ID, false, func(ctx context.Context) (any, error) {
			return c.h.WriteTextFile(ctx, req)
		})
	case MethodTerminalCreate:
		req, ok := decodeCreateTerminal(r.Params)
		if !ok {
			return nil, invalidParams()
		}
		return c.run(ctx, r.ID, false, func(ctx context.Context) (any, error) {
			return c.h.CreateTerminal(ctx, req)
		})
	case MethodTerminalOutput, MethodTerminalWaitForExit, MethodTerminalKill, MethodTerminalRelease:
		req, ok := decodeTerminalRequest(r.Params)
		if !ok {
			return nil, invalidParams()
		}
		return c.run(ctx, r.ID, false, func(ctx context.Context) (any, error) {
			switch r.Method {
			case MethodTerminalOutput:
				return c.h.TerminalOutput(ctx, req)
			case MethodTerminalWaitForExit:
				return c.h.WaitForTerminalExit(ctx, req)
			case MethodTerminalKill:
				return struct{}{}, c.h.KillTerminal(ctx, req)
			default:
				return struct{}{}, c.h.ReleaseTerminal(ctx, req)
			}
		})
	}
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found", Data: errData("method_not_found")}
}

// run holds one request so "$/cancel_request" can cancel it. A cancelled
// request is answered as cancelled whatever fn returned.
func (c *clientHandler) run(parent context.Context, id jsonrpc.ID, permission bool, fn func(context.Context) (any, error)) (any, error) {
	key := idKey(id)
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	c.mu.Lock()
	c.held[key] = cancel
	if _, ok := c.early[key]; ok {
		delete(c.early, key)
		cancel(errCancelRequest)
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.held, key)
		c.mu.Unlock()
	}()

	result, err := fn(ctx)
	if errors.Is(context.Cause(ctx), errCancelRequest) {
		if permission {
			return RequestPermissionResponse{Outcome: PermissionOutcome{Outcome: OutcomeCancelled}}, nil
		}
		return nil, &jsonrpc.Error{Code: CodeRequestCancelled, Message: "request cancelled", Data: errData("cancelled")}
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

// cancel cancels the held request with this ID, or remembers the ID for a
// request that has not started yet.
func (c *clientHandler) cancel(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cancel, ok := c.held[key]; ok {
		cancel(errCancelRequest)
		return
	}
	if _, ok := c.early[key]; ok {
		return
	}
	if len(c.earlyOrder) >= maxEarlyCancels {
		delete(c.early, c.earlyOrder[0])
		c.earlyOrder = c.earlyOrder[1:]
	}
	c.early[key] = struct{}{}
	c.earlyOrder = append(c.earlyOrder, key)
}

// idKey is a request ID in compact form, so "$/cancel_request" matches it
// however it was spaced.
func idKey(id jsonrpc.ID) string {
	var buf bytes.Buffer
	if json.Compact(&buf, id) != nil {
		return string(bytes.TrimSpace(id))
	}
	return buf.String()
}

func invalidParams() error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params", Data: errData("invalid_params")}
}

func errData(code string) json.RawMessage {
	b, _ := json.Marshal(jsonrpc.ErrorData{Code: code})
	return b
}
