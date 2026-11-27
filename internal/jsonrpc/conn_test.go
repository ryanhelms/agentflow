package jsonrpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

var echo = jsonrpc.HandlerFunc(func(_ context.Context, r *jsonrpc.Request) (any, error) {
	return r.Params, nil
})

func waitDone(t *testing.T, c jsonrpc.Conn) error {
	t.Helper()
	select {
	case <-c.Done():
		return c.Err()
	case <-time.After(5 * time.Second):
		t.Fatal("connection did not close")
		return nil
	}
}

// response decodes one frame the Conn wrote.
func response(t *testing.T, p *jsonrpctest.Peer) jsonrpc.Message {
	t.Helper()
	line, err := p.ReadLine()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var m jsonrpc.Message
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("frame %q: %v", line, err)
	}
	if m.JSONRPC != "2.0" {
		t.Fatalf("frame %q has no jsonrpc 2.0", line)
	}
	return m
}

func errCode(t *testing.T, m jsonrpc.Message) (int, string) {
	t.Helper()
	if m.Error == nil {
		t.Fatalf("want an error response, got result %s", m.Result)
	}
	var d jsonrpc.ErrorData
	if len(m.Error.Data) > 0 {
		if err := json.Unmarshal(m.Error.Data, &d); err != nil {
			t.Fatalf("error data %s: %v", m.Error.Data, err)
		}
	}
	return m.Error.Code, d.Code
}

func TestCallAndNotifyBothWays(t *testing.T) {
	var mu sync.Mutex
	var notes []string
	recorder := jsonrpc.HandlerFunc(func(_ context.Context, r *jsonrpc.Request) (any, error) {
		if r.Notification() {
			mu.Lock()
			notes = append(notes, string(r.Params))
			mu.Unlock()
			return nil, nil
		}
		return map[string]string{"method": r.Method}, nil
	})
	a, b := jsonrpctest.Pipe(t, echo, recorder, jsonrpc.Limits{})
	ctx := context.Background()

	var got map[string]int
	if err := b.Call(ctx, "echo", map[string]int{"n": 7}, &got); err != nil || got["n"] != 7 {
		t.Fatalf("b->a call = %v, %v", got, err)
	}
	var back map[string]string
	if err := a.Call(ctx, "hello", nil, &back); err != nil || back["method"] != "hello" {
		t.Fatalf("a->b call = %v, %v", back, err)
	}
	for i := range 20 {
		if err := a.Notify(ctx, "note", []int{i}); err != nil {
			t.Fatal(err)
		}
	}
	// A call after the notifications returns after they were read.
	if err := a.Call(ctx, "sync", nil, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(notes)
		mu.Unlock()
		if n == 20 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notes) != 20 {
		t.Fatalf("got %d notifications, want 20", len(notes))
	}
	for i, n := range notes {
		if n != fmt.Sprintf("[%d]", i) {
			t.Fatalf("notification %d = %s: out of order", i, n)
		}
	}
}

func TestConcurrentCallsOutOfOrderAnswers(t *testing.T) {
	c, peer := jsonrpctest.Wire(t, nil, jsonrpc.Limits{})
	const n = 3
	results := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			errs[i] = c.Call(context.Background(), "m", []int{i}, &results[i])
		})
	}
	reqs := make([]jsonrpc.Message, n)
	for i := range n {
		reqs[i] = response(t, peer)
	}
	for i := n - 1; i >= 0; i-- {
		r := reqs[i]
		answer := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":"for %s"}`, r.ID, r.Params)
		if err := peer.WriteLine(answer); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || results[i] != fmt.Sprintf("for [%d]", i) {
			t.Fatalf("call %d = %q, %v", i, results[i], errs[i])
		}
	}
}

