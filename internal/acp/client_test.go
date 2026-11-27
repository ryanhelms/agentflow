package acp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

// recorder is a ClientHandler that records every call. answer, when set,
// decides the result; otherwise each method gives a fixed one.
type recorder struct {
	mu      sync.Mutex
	calls   []call
	updates []acp.SessionNotification
	seen    chan struct{} // one value per call or update

	answer func(ctx context.Context, method string, req any) (any, error)
}

type call struct {
	method string
	req    any
}

func newRecorder() *recorder { return &recorder{seen: make(chan struct{}, 1024)} }

func (r *recorder) record(method string, req any) {
	r.mu.Lock()
	r.calls = append(r.calls, call{method, req})
	r.mu.Unlock()
	r.seen <- struct{}{}
}

func (r *recorder) reply(ctx context.Context, method string, req any, def any) (any, error) {
	r.record(method, req)
	if r.answer != nil {
		return r.answer(ctx, method, req)
	}
	return def, nil
}

func (r *recorder) SessionUpdate(_ context.Context, n acp.SessionNotification) {
	r.mu.Lock()
	r.updates = append(r.updates, n)
	r.mu.Unlock()
	r.seen <- struct{}{}
}

func (r *recorder) RequestPermission(ctx context.Context, q acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	v, err := r.reply(ctx, acp.MethodRequestPermission, q, acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: acp.OutcomeSelected, OptionID: "reject-once"}})
	out, _ := v.(acp.RequestPermissionResponse)
	return out, err
}

func (r *recorder) ReadTextFile(ctx context.Context, q acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	v, err := r.reply(ctx, acp.MethodReadTextFile, q, acp.ReadTextFileResponse{Content: "content"})
	out, _ := v.(acp.ReadTextFileResponse)
	return out, err
}

func (r *recorder) WriteTextFile(ctx context.Context, q acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	_, err := r.reply(ctx, acp.MethodWriteTextFile, q, acp.WriteTextFileResponse{})
	return acp.WriteTextFileResponse{}, err
}

func (r *recorder) CreateTerminal(ctx context.Context, q acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	v, err := r.reply(ctx, acp.MethodTerminalCreate, q, acp.CreateTerminalResponse{TerminalID: "term-1"})
	out, _ := v.(acp.CreateTerminalResponse)
	return out, err
}

func (r *recorder) TerminalOutput(ctx context.Context, q acp.TerminalRequest) (acp.TerminalOutputResponse, error) {
	v, err := r.reply(ctx, acp.MethodTerminalOutput, q, acp.TerminalOutputResponse{Output: "hi\n"})
	out, _ := v.(acp.TerminalOutputResponse)
	return out, err
}

func (r *recorder) WaitForTerminalExit(ctx context.Context, q acp.TerminalRequest) (acp.TerminalExitStatus, error) {
	zero := uint32(0)
	v, err := r.reply(ctx, acp.MethodTerminalWaitForExit, q, acp.TerminalExitStatus{ExitCode: &zero})
	out, _ := v.(acp.TerminalExitStatus)
	return out, err
}

func (r *recorder) KillTerminal(ctx context.Context, q acp.TerminalRequest) error {
	_, err := r.reply(ctx, acp.MethodTerminalKill, q, nil)
	return err
}

func (r *recorder) ReleaseTerminal(ctx context.Context, q acp.TerminalRequest) error {
	_, err := r.reply(ctx, acp.MethodTerminalRelease, q, nil)
	return err
}

func (r *recorder) wait(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-r.seen:
		case <-time.After(5 * time.Second):
			t.Fatalf("handler saw %d of %d messages", i, n)
		}
	}
}

func (r *recorder) snapshot() ([]call, []acp.SessionNotification) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]call(nil), r.calls...), append([]acp.SessionNotification(nil), r.updates...)
}

// agentWire is the agent side of a client connection, as raw frames.
type agentWire struct {
	t    *testing.T
	peer *jsonrpctest.Peer
	next int
}

func newAgentWire(t *testing.T, h acp.ClientHandler) *agentWire {
	t.Helper()
	_, peer := jsonrpctest.Wire(t, acp.NewClientHandler(h), acp.SessionLimits)
	return &agentWire{t: t, peer: peer}
}

func (a *agentWire) send(frame string) {
	a.t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(frame)); err != nil {
		a.t.Fatalf("frame %s: %v", frame, err)
	}
	if err := a.peer.WriteLine(buf.String()); err != nil {
		a.t.Fatal(err)
	}
}

// request sends a request and returns its ID.
func (a *agentWire) request(method, params string) int {
	a.t.Helper()
	a.next++
	a.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, a.next, method, params))
	return a.next
}

