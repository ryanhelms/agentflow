package jsonrpc_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

// answerAndExit sends 6 notifications, answers the first request and exits
// at once, as an ACP agent may after answering session/prompt.
func answerAndExit([]string) int {
	done := make(chan struct{})
	var c jsonrpc.Conn
	ready := make(chan struct{})
	h := jsonrpc.HandlerFunc(func(ctx context.Context, r *jsonrpc.Request) (any, error) {
		<-ready
		for i := range 6 {
			if err := c.Notify(ctx, "note", []int{i}); err != nil {
				fmt.Fprintln(os.Stderr, "notify:", err)
			}
		}
		defer close(done)
		return "answered", nil
	})
	c = jsonrpc.NewConn(os.Stdin, os.Stdout, h, jsonrpc.Limits{})
	close(ready)
	<-done
	time.Sleep(50 * time.Millisecond) // the answer is written; exit at once
	return 0
}

func init() { jsonrpctest.RegisterChild("answer-and-exit", answerAndExit) }

// With OrderedResponses, a child that answers and exits while the parent's
// notification handler is still busy for longer than the 500 ms drain delay
// must not lose the answer: Call returns it after every notification.
func TestLaunchOrderedResponsesSurviveExit(t *testing.T) {
	var handled atomic.Int32
	s := jsonrpctest.ChildSpec(t, "answer-and-exit")
	s.Limits = jsonrpc.Limits{OrderedResponses: true, CallTimeout: 20 * time.Second}
	s.Handler = jsonrpc.HandlerFunc(func(ctx context.Context, r *jsonrpc.Request) (any, error) {
		if r.Method == "note" {
			time.Sleep(200 * time.Millisecond) // 1.2 s in all
			handled.Add(1)
		}
		return nil, nil
	})
	p, err := jsonrpc.Launch(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Kill() }()
	var got string
	if err := p.Conn().Call(context.Background(), "go", nil, &got); err != nil {
		t.Fatalf("Call: %v (handled %d notifications)", err, handled.Load())
	}
	if got != "answered" || handled.Load() != 6 {
		t.Fatalf("got %q after %d notifications", got, handled.Load())
	}
	select {
	case <-p.Exited():
	case <-time.After(15 * time.Second):
		t.Fatal("the process did not end")
	}
}
