package codexhost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/provider/native"
)

const writeInventory = `{"id":3,"method":"mcpServerStatus/list","params":{"threadId":"scratch","serverName":"slack","detail":"toolsAndAuthOnly"}}`
const slackTools = `{"id":3,"result":{"data":[{"name":"slack","httpOrigin":"https://mcp.slack.com","authStatus":"oAuth","runtimeStatus":"connected","tools":{"slack_send_message":{"inputSchema":{"type":"object","properties":{"channel_id":{"type":"string"},"message":{"type":"string"}},"required":["channel_id","message"]}},"slack_read_user_profile":{"inputSchema":{"type":"object"}}}}]}}`
const slackSend = `{"id":5,"method":"mcpServer/tool/call","params":{"threadId":"scratch","server":"slack","tool":"slack_send_message","arguments":{"channel_id":"C123","message":"fixture"}}}`

func TestNativeWritePinsApprovedConnectionAndPayload(t *testing.T) {
	dir := t.TempDir()
	trace := setupFake(t, dir, append(handshake(t, dir),
		exchange{writeInventory, []string{slackTools}},
		exchange{`{"id":4,"method":"mcpServer/tool/call","params":{"threadId":"scratch","server":"slack","tool":"slack_read_user_profile","arguments":{}}}`,
			[]string{`{"id":4,"result":{"content":[],"structuredContent":{"workspace_id":"T123"}}}`}},
		exchange{slackSend, []string{`{"id":5,"result":{"content":[],"structuredContent":{"sent":true}}}`}},
		exchange{`{"id":6,"method":"mcpServer/tool/call","params":{"threadId":"scratch","server":"slack","tool":"slack_read_user_profile","arguments":{}}}`,
			[]string{`{"id":6,"result":{"content":[],"structuredContent":{"workspace_id":"T123"}}}`}},
	))
	host := NewWriter(dir)
	t.Cleanup(host.Close)

	call := native.Call{Provider: "slack", Server: "slack", Tool: "slack_send_message",
		Arguments: map[string]any{"channel_id": "C123", "message": "fixture"}}
	preview, cancel := context.WithCancel(t.Context())
	binding, err := host.Prepare(preview, call)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	same, err := host.Prepare(t.Context(), call)
	if err != nil || same != binding {
		t.Fatalf("prepare on confirmation: %+v %v", same, err)
	}
	changed := call
	changed.Arguments = map[string]any{"channel_id": "C123", "message": "altered"}
	if _, err := host.Write(t.Context(), binding, changed); !errors.Is(err, native.ErrDenied) {
		t.Fatalf("changed write: %v", err)
	}
	if _, err := host.ReadBound(t.Context(), binding, call); !errors.Is(err, native.ErrDenied) {
		t.Fatalf("write through scope probe: %v", err)
	}
	if _, err := host.ReadBound(t.Context(), binding, native.Call{Provider: "slack", Server: "slack", Tool: "slack_read_channel"}); !errors.Is(err, native.ErrUnavailable) {
		t.Fatalf("missing capability: %v", err)
	}
	if got, err := host.ReadBound(t.Context(), binding, native.Call{Provider: "slack", Server: "slack", Tool: "slack_read_user_profile"}); err != nil ||
		!strings.Contains(string(got), "T123") {
		t.Fatalf("scope probe: %v", err)
	}
	if _, err := host.Write(t.Context(), binding, call); err != nil {
		t.Fatal(err)
	}
	if _, err := host.ReadBound(t.Context(), binding, native.Call{Provider: "slack", Server: "slack", Tool: "slack_read_user_profile"}); err != nil {
		t.Fatalf("verified readback: %v", err)
	}
	if _, err := host.Write(t.Context(), binding, call); !errors.Is(err, native.ErrDenied) {
		t.Fatalf("replay: %v", err)
	}
	host.Close()
	checkTrace(t, trace)
}

func TestNativeWriteRequiresOfficialAuthenticatedEndpoint(t *testing.T) {
	for _, tt := range []struct {
		name, inventory string
		want            error
	}{
		{"different origin", strings.Replace(slackTools, "https://mcp.slack.com", "https://wrong.example", 1), native.ErrDenied},
		{"no origin", strings.Replace(slackTools, `"https://mcp.slack.com"`, "null", 1), native.ErrUnavailable},
		{"unsigned", strings.Replace(slackTools, "oAuth", "notLoggedIn", 1), native.ErrSignIn},
		{"schema mismatch", slackTools, native.ErrUnavailable},
		{"missing capability", strings.Replace(slackTools, `"slack_send_message":`, `"unknown":`, 1), native.ErrUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			trace := setupFake(t, dir, append(handshake(t, dir), exchange{writeInventory, []string{tt.inventory}}))
			host := NewWriter(dir)
			t.Cleanup(host.Close)
			_, err := host.Prepare(t.Context(), native.Call{Provider: "slack", Server: "slack", Tool: "slack_send_message"})
			if !errors.Is(err, tt.want) {
				t.Fatalf("prepare: %v, want %v", err, tt.want)
			}
			checkTrace(t, trace)
		})
	}
}

func TestNativeWriteFailureCannotFallBackOrReplay(t *testing.T) {
	dir := t.TempDir()
	trace := setupFake(t, dir, append(handshake(t, dir),
		exchange{writeInventory, []string{slackTools}},
		exchange{strings.Replace(slackSend, `"id":5`, `"id":4`, 1),
			[]string{`{"id":4,"error":{"code":-32601,"message":"method no longer available"}}`}},
	))
	host := NewWriter(dir)
	t.Cleanup(host.Close)
	call := native.Call{Provider: "slack", Server: "slack", Tool: "slack_send_message",
		Arguments: map[string]any{"channel_id": "C123", "message": "fixture"}}
	binding, err := host.Prepare(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Write(t.Context(), binding, call); !errors.Is(err, native.ErrWriteUncertain) ||
		errors.Is(err, native.ErrUnavailable) {
		t.Fatalf("failed dispatch allowed fallback: %v", err)
	}
	if _, err := host.Write(t.Context(), binding, call); !errors.Is(err, native.ErrDenied) {
		t.Fatalf("replay: %v", err)
	}
	checkTrace(t, trace)
}