func (a *agentWire) notify(method, params string) {
	a.t.Helper()
	a.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":%s}`, method, params))
}

// answer reads the next frame, which must be a response.
func (a *agentWire) answer() jsonrpc.Message {
	a.t.Helper()
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := a.peer.ReadLine()
		ch <- res{line, err}
	}()
	var r res
	select {
	case r = <-ch:
	case <-time.After(5 * time.Second):
		a.t.Fatal("no answer")
	}
	if r.err != nil {
		a.t.Fatal(r.err)
	}
	var m jsonrpc.Message
	if err := json.Unmarshal([]byte(r.line), &m); err != nil {
		a.t.Fatalf("frame %q: %v", r.line, err)
	}
	return m
}

func errorOf(t *testing.T, m jsonrpc.Message) (int, string) {
	t.Helper()
	if m.Error == nil {
		t.Fatalf("want an error, got result %s", m.Result)
	}
	var d jsonrpc.ErrorData
	_ = json.Unmarshal(m.Error.Data, &d)
	return m.Error.Code, d.Code
}

func TestClientHandlerAnswersUnadvertisedMethods(t *testing.T) {
	rec := newRecorder()
	a := newAgentWire(t, rec)
	for _, method := range []string{
		"elicitation/create", "elicitation/complete", "_zed/custom", "session/new",
		"session/prompt", "initialize", "authenticate", "fs/delete", "terminal/run", "nope",
	} {
		a.request(method, `{"sessionId":"s"}`)
		if code, data := errorOf(t, a.answer()); code != jsonrpc.CodeMethodNotFound || data != "method_not_found" {
			t.Errorf("%s: code %d data %q, want -32601", method, code, data)
		}
	}
	// Unknown notifications are dropped; the connection stays up.
	a.notify("_zed/ping", `{}`)
	a.notify("session/cancel", `{"sessionId":"s"}`)
	a.request(acp.MethodTerminalOutput, `{"sessionId":"s","terminalId":"t"}`)
	if m := a.answer(); m.Error != nil {
		t.Fatalf("after notifications: %v", m.Error)
	}
	if calls, _ := rec.snapshot(); len(calls) != 1 {
		t.Fatalf("handler calls %v, want only terminal/output", calls)
	}
}

func TestClientHandlerRefusesInvalidParams(t *testing.T) {
	cases := []struct{ method, params string }{
		{acp.MethodReadTextFile, `{"sessionId":"s"}`},
		{acp.MethodReadTextFile, `{"sessionId":"s","path":7}`},
		{acp.MethodReadTextFile, `{"path":"/a"}`},
		{acp.MethodReadTextFile, `[]`},
		{acp.MethodWriteTextFile, `{"sessionId":"s","path":"/a"}`},
		{acp.MethodWriteTextFile, `{"sessionId":"s","path":"/a","content":null}`},
		{acp.MethodTerminalCreate, `{"sessionId":"s"}`},
		{acp.MethodTerminalCreate, `{"sessionId":"s","command":"ls","args":["-l",7]}`},
		{acp.MethodTerminalCreate, `{"sessionId":"s","command":"ls","args":"-l"}`},
		{acp.MethodTerminalCreate, `{"sessionId":"s","command":"ls","args":["-l",null]}`},
		{acp.MethodTerminalCreate, `{"sessionId":"s","command":"ls","env":[{"name":"A","value":null}]}`},
		{acp.MethodTerminalCreate, `{"sessionId":"s","command":"ls","env":[{"name":"A"}]}`},
		{acp.MethodTerminalCreate, `{"sessionId":"s","command":"ls","env":[{"name":"A","value":"1"},"B=2"]}`},
		{acp.MethodTerminalCreate, `{"sessionId":"s","command":"ls","cwd":5}`},
		{acp.MethodTerminalOutput, `{"sessionId":"s"}`},
		{acp.MethodTerminalWaitForExit, `{"terminalId":"t"}`},
		{acp.MethodTerminalKill, `{"sessionId":"s","terminalId":""}`},
		{acp.MethodTerminalRelease, `[]`},
		{acp.MethodRequestPermission, `{"sessionId":"s","options":[]}`},
		{acp.MethodRequestPermission, `{"sessionId":"s","toolCall":{},"options":[]}`},
		{acp.MethodRequestPermission, `{"sessionId":"s","toolCall":{"toolCallId":"c"}}`},
	}
	rec := newRecorder()
	a := newAgentWire(t, rec)
	for _, c := range cases {
		a.request(c.method, c.params)
		if code, data := errorOf(t, a.answer()); code != jsonrpc.CodeInvalidParams || data != "invalid_params" {
			t.Errorf("%s %s: code %d data %q, want -32602", c.method, c.params, code, data)
		}
	}
	if calls, _ := rec.snapshot(); len(calls) != 0 {
		t.Fatalf("handler was called for invalid params: %v", calls)
	}
}

