package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// ErrProtocol is returned by Initialize when the answer is not an ACP
// initialize result: it is not a JSON object, or protocolVersion is missing
// or not an integer from 0 to 65535.
var ErrProtocol = errors.New("acp: the initialize answer is not an ACP result")

// maxProtocolVersion is the largest protocolVersion (a uint16 on the wire).
const maxProtocolVersion = 65535

// Initialize sends initialize on c and decodes the answer as the ACP schema
// says: unknown fields are ignored (ACP is extensible); protocolVersion is
// required and must be an integer from 0 to 65535, else ErrProtocol; a
// capability object, agentInfo or clientInfo that fails to decode becomes
// its zero value ("default on error"); an authMethods item that fails to
// decode is dropped ("skip invalid items"). A peer error is returned as
// *jsonrpc.Error. Lane 7.1 keeps this rule for every ACP message.
//
// An authMethods item fails to decode when it is not an object, its id or
// name is missing or empty, or a field has the wrong JSON type.
func Initialize(ctx context.Context, c jsonrpc.Conn, req InitializeRequest) (InitializeResponse, error) {
	var raw json.RawMessage
	if err := c.Call(ctx, MethodInitialize, req, &raw); err != nil {
		return InitializeResponse{}, err
	}
	return decodeInitialize(raw)
}

func decodeInitialize(raw json.RawMessage) (InitializeResponse, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return InitializeResponse{}, ErrProtocol
	}
	var out InitializeResponse
	pv, ok := fields["protocolVersion"]
	if !ok {
		return InitializeResponse{}, fmt.Errorf("%w: protocolVersion is missing", ErrProtocol)
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(pv))
	dec.UseNumber()
	// A json.Number also accepts a quoted number; ACP's is a bare integer.
	if t := bytes.TrimSpace(pv); len(t) == 0 || t[0] == '"' || dec.Decode(&n) != nil {
		return InitializeResponse{}, fmt.Errorf("%w: protocolVersion is not a number", ErrProtocol)
	}
	v, err := n.Int64()
	if err != nil || v < 0 || v > maxProtocolVersion {
		return InitializeResponse{}, fmt.Errorf("%w: protocolVersion is not an integer from 0 to 65535", ErrProtocol)
	}
	out.ProtocolVersion = int(v)

	if caps, ok := fields["agentCapabilities"]; ok {
		out.AgentCapabilities = decodeAgentCapabilities(caps)
	}
	if info, ok := fields["agentInfo"]; ok {
		var impl Implementation
		if json.Unmarshal(info, &impl) == nil && !isNull(info) {
			out.AgentInfo = &impl
		}
	}
	out.AuthMethods = []AuthMethod{}
	if methods, ok := fields["authMethods"]; ok {
		var items []json.RawMessage
		if json.Unmarshal(methods, &items) == nil {
			for _, item := range items {
				var m AuthMethod
				if json.Unmarshal(item, &m) != nil || isNull(item) || m.ID == "" || m.Name == "" {
					continue
				}
				out.AuthMethods = append(out.AuthMethods, m)
			}
		}
	}
	return out, nil
}

// decodeAgentCapabilities decodes each member on its own, so one that fails
// becomes its zero value without losing the others.
func decodeAgentCapabilities(raw json.RawMessage) AgentCapabilities {
	var out AgentCapabilities
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return out
	}
	if v, ok := fields["loadSession"]; ok {
		_ = json.Unmarshal(v, &out.LoadSession)
	}
	if v, ok := fields["promptCapabilities"]; ok {
		var p PromptCapabilities
		if json.Unmarshal(v, &p) == nil {
			out.PromptCapabilities = p
		}
	}
	if v, ok := fields["mcpCapabilities"]; ok {
		var m MCPCapabilities
		if json.Unmarshal(v, &m) == nil {
			out.MCPCapabilities = m
		}
	}
	if v, ok := fields["sessionCapabilities"]; ok && !isNull(v) {
		out.SessionCapabilities = append(json.RawMessage(nil), v...)
	}
	if v, ok := fields["auth"]; ok {
		var a AgentAuthCapabilities
		if json.Unmarshal(v, &a) == nil {
			out.Auth = a
		}
	}
	return out
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