func TestMaxInFlightAnswersBusy(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	h := jsonrpc.HandlerFunc(func(_ context.Context, _ *jsonrpc.Request) (any, error) {
		started <- struct{}{}
		<-release
		return "ok", nil
	})
	_, peer := jsonrpctest.Wire(t, h, jsonrpc.Limits{MaxInFlight: 2})
	for i := 1; i <= 2; i++ {
		if err := peer.WriteLine(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"slow"}`, i)); err != nil {
			t.Fatal(err)
		}
		<-started
	}
	if err := peer.WriteLine(`{"jsonrpc":"2.0","id":"three","method":"slow"}`); err != nil {
		t.Fatal(err)
	}
	m := response(t, peer)
	if string(m.ID) != `"three"` {
		t.Fatalf("first answer is for %s, want the busy one", m.ID)
	}
	if code, data := errCode(t, m); code != jsonrpc.CodeBusy || data != "busy" {
		t.Fatalf("busy answer = %d %q", code, data)
	}
	close(release)
	for range 2 {
		if m := response(t, peer); m.Error != nil {
			t.Fatalf("held request failed: %v", m.Error)
		}
	}
}

func TestCallTimeoutDropsLateAnswer(t *testing.T) {
	c, peer := jsonrpctest.Wire(t, nil, jsonrpc.Limits{CallTimeout: 50 * time.Millisecond})
	errc := make(chan error, 1)
	go func() { errc <- c.Call(context.Background(), "slow", nil, nil) }()
	req := response(t, peer)
	err := <-errc
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call error = %v, want deadline exceeded", err)
	}
	if err := peer.WriteLine(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":"late"}`, req.ID)); err != nil {
		t.Fatal(err)
	}
	// The connection survives: the next call works.
	go func() {
		var out string
		errc <- c.Call(context.Background(), "next", nil, &out)
	}()
	next := response(t, peer)
	if err := peer.WriteLine(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":"ok"}`, next.ID)); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("call after a late answer: %v", err)
	}
	if c.Err() != nil {
		t.Fatalf("connection closed: %v", c.Err())
	}
}

func TestCallContextCancel(t *testing.T) {
	c, peer := jsonrpctest.Wire(t, nil, jsonrpc.Limits{})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- c.Call(ctx, "slow", nil, nil) }()
	response(t, peer)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("call error = %v, want canceled", err)
	}
}

func TestOversizedFrameCloses(t *testing.T) {
	for name, frame := range map[string]string{
		"long line":       `{"jsonrpc":"2.0","method":"x","params":["` + strings.Repeat("a", 2000) + `"]}` + "\n",
		"never a newline": strings.Repeat("b", 5000),
	} {
		t.Run(name, func(t *testing.T) {
			c, peer := jsonrpctest.Wire(t, echo, jsonrpc.Limits{MaxFrame: 1024})
			go func() { _, _ = io.WriteString(peer.Out, frame) }()
			if err := waitDone(t, c); !errors.Is(err, jsonrpc.ErrFrameTooLarge) {
				t.Fatalf("Err = %v, want ErrFrameTooLarge", err)
			}
		})
	}
}

func TestFrameAtLimitIsAccepted(t *testing.T) {
	c, peer := jsonrpctest.Wire(t, echo, jsonrpc.Limits{MaxFrame: 256})
	head := `{"jsonrpc":"2.0","id":1,"method":"echo","params":["`
	tail := `"]}`
	frame := head + strings.Repeat("a", 256-len(head)-len(tail)) + tail
	if err := peer.WriteLine(frame); err != nil {
		t.Fatal(err)
	}
	if m := response(t, peer); m.Error != nil {
		t.Fatalf("frame at the limit refused: %v", m.Error)
	}
	if c.Err() != nil {
		t.Fatalf("closed: %v", c.Err())
	}
	// One byte more closes the connection.
	if err := peer.WriteLine(frame[:len(head)] + "a" + frame[len(head):]); err != nil {
		t.Fatal(err)
	}
	if err := waitDone(t, c); !errors.Is(err, jsonrpc.ErrFrameTooLarge) {
		t.Fatalf("Err = %v", err)
	}
}