func TestClientHandlerAnswerShapes(t *testing.T) {
	rec := newRecorder()
	a := newAgentWire(t, rec)
	ids := `{"sessionId":"s","terminalId":"t"}`
	cases := []struct{ method, params, want string }{
		{acp.MethodTerminalKill, ids, `{}`},
		{acp.MethodTerminalRelease, ids, `{}`},
		{acp.MethodWriteTextFile, `{"sessionId":"s","path":"/a","content":""}`, `{}`},
		{acp.MethodTerminalWaitForExit, ids, `{"exitCode":0,"signal":null}`},
		{acp.MethodTerminalOutput, ids, `{"output":"hi\n","truncated":false}`},
		{acp.MethodTerminalCreate, `{"sessionId":"s","command":"ls"}`, `{"terminalId":"term-1"}`},
		{acp.MethodReadTextFile, `{"sessionId":"s","path":"/a"}`, `{"content":"content"}`},
		{acp.MethodRequestPermission, `{"sessionId":"s","toolCall":{"toolCallId":"c"},"options":[]}`, `{"outcome":{"outcome":"selected","optionId":"reject-once"}}`},
	}
	for _, c := range cases {
		a.request(c.method, c.params)
		m := a.answer()
		if m.Error != nil || string(m.Result) != c.want {
			t.Errorf("%s: result %s error %v, want %s", c.method, m.Result, m.Error, c.want)
		}
	}
}

func TestClientHandlerSendsHandlerErrorsAsIs(t *testing.T) {
	rec := newRecorder()
	rec.answer = func(_ context.Context, method string, _ any) (any, error) {
		switch method {
		case acp.MethodReadTextFile:
			return nil, &jsonrpc.Error{Code: acp.CodeDenied, Message: "denied: outside the worktree", Data: json.RawMessage(`{"code":"outside_worktree"}`)}
		case acp.MethodWriteTextFile:
			return nil, &jsonrpc.Error{Code: acp.CodeResourceNotFound, Message: "not found"}
		}
		return nil, errors.New("secret /path/in/an/error")
	}
	a := newAgentWire(t, rec)
	a.request(acp.MethodReadTextFile, `{"sessionId":"s","path":"/etc/hosts"}`)
	if code, data := errorOf(t, a.answer()); code != acp.CodeDenied || data != "outside_worktree" {
		t.Errorf("read: %d %q", code, data)
	}
	a.request(acp.MethodWriteTextFile, `{"sessionId":"s","path":"/a","content":""}`)
	if code, _ := errorOf(t, a.answer()); code != acp.CodeResourceNotFound {
		t.Errorf("write: %d", code)
	}
	a.request(acp.MethodTerminalCreate, `{"sessionId":"s","command":"ls"}`)
	m := a.answer()
	if code, _ := errorOf(t, m); code != jsonrpc.CodeInternalError || strings.Contains(m.Error.Message, "secret") {
		t.Errorf("other error: %d %q", code, m.Error.Message)
	}
}

// held answers every request only when its context ends.
func held(started chan<- string) func(ctx context.Context, method string, _ any) (any, error) {
	return func(ctx context.Context, method string, _ any) (any, error) {
		started <- method
		<-ctx.Done()
		// What a host returns after its context ends does not matter: the
		// cancelled answer replaces it.
		return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: acp.OutcomeSelected, OptionID: "allow-once"}}, ctx.Err()
	}
}

func TestCancelRequestCancelsHeldRequests(t *testing.T) {
	started := make(chan string, 8)
	rec := newRecorder()
	rec.answer = held(started)
	a := newAgentWire(t, rec)

	cases := []struct {
		method, params string
		permission     bool
	}{
		{acp.MethodRequestPermission, `{"sessionId":"s","toolCall":{"toolCallId":"c"},"options":[{"optionId":"allow-once","name":"Allow","kind":"allow_once"}]}`, true},
		{acp.MethodReadTextFile, `{"sessionId":"s","path":"/a"}`, false},
		{acp.MethodWriteTextFile, `{"sessionId":"s","path":"/a","content":"x"}`, false},
		{acp.MethodTerminalCreate, `{"sessionId":"s","command":"ls"}`, false},
		{acp.MethodTerminalWaitForExit, `{"sessionId":"s","terminalId":"t"}`, false},
	}
	for _, c := range cases {
		id := a.request(c.method, c.params)
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never reached the handler", c.method)
		}
		a.notify(acp.MethodCancelRequest, fmt.Sprintf(`{"requestId": %d }`, id))
		m := a.answer()
		if c.permission {
			if m.Error != nil || string(m.Result) != `{"outcome":{"outcome":"cancelled"}}` {
				t.Errorf("permission: result %s error %v", m.Result, m.Error)
			}
			continue
		}
		if code, data := errorOf(t, m); code != acp.CodeRequestCancelled || data != "cancelled" {
			t.Errorf("%s: %d %q, want -32800", c.method, code, data)
		}
	}
}

