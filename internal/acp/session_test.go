package acp_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

// agentPeer is an agent answering with fn; it returns the client Conn and
// what the agent received.
type agentPeer struct {
	mu   sync.Mutex
	got  []jsonrpc.Request
	conn jsonrpc.Conn
}

func (p *agentPeer) received() []jsonrpc.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]jsonrpc.Request(nil), p.got...)
}

func startAgent(t *testing.T, client acp.ClientHandler, l jsonrpc.Limits, fn func(ctx context.Context, agent jsonrpc.Conn, r *jsonrpc.Request) (any, error)) (jsonrpc.Conn, *agentPeer) {
	t.Helper()
	p := &agentPeer{}
	ready := make(chan struct{})
	h := jsonrpc.HandlerFunc(func(ctx context.Context, r *jsonrpc.Request) (any, error) {
		<-ready
		p.mu.Lock()
		p.got = append(p.got, jsonrpc.Request{ID: r.ID, Method: r.Method, Params: append(json.RawMessage(nil), r.Params...)})
		p.mu.Unlock()
		return fn(ctx, p.conn, r)
	})
	var ch jsonrpc.Handler
	if client != nil {
		ch = acp.NewClientHandler(client)
	}
	c, agent := jsonrpctest.Pipe(t, ch, h, l)
	p.conn = agent
	close(ready)
	return c, p
}

