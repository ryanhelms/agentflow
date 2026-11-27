package acp_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

func probeRequest() acp.InitializeRequest {
	return acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersion,
		ClientCapabilities: acp.ClientCapabilities{
			Auth: acp.AuthCapabilities{Terminal: true},
		},
		ClientInfo: &acp.Implementation{Name: "agentflow", Title: "agentflow", Version: "1.0.0"},
	}
}

// answer runs Initialize against a peer that answers with result (raw
// JSON) or rerr, and returns what the peer received.
func answer(t *testing.T, result string, rerr *jsonrpc.Error) (acp.InitializeResponse, error, json.RawMessage) {
	t.Helper()
	var got json.RawMessage
	agent := jsonrpc.HandlerFunc(func(_ context.Context, r *jsonrpc.Request) (any, error) {
		if r.Method != acp.MethodInitialize {
			t.Errorf("method %q", r.Method)
		}
		got = append(json.RawMessage(nil), r.Params...)
		if rerr != nil {
			return nil, rerr
		}
		return json.RawMessage(result), nil
	})
	client, _ := jsonrpctest.Pipe(t, nil, agent, jsonrpc.Limits{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := acp.Initialize(ctx, client, probeRequest())
	return resp, err, got
}

func TestInitializeSendsTheProbeRequest(t *testing.T) {
	_, err, got := answer(t, `{"protocolVersion":1}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false,"auth":{"terminal":true}},"clientInfo":{"name":"agentflow","title":"agentflow","version":"1.0.0"}}`
	if string(got) != want {
		t.Errorf("params\n got %s\nwant %s", got, want)
	}
}

func TestInitializeDecodesAnAnswer(t *testing.T) {
	resp, err, _ := answer(t, `{
		"protocolVersion": 1,
		"agentCapabilities": {"loadSession": true, "promptCapabilities": {"image": true, "audio": false, "embeddedContext": true},
			"mcpCapabilities": {"http": true, "sse": false}, "sessionCapabilities": {"list": {}}, "auth": {"logout": {}}, "futureThing": 3},
		"authMethods": [
			{"id": "oauth", "name": "Log in"},
			{"id": "term", "name": "Terminal", "type": "terminal", "args": ["login"], "env": {"MODE": "x"}}
		],
		"agentInfo": {"name": "gemini-cli", "title": "Gemini", "version": "0.71.1"},
		"unknownTopLevel": {"x": 1}
	}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := resp.AgentCapabilities
	if !c.LoadSession || !c.PromptCapabilities.Image || !c.PromptCapabilities.EmbeddedContext || !c.MCPCapabilities.HTTP || c.Auth.Logout == nil {
		t.Errorf("capabilities %+v", c)
	}
	if string(c.SessionCapabilities) != `{"list":{}}` {
		t.Errorf("session capabilities %s", c.SessionCapabilities)
	}
	if len(resp.AuthMethods) != 2 || resp.AuthMethods[1].Type != "terminal" || !reflect.DeepEqual(resp.AuthMethods[1].Args, []string{"login"}) {
		t.Errorf("auth methods %+v", resp.AuthMethods)
	}
	if resp.AgentInfo == nil || resp.AgentInfo.Name != "gemini-cli" {
		t.Errorf("agent info %+v", resp.AgentInfo)
	}
}

func TestInitializeDefaultsOnError(t *testing.T) {
	resp, err, _ := answer(t, `{
		"protocolVersion": 1,
		"agentCapabilities": {"loadSession": "yes", "promptCapabilities": {"image": 1}, "mcpCapabilities": {"http": true}},
		"authMethods": [1, null, {"id": "a"}, {"id": "", "name": "x"}, {"id": "ok", "name": "OK"}, {"id": "b", "name": "B", "args": "nope"}],
		"agentInfo": "not an object"
	}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := resp.AgentCapabilities
	if c.LoadSession || c.PromptCapabilities.Image || !c.MCPCapabilities.HTTP {
		t.Errorf("capabilities %+v", c)
	}
	if len(resp.AuthMethods) != 1 || resp.AuthMethods[0].ID != "ok" {
		t.Errorf("auth methods %+v", resp.AuthMethods)
	}
	if resp.AgentInfo != nil {
		t.Errorf("agent info %+v", resp.AgentInfo)
	}

	resp, err, _ = answer(t, `{"protocolVersion": 1, "agentCapabilities": [], "authMethods": {}}`, nil)
	if err != nil || resp.AgentCapabilities.LoadSession || len(resp.AuthMethods) != 0 {
		t.Errorf("%+v %v", resp, err)
	}
}

func TestInitializeProtocolVersion(t *testing.T) {
	for _, raw := range []string{`{}`, `{"protocolVersion": "1"}`, `{"protocolVersion": 1.5}`, `{"protocolVersion": -1}`,
		`{"protocolVersion": 65536}`, `{"protocolVersion": null}`, `[]`, `"x"`, `null`} {
		_, err, _ := answer(t, raw, nil)
		if !errors.Is(err, acp.ErrProtocol) {
			t.Errorf("%s: err = %v", raw, err)
		}
	}
	resp, err, _ := answer(t, `{"protocolVersion": 2}`, nil)
	if err != nil || resp.ProtocolVersion != 2 {
		t.Errorf("v2: %+v %v", resp, err)
	}
	resp, err, _ = answer(t, `{"protocolVersion": 0}`, nil)
	if err != nil || resp.ProtocolVersion != 0 {
		t.Errorf("v0: %+v %v", resp, err)
	}
}

func TestInitializePeerErrorKeepsACPMeaning(t *testing.T) {
	_, err, _ := answer(t, "", &jsonrpc.Error{Code: acp.CodeAuthRequired, Message: "Authentication required"})
	var rerr *jsonrpc.Error
	if !errors.As(err, &rerr) || rerr.Code != acp.CodeAuthRequired {
		t.Fatalf("err = %v", err)
	}
	// -32002 means "resource not found" from an ACP peer, whatever jsonrpc
	// calls it.
	_, err, _ = answer(t, "", &jsonrpc.Error{Code: acp.CodeResourceNotFound, Message: "nope"})
	if !errors.As(err, &rerr) || rerr.Code != acp.CodeResourceNotFound || strings.Contains(err.Error(), "rate") {
		t.Fatalf("err = %v", err)
	}
}