func TestCancelRequestMatchesStringIDs(t *testing.T) {
	started := make(chan string, 1)
	rec := newRecorder()
	rec.answer = held(started)
	a := newAgentWire(t, rec)
	a.send(`{"jsonrpc":"2.0","id":"read-1","method":"fs/read_text_file","params":{"sessionId":"s","path":"/a"}}`)
	<-started
	a.notify(acp.MethodCancelRequest, `{"requestId":"read-2"}`) // another request: no effect
	a.notify(acp.MethodCancelRequest, `{"requestId":"read-1"}`)
	m := a.answer()
	if string(m.ID) != `"read-1"` {
		t.Fatalf("answer to %s", m.ID)
	}
	if code, _ := errorOf(t, m); code != acp.CodeRequestCancelled {
		t.Fatalf("code %d", code)
	}
}

func TestCancelRequestBeforeTheRequestStarts(t *testing.T) {
	// The notification can be handled before the request's goroutine
	// starts; the request is then cancelled as soon as it does.
	rec := newRecorder()
	rec.answer = func(ctx context.Context, _ string, _ any) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	h := acp.NewClientHandler(rec)
	ctx := context.Background()
	if _, err := h.Handle(ctx, &jsonrpc.Request{Method: acp.MethodCancelRequest, Params: json.RawMessage(`{"requestId":5}`)}); err != nil {
		t.Fatal(err)
	}
	_, err := h.Handle(ctx, &jsonrpc.Request{ID: jsonrpc.ID(`5`), Method: acp.MethodReadTextFile, Params: json.RawMessage(`{"sessionId":"s","path":"/a"}`)})
	var rerr *jsonrpc.Error
	if !errors.As(err, &rerr) || rerr.Code != acp.CodeRequestCancelled {
		t.Fatalf("err = %v, want -32800", err)
	}
}

func TestCancelRequestAfterTheAnswerChangesNothing(t *testing.T) {
	rec := newRecorder()
	h := acp.NewClientHandler(rec)
	ctx := context.Background()
	res, err := h.Handle(ctx, &jsonrpc.Request{ID: jsonrpc.ID(`1`), Method: acp.MethodReadTextFile, Params: json.RawMessage(`{"sessionId":"s","path":"/a"}`)})
	if err != nil || res.(acp.ReadTextFileResponse).Content != "content" {
		t.Fatalf("read: %v %v", res, err)
	}
	for _, p := range []string{`{"requestId":1}`, `{"requestId":null}`, `{"requestId":{}}`, `{}`, `[]`} {
		if _, err := h.Handle(ctx, &jsonrpc.Request{Method: acp.MethodCancelRequest, Params: json.RawMessage(p)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClientHandlerRunsRequestsConcurrently(t *testing.T) {
	// A held request (a permission waiting for a person) does not stop
	// others or session/update.
	started := make(chan string, 1)
	release := make(chan struct{})
	rec := newRecorder()
	rec.answer = func(ctx context.Context, method string, req any) (any, error) {
		if method == acp.MethodRequestPermission {
			started <- method
			<-release
			return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: acp.OutcomeCancelled}}, nil
		}
		return acp.ReadTextFileResponse{Content: "x"}, nil
	}
	a := newAgentWire(t, rec)
	a.request(acp.MethodRequestPermission, `{"sessionId":"s","toolCall":{"toolCallId":"c"},"options":[]}`)
	<-started
	a.notify(acp.MethodSessionUpdate, `{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}`)
	a.request(acp.MethodReadTextFile, `{"sessionId":"s","path":"/a"}`)
	if m := a.answer(); m.Error != nil || string(m.ID) != "2" {
		t.Fatalf("read while permission held: %s %v", m.ID, m.Error)
	}
	close(release)
	if m := a.answer(); string(m.ID) != "1" {
		t.Fatalf("permission answer %s", m.ID)
	}
}