func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSessionInitializeRequest(t *testing.T) {
	b, err := json.Marshal(acp.SessionInitialize("1.2.3"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true,"auth":{"terminal":false}},"clientInfo":{"name":"agentflow","title":"agentflow","version":"1.2.3"}}`
	if string(b) != want {
		t.Errorf("\n got %s\nwant %s", b, want)
	}
}

func TestNewSessionSendsRequiredLists(t *testing.T) {
	c, agent := startAgent(t, nil, acp.SessionLimits, func(context.Context, jsonrpc.Conn, *jsonrpc.Request) (any, error) {
		return json.RawMessage(`{"sessionId":"sess_1","configOptions":[{"x":1}],"_meta":{}}`), nil
	})
	resp, err := acp.NewSession(deadline(t), c, acp.NewSessionRequest{Cwd: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.SessionID != "sess_1" || resp.Modes != nil {
		t.Errorf("%+v", resp)
	}
	resp, err = acp.NewSession(deadline(t), c, acp.NewSessionRequest{Cwd: "/w", MCPServers: []acp.MCPServer{{Name: "agentflow", Command: "/bin/agentflow"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := agent.received()
	if string(got[0].Params) != `{"cwd":"/w","mcpServers":[]}` {
		t.Errorf("params %s", got[0].Params)
	}
	if string(got[1].Params) != `{"cwd":"/w","mcpServers":[{"name":"agentflow","command":"/bin/agentflow","args":[],"env":[]}]}` {
		t.Errorf("params %s", got[1].Params)
	}
	_ = resp
}

func TestNewSessionDecodesModes(t *testing.T) {
	answer := `{"sessionId":"sess_abc123def456","modes":{"currentModeId":"ask","availableModes":[
		{"id":"ask","name":"Ask","description":"Request permission before making any changes"},
		{"id":"code","name":"Code"},{"name":"no id"},{"id":"x"},7]}}`
	var compact json.RawMessage
	if err := json.Unmarshal([]byte(answer), &compact); err != nil {
		t.Fatal(err)
	}
	c, _ := startAgent(t, nil, acp.SessionLimits, func(context.Context, jsonrpc.Conn, *jsonrpc.Request) (any, error) {
		return compact, nil
	})
	resp, err := acp.NewSession(deadline(t), c, acp.NewSessionRequest{Cwd: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Modes
	if m == nil || m.CurrentModeID != "ask" || len(m.AvailableModes) != 2 || m.AvailableModes[1] != (acp.SessionMode{ID: "code", Name: "Code"}) {
		t.Errorf("%+v", m)
	}
}

func TestNewSessionBadModesDefault(t *testing.T) {
	for _, modes := range []string{`"ask"`, `{"availableModes":[]}`, `{"currentModeId":"a"}`, `null`} {
		c, _ := startAgent(t, nil, acp.SessionLimits, func(context.Context, jsonrpc.Conn, *jsonrpc.Request) (any, error) {
			return json.RawMessage(`{"sessionId":"s","modes":` + modes + `}`), nil
		})
		resp, err := acp.NewSession(deadline(t), c, acp.NewSessionRequest{Cwd: "/w"})
		if err != nil || resp.Modes != nil {
			t.Errorf("modes %s: %+v %v", modes, resp, err)
		}
	}
}

func TestNewSessionErrors(t *testing.T) {
	for _, answer := range []string{`{}`, `{"sessionId":""}`, `{"sessionId":5}`, `[]`, `"s"`} {
		c, _ := startAgent(t, nil, acp.SessionLimits, func(context.Context, jsonrpc.Conn, *jsonrpc.Request) (any, error) {
			return json.RawMessage(answer), nil
		})
		if _, err := acp.NewSession(deadline(t), c, acp.NewSessionRequest{Cwd: "/w"}); !errors.Is(err, acp.ErrProtocol) {
			t.Errorf("answer %s: err %v, want ErrProtocol", answer, err)
		}
	}
	c, _ := startAgent(t, nil, acp.SessionLimits, func(context.Context, jsonrpc.Conn, *jsonrpc.Request) (any, error) {
		return nil, &jsonrpc.Error{Code: acp.CodeAuthRequired, Message: "Authentication required"}
	})
	_, err := acp.NewSession(deadline(t), c, acp.NewSessionRequest{Cwd: "/w"})
	var rerr *jsonrpc.Error
	if !errors.As(err, &rerr) || rerr.Code != acp.CodeAuthRequired {
		t.Errorf("err %v, want -32000", err)
	}
}

func TestSetModeAndCancel(t *testing.T) {
	c, agent := startAgent(t, nil, acp.SessionLimits, func(_ context.Context, _ jsonrpc.Conn, r *jsonrpc.Request) (any, error) {
		return json.RawMessage(`{}`), nil
	})
	if err := acp.SetMode(deadline(t), c, acp.SetSessionModeRequest{SessionID: "s", ModeID: "code"}); err != nil {
		t.Fatal(err)
	}
	if err := acp.Cancel(deadline(t), c, "s"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(agent.received()) == 2 })
	got := agent.received()
	if got[0].Method != acp.MethodSessionSetMode || string(got[0].Params) != `{"sessionId":"s","modeId":"code"}` {
		t.Errorf("set_mode %s %s", got[0].Method, got[0].Params)
	}
	if got[1].Method != acp.MethodSessionCancel || !got[1].Notification() || string(got[1].Params) != `{"sessionId":"s"}` {
		t.Errorf("cancel %s %s", got[1].Method, got[1].Params)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("condition not met")
}

func TestPromptNeedsADeadline(t *testing.T) {
	c, agent := startAgent(t, nil, acp.SessionLimits, func(context.Context, jsonrpc.Conn, *jsonrpc.Request) (any, error) {
		return json.RawMessage(`{"stopReason":"end_turn"}`), nil
	})
	if _, err := acp.Prompt(context.Background(), c, acp.PromptRequest{SessionID: "s"}); !errors.Is(err, acp.ErrNoDeadline) {
		t.Fatalf("err %v, want ErrNoDeadline", err)
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(agent.received()); n != 0 {
		t.Fatalf("the agent received %d messages", n)
	}
}

func TestPromptOutlivesCallTimeout(t *testing.T) {
	// jsonrpc's CallTimeout never applies to session/prompt: only ctx.
	l := acp.SessionLimits
	l.CallTimeout = 50 * time.Millisecond
	c, agent := startAgent(t, nil, l, func(context.Context, jsonrpc.Conn, *jsonrpc.Request) (any, error) {
		time.Sleep(300 * time.Millisecond)
		return json.RawMessage(`{"stopReason":"max_tokens","_meta":{}}`), nil
	})
	resp, err := acp.Prompt(deadline(t), c, acp.PromptRequest{SessionID: "s", Prompt: []acp.ContentBlock{{Type: "text", Text: "Add a greeting"}}})
	if err != nil || resp.StopReason != acp.StopMaxTokens {
		t.Fatalf("%+v %v", resp, err)
	}
	if got := string(agent.received()[0].Params); got != `{"sessionId":"s","prompt":[{"type":"text","text":"Add a greeting"}]}` {
		t.Errorf("params %s", got)
	}
}

func TestPromptEndsWithItsContext(t *testing.T) {
	c, _ := startAgent(t, nil, acp.SessionLimits, func(ctx context.Context, _ jsonrpc.Conn, _ *jsonrpc.Request) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := acp.Prompt(ctx, c, acp.PromptRequest{SessionID: "s"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
}

func TestPromptAnswers(t *testing.T) {
	for answer, want := range map[string]acp.StopReason{
		`{"stopReason":"end_turn"}`:          acp.StopEndTurn,
		`{"stopReason":"max_turn_requests"}`: acp.StopMaxTurnRequests,
		`{"stopReason":"refusal"}`:           acp.StopRefusal,
		`{"stopReason":"cancelled"}`:         acp.StopCancelled,
		`{"stopReason":"future"}`:            "future",
	} {
		c, _ := startAgent(t, nil, acp.SessionLimits, func(context.Context, jsonrpc.Conn, *jsonrpc.Request) (any, error) {
			return json.RawMessage(answer), nil
		})
		resp, err := acp.Prompt(deadline(t), c, acp.PromptRequest{SessionID: "s"})
		if err != nil || resp.StopReason != want {
			t.Errorf("%s: %+v %v", answer, resp, err)
		}
	}
	for _, answer := range []string{`{}`, `{"stopReason":1}`, `null`, `[]`} {
		c, _ := startAgent(t, nil, acp.SessionLimits, func(context.Context, jsonrpc.Conn, *jsonrpc.Request) (any, error) {
			return json.RawMessage(answer), nil
		})
		if _, err := acp.Prompt(deadline(t), c, acp.PromptRequest{SessionID: "s"}); !errors.Is(err, acp.ErrProtocol) {
			t.Errorf("%s: err %v, want ErrProtocol", answer, err)
		}
	}
}

// slowUpdates counts session/update notifications, slowly.
type slowUpdates struct {
	recorder
	n atomic.Int64
}

func (s *slowUpdates) SessionUpdate(context.Context, acp.SessionNotification) {
	time.Sleep(100 * time.Microsecond)
	s.n.Add(1)
}

func TestPromptReturnsAfterEveryEarlierUpdate(t *testing.T) {
	const updates = 500 // well past the notification queue
	client := &slowUpdates{recorder: *newRecorder()}
	c, _ := startAgent(t, client, acp.SessionLimits, func(ctx context.Context, agent jsonrpc.Conn, r *jsonrpc.Request) (any, error) {
		for i := 0; i < updates; i++ {
			_ = agent.Notify(ctx, acp.MethodSessionUpdate, map[string]any{
				"sessionId": "s",
				"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "x"}},
			})
		}
		return json.RawMessage(`{"stopReason":"end_turn"}`), nil
	})
	resp, err := acp.Prompt(deadline(t), c, acp.PromptRequest{SessionID: "s"})
	if err != nil || resp.StopReason != acp.StopEndTurn {
		t.Fatalf("%+v %v", resp, err)
	}
	if got := client.n.Load(); got != updates {
		t.Fatalf("Prompt returned after %d of %d updates were handled", got, updates)
	}
}

func TestSessionLimits(t *testing.T) {
	l := acp.SessionLimits
	if l.MaxFrame != 8<<20 || l.MaxInFlight != 16 || l.CallTimeout != 30*time.Second || !l.OrderedResponses {
		t.Fatalf("%+v", l)
	}
}