func TestOversizedAnswerBecomesInternalError(t *testing.T) {
	h := jsonrpc.HandlerFunc(func(context.Context, *jsonrpc.Request) (any, error) {
		return strings.Repeat("z", 500), nil
	})
	c, peer := jsonrpctest.Wire(t, h, jsonrpc.Limits{MaxFrame: 256})
	if err := peer.WriteLine(`{"jsonrpc":"2.0","id":1,"method":"big"}`); err != nil {
		t.Fatal(err)
	}
	if code, _ := errCode(t, response(t, peer)); code != jsonrpc.CodeInternalError {
		t.Fatalf("code = %d", code)
	}
	if c.Err() != nil {
		t.Fatalf("closed: %v", c.Err())
	}
}

func TestMalformedFramesClose(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		code  int    // answer expected; 0 = none
		id    string // ID of the answer
	}{
		{"not json", `{"jsonrpc":"2.0",`, jsonrpc.CodeParseError, "null"},
		{"text", `hello`, jsonrpc.CodeParseError, "null"},
		{"wrong version", `{"jsonrpc":"1.0","id":1,"method":"x"}`, jsonrpc.CodeInvalidRequest, "1"},
		{"no version", `{"id":1,"method":"x"}`, jsonrpc.CodeInvalidRequest, "1"},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"x"}]`, jsonrpc.CodeInvalidRequest, "null"},
		{"scalar", `42`, jsonrpc.CodeInvalidRequest, "null"},
		{"object id", `{"jsonrpc":"2.0","id":{},"method":"x"}`, jsonrpc.CodeInvalidRequest, "null"},
		{"null id request", `{"jsonrpc":"2.0","id":null,"method":"x"}`, jsonrpc.CodeInvalidRequest, "null"},
		{"scalar params", `{"jsonrpc":"2.0","id":2,"method":"x","params":3}`, jsonrpc.CodeInvalidRequest, "2"},
		{"method and result", `{"jsonrpc":"2.0","id":3,"method":"x","result":1}`, jsonrpc.CodeInvalidRequest, "3"},
		{"result and error", `{"jsonrpc":"2.0","id":4,"result":1,"error":{"code":1,"message":"m"}}`, jsonrpc.CodeInvalidRequest, "null"},
		{"neither", `{"jsonrpc":"2.0","id":5}`, jsonrpc.CodeInvalidRequest, "null"},
		{"unknown response id", `{"jsonrpc":"2.0","id":99,"result":1}`, 0, ""},
		{"string response id", `{"jsonrpc":"2.0","id":"x","result":1}`, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, peer := jsonrpctest.Wire(t, echo, jsonrpc.Limits{})
			if err := peer.WriteLine(tc.frame); err != nil {
				t.Fatal(err)
			}
			if tc.code != 0 {
				m := response(t, peer)
				if code, _ := errCode(t, m); code != tc.code {
					t.Fatalf("code = %d, want %d", code, tc.code)
				}
				if string(m.ID) != tc.id {
					t.Fatalf("id = %s, want %s", m.ID, tc.id)
				}
			}
			if err := waitDone(t, c); !errors.Is(err, jsonrpc.ErrProtocol) {
				t.Fatalf("Err = %v, want ErrProtocol", err)
			}
		})
	}
}

func TestBlankLinesAndUnknownMembersAreFine(t *testing.T) {
	c, peer := jsonrpctest.Wire(t, echo, jsonrpc.Limits{})
	if err := peer.WriteLine("\r"); err != nil {
		t.Fatal(err)
	}
	if err := peer.WriteLine(`{"jsonrpc":"2.0","id":"a-b","method":"echo","params":{"k":1},"extra":true}`); err != nil {
		t.Fatal(err)
	}
	m := response(t, peer)
	if string(m.ID) != `"a-b"` || string(m.Result) != `{"k":1}` {
		t.Fatalf("answer = %s %s", m.ID, m.Result)
	}
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
}

func TestIDEchoedExactly(t *testing.T) {
	_, peer := jsonrpctest.Wire(t, echo, jsonrpc.Limits{})
	for _, id := range []string{`7`, `"seven"`, `1e3`, `-2`} {
		if err := peer.WriteLine(`{"jsonrpc":"2.0","id":` + id + `,"method":"echo"}`); err != nil {
			t.Fatal(err)
		}
		m := response(t, peer)
		if string(m.ID) != id {
			t.Fatalf("id %s echoed as %s", id, m.ID)
		}
		if string(m.Result) != "null" {
			t.Fatalf("result = %s, want null", m.Result)
		}
	}
}

func TestHandlerErrorsNeverLeakText(t *testing.T) {
	secret := "ghp_secretvalue /home/me/.netrc"
	h := jsonrpc.HandlerFunc(func(_ context.Context, r *jsonrpc.Request) (any, error) {
		switch r.Method {
		case "plain":
			return nil, errors.New(secret)
		case "rpc":
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeForbidden, Message: "forbidden", Data: json.RawMessage(`{"code":"not_granted"}`)}
		case "wrapped":
			return nil, fmt.Errorf("%s: %w", secret, &jsonrpc.Error{Code: jsonrpc.CodeRateLimited, Message: "rate limited"})
		case "unmarshalable":
			return func() {}, nil
		}
		return nil, nil
	})
	_, peer := jsonrpctest.Wire(t, h, jsonrpc.Limits{})
	cases := []struct {
		method string
		code   int
		data   string
	}{
		{"plain", jsonrpc.CodeInternalError, "internal"},
		{"rpc", jsonrpc.CodeForbidden, "not_granted"},
		{"wrapped", jsonrpc.CodeRateLimited, ""},
		{"unmarshalable", jsonrpc.CodeInternalError, "internal"},
	}
	for i, tc := range cases {
		if err := peer.WriteLine(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q}`, i+1, tc.method)); err != nil {
			t.Fatal(err)
		}
		line, err := peer.ReadLine()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "ghp_") || strings.Contains(line, ".netrc") {
			t.Fatalf("%s: answer leaks the error text: %s", tc.method, line)
		}
		var m jsonrpc.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		code, data := errCode(t, m)
		if code != tc.code || data != tc.data {
			t.Fatalf("%s: got %d %q, want %d %q", tc.method, code, data, tc.code, tc.data)
		}
		if tc.code == jsonrpc.CodeInternalError && m.Error.Message != "internal error" {
			t.Fatalf("%s: message %q", tc.method, m.Error.Message)
		}
	}
}

