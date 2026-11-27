package jsonrpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

// slowCounter counts notifications, taking a little time over each.
func slowCounter(n *atomic.Int64) jsonrpc.Handler {
	return jsonrpc.HandlerFunc(func(_ context.Context, r *jsonrpc.Request) (any, error) {
		if r.Notification() {
			time.Sleep(200 * time.Microsecond)
			n.Add(1)
		}
		return nil, nil
	})
}

// burst sends updates notifications, then answers the first request.
func burst(t *testing.T, peer *jsonrpctest.Peer, updates int) {
	t.Helper()
	line, err := peer.ReadLine() // the Call's request
	if err != nil {
		t.Error(err)
		return
	}
	var m jsonrpc.Message
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Error(err)
		return
	}
	for i := 0; i < updates; i++ {
		if err := peer.WriteLine(fmt.Sprintf(`{"jsonrpc":"2.0","method":"update","params":{"n":%d}}`, i)); err != nil {
			t.Error(err)
			return
		}
	}
	if err := peer.WriteLine(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{}}`, m.ID)); err != nil {
		t.Error(err)
	}
}

func TestOrderedResponsesWaitForEarlierNotifications(t *testing.T) {
	const updates = 300 // more than the notification queue holds
	var handled atomic.Int64
	c, peer := jsonrpctest.Wire(t, slowCounter(&handled), jsonrpc.Limits{OrderedResponses: true})
	go burst(t, peer, updates)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Call(ctx, "prompt", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := handled.Load(); got != updates {
		t.Fatalf("Call returned after %d of %d notifications were handled", got, updates)
	}
}

func TestOrderedResponsesStillRefuseUnknownIDs(t *testing.T) {
	c, peer := jsonrpctest.Wire(t, nil, jsonrpc.Limits{OrderedResponses: true})
	if err := peer.WriteLine(`{"jsonrpc":"2.0","id":7,"result":{}}`); err != nil {
		t.Fatal(err)
	}
	if err := waitDone(t, c); !errors.Is(err, jsonrpc.ErrProtocol) {
		t.Fatalf("Err = %v, want ErrProtocol", err)
	}
}

func TestOrderedResponsesWithoutHandler(t *testing.T) {
	c, peer := jsonrpctest.Wire(t, nil, jsonrpc.Limits{OrderedResponses: true})
	go burst(t, peer, 5)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Call(ctx, "m", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOrderedResponsesHandleQueueBeforeEOF(t *testing.T) {
	// The peer answers, sends more notifications and exits at once: the
	// Call still gets its answer, and every notification is handled before
	// the connection reports EOF.
	const updates = 100
	var handled atomic.Int64
	c, peer := jsonrpctest.Wire(t, slowCounter(&handled), jsonrpc.Limits{OrderedResponses: true})
	go func() {
		burst(t, peer, updates)
		for i := 0; i < updates; i++ {
			_ = peer.WriteLine(`{"jsonrpc":"2.0","method":"late"}`)
		}
		_ = peer.Out.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Call(ctx, "prompt", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := waitDone(t, c); !errors.Is(err, io.EOF) {
		t.Fatalf("Err = %v, want io.EOF", err)
	}
	if got := handled.Load(); got != 2*updates {
		t.Fatalf("%d of %d notifications handled before EOF", got, 2*updates)
	}
}