func TestPeerErrorReturnedAsError(t *testing.T) {
	h := jsonrpc.HandlerFunc(func(context.Context, *jsonrpc.Request) (any, error) {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeForbidden, Message: "no", Data: json.RawMessage(`{"code":"x"}`)}
	})
	a, _ := jsonrpctest.Pipe(t, nil, h, jsonrpc.Limits{})
	err := a.Call(context.Background(), "m", nil, nil)
	var e *jsonrpc.Error
	if !errors.As(err, &e) || e.Code != jsonrpc.CodeForbidden || string(e.Data) != `{"code":"x"}` {
		t.Fatalf("err = %#v", err)
	}
}

func TestNilHandlerAnswersMethodNotFound(t *testing.T) {
	a, _ := jsonrpctest.Pipe(t, nil, nil, jsonrpc.Limits{})
	err := a.Call(context.Background(), "anything", nil, nil)
	var e *jsonrpc.Error
	if !errors.As(err, &e) || e.Code != jsonrpc.CodeMethodNotFound {
		t.Fatalf("err = %v", err)
	}
	if err := a.Notify(context.Background(), "ignored", nil); err != nil {
		t.Fatal(err)
	}
}

func TestCloseFailsWaitingCalls(t *testing.T) {
	c, peer := jsonrpctest.Wire(t, nil, jsonrpc.Limits{})
	errc := make(chan error, 1)
	go func() { errc <- c.Call(context.Background(), "slow", nil, nil) }()
	response(t, peer)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; !errors.Is(err, jsonrpc.ErrClosed) {
		t.Fatalf("waiting call = %v, want ErrClosed", err)
	}
	if err := c.Err(); err != nil {
		t.Fatalf("Err after Close = %v, want nil", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if err := c.Call(context.Background(), "m", nil, nil); !errors.Is(err, jsonrpc.ErrClosed) {
		t.Fatalf("call after Close = %v", err)
	}
	if err := c.Notify(context.Background(), "m", nil); !errors.Is(err, jsonrpc.ErrClosed) {
		t.Fatalf("notify after Close = %v", err)
	}
}

func TestPeerEOF(t *testing.T) {
	c, peer := jsonrpctest.Wire(t, nil, jsonrpc.Limits{})
	_ = peer.Out.Close()
	if err := waitDone(t, c); !errors.Is(err, io.EOF) {
		t.Fatalf("Err = %v, want io.EOF", err)
	}
}

func TestHandlerContextEndsWithConnection(t *testing.T) {
	ended := make(chan struct{})
	h := jsonrpc.HandlerFunc(func(ctx context.Context, _ *jsonrpc.Request) (any, error) {
		<-ctx.Done()
		close(ended)
		return nil, ctx.Err()
	})
	c, peer := jsonrpctest.Wire(t, h, jsonrpc.Limits{})
	if err := peer.WriteLine(`{"jsonrpc":"2.0","id":1,"method":"wait"}`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	_ = c.Close()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("handler context did not end")
	}
}

func TestStalledPeerTimesOutWrites(t *testing.T) {
	// The peer never reads, so the write blocks and the connection closes.
	c, _ := jsonrpctest.Wire(t, nil, jsonrpc.Limits{WriteTimeout: 50 * time.Millisecond})
	err := c.Notify(context.Background(), "m", nil)
	if !errors.Is(err, jsonrpc.ErrClosed) {
		t.Fatalf("notify = %v, want ErrClosed", err)
	}
	if err := waitDone(t, c); !errors.Is(err, jsonrpc.ErrClosed) {
		t.Fatalf("Err = %v, want ErrClosed", err)
	}
}

func TestOutgoingFrameTooLarge(t *testing.T) {
	a, _ := jsonrpctest.Pipe(t, nil, nil, jsonrpc.Limits{MaxFrame: 128})
	big := []string{strings.Repeat("x", 200)}
	if err := a.Notify(context.Background(), "m", big); !errors.Is(err, jsonrpc.ErrFrameTooLarge) {
		t.Fatalf("notify = %v", err)
	}
	if err := a.Call(context.Background(), "m", big, nil); !errors.Is(err, jsonrpc.ErrFrameTooLarge) {
		t.Fatalf("call = %v", err)
	}
	if a.Err() != nil {
		t.Fatalf("closed: %v", a.Err())
	}
}

func TestParamsMustBeStructured(t *testing.T) {
	a, _ := jsonrpctest.Pipe(t, nil, nil, jsonrpc.Limits{})
	if err := a.Notify(context.Background(), "m", 3); err == nil {
		t.Fatal("scalar params were sent")
	}
}

func TestHandlerPanicBecomesInternalError(t *testing.T) {
	h := jsonrpc.HandlerFunc(func(_ context.Context, r *jsonrpc.Request) (any, error) {
		if r.Method == "ok" {
			return "fine", nil
		}
		panic("handler panics")
	})
	c, peer := jsonrpctest.Wire(t, h, jsonrpc.Limits{})
	// A panicking notification is dropped; a panicking request is answered.
	if err := peer.WriteLine(`{"jsonrpc":"2.0","method":"boom"}`); err != nil {
		t.Fatal(err)
	}
	if err := peer.WriteLine(`{"jsonrpc":"2.0","id":1,"method":"boom"}`); err != nil {
		t.Fatal(err)
	}
	if code, _ := errCode(t, response(t, peer)); code != jsonrpc.CodeInternalError {
		t.Fatalf("code = %d", code)
	}
	if err := peer.WriteLine(`{"jsonrpc":"2.0","id":2,"method":"ok"}`); err != nil {
		t.Fatal(err)
	}
	if m := response(t, peer); m.Error != nil || string(m.Result) != `"fine"` {
		t.Fatalf("after a panic: %+v", m)
	}
	if c.Err() != nil {
		t.Fatalf("closed: %v", c.Err())
	}
}
