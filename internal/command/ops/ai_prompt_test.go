package ops

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/aisession"
)

// syncBuffer is a bytes.Buffer safe for the prompt goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type answered struct {
	ask      string
	decision aisession.Decision
}

type promptHarness struct {
	p       *foregroundPrompt
	in      *io.PipeWriter
	out     *syncBuffer
	answers chan answered
	shown   chan string
	done    chan struct{}
	exited  chan struct{}
}

func startPrompt(t *testing.T) *promptHarness {
	t.Helper()
	r, w := io.Pipe()
	h := &promptHarness{in: w, out: &syncBuffer{}, answers: make(chan answered, 8), shown: make(chan string, 8), done: make(chan struct{}), exited: make(chan struct{})}
	h.p = newForegroundPrompt(r, h.out, io.Discard, func(a aisession.AskRecord, d aisession.Decision) error {
		h.answers <- answered{a.ID, d}
		return nil
	})
	h.p.shown = func(id string) { h.shown <- id }
	go func() {
		defer close(h.exited)
		h.p.run(h.done)
	}()
	t.Cleanup(func() {
		_ = w.Close()
		select {
		case <-h.done:
		default:
			close(h.done)
		}
		select {
		case <-h.exited:
		case <-time.After(2 * time.Second):
			t.Error("the prompt did not stop at the end of the session")
		}
	})
	return h
}

func testAsk(id string, in time.Duration) aisession.AskRecord {
	return aisession.AskRecord{ID: id, Kind: aisession.KindTerminal, Reason: "terminal", CanAllow: true, Line: "make", ExpiresAt: time.Now().Add(in)}
}

func answerItem(id string) aisession.Item {
	b, _ := json.Marshal(map[string]string{"ask": id, "decision": "deny", "by": "person", "handler": "person", "code": "rejected"})
	return aisession.Item{Kind: aisession.ItemAnswer, Data: b}
}

// type writes a line; it gives up after a short wait so a reader that is
// not reading yet does not hang the test.
func (h *promptHarness) typeLine(s string) {
	wrote := make(chan struct{})
	go func() {
		_, _ = h.in.Write([]byte(s))
		close(wrote)
	}()
	select {
	case <-wrote:
	case <-time.After(200 * time.Millisecond):
	}
}

func (h *promptHarness) waitShown(t *testing.T, id string) {
	t.Helper()
	select {
	case got := <-h.shown:
		if got != id {
			t.Fatalf("prompt shown for %s, want %s", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no prompt for %s:\n%s", id, h.out.String())
	}
}

func (h *promptHarness) noAnswer(t *testing.T, why string) {
	t.Helper()
	select {
	case a := <-h.answers:
		t.Fatalf("%s: answered %s with %s", why, a.ask, a.decision)
	case <-time.After(300 * time.Millisecond):
	}
}

func (h *promptHarness) wantAnswer(t *testing.T, id string, d aisession.Decision) {
	t.Helper()
	select {
	case a := <-h.answers:
		if a.ask != id || a.decision != d {
			t.Fatalf("answered %s with %s, want %s with %s", a.ask, a.decision, id, d)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no answer for %s:\n%s", id, h.out.String())
	}
}

// A line entered before a request is shown never answers it.
func TestPromptIgnoresLineEnteredBeforeRequest(t *testing.T) {
	h := startPrompt(t)
	h.typeLine("y\n")
	h.p.onAsk(testAsk("ask_0000000000000001", time.Minute))
	h.waitShown(t, "ask_0000000000000001")
	h.noAnswer(t, "a line entered before the request was shown")
	h.typeLine("y\n")
	h.wantAnswer(t, "ask_0000000000000001", aisession.Allow)
}

// Transcript items wait while a prompt does, and OnItems never blocks.
func TestPromptHoldsItemsWhileWaiting(t *testing.T) {
	h := startPrompt(t)
	h.p.onAsk(testAsk("ask_0000000000000001", time.Minute))
	h.waitShown(t, "ask_0000000000000001")
	returned := make(chan struct{})
	go func() {
		h.p.onItems([]aisession.Item{{Kind: aisession.ItemMessage, Text: "held-message-text\n"}})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("OnItems blocked while a prompt waited")
	}
	if strings.Contains(h.out.String(), "held-message-text") {
		t.Fatalf("an item printed while the prompt waited:\n%s", h.out.String())
	}
	h.typeLine("n\n")
	h.wantAnswer(t, "ask_0000000000000001", aisession.Deny)
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(h.out.String(), "held-message-text") {
		if time.Now().After(deadline) {
			t.Fatalf("the held item never printed:\n%s", h.out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	out := h.out.String()
	if strings.Index(out, "held-message-text") < strings.Index(out, "Allow once? [y/N]") {
		t.Fatalf("held item printed before the prompt:\n%s", out)
	}
}

// A request resolved elsewhere is dismissed: the next one is shown, and a
// line goes only to it.
func TestPromptDismissedWhenResolvedElsewhere(t *testing.T) {
	h := startPrompt(t)
	h.p.onAsk(testAsk("ask_0000000000000001", time.Minute))
	h.p.onAsk(testAsk("ask_0000000000000002", time.Minute))
	h.waitShown(t, "ask_0000000000000001")
	h.p.onItems([]aisession.Item{answerItem("ask_0000000000000001")})
	h.waitShown(t, "ask_0000000000000002")
	h.typeLine("y\n")
	h.wantAnswer(t, "ask_0000000000000002", aisession.Allow)
	h.noAnswer(t, "after the second request")
}

// A request that expires is dismissed even without an answer item.
func TestPromptDismissedAtExpiry(t *testing.T) {
	h := startPrompt(t)
	h.p.onAsk(testAsk("ask_0000000000000001", 500*time.Millisecond))
	h.p.onAsk(testAsk("ask_0000000000000002", time.Minute))
	h.waitShown(t, "ask_0000000000000001")
	h.waitShown(t, "ask_0000000000000002")
	h.typeLine("y\n")
	h.wantAnswer(t, "ask_0000000000000002", aisession.Allow)
}

// End of input rejects, this request and the next.
func TestPromptEndOfInputRejects(t *testing.T) {
	h := startPrompt(t)
	_ = h.in.Close()
	h.p.onAsk(testAsk("ask_0000000000000001", time.Minute))
	h.wantAnswer(t, "ask_0000000000000001", aisession.Deny)
	h.p.onAsk(testAsk("ask_0000000000000002", time.Minute))
	h.wantAnswer(t, "ask_0000000000000002", aisession.Deny)
}

// The end of the session ends a waiting prompt and prints what was held.
func TestPromptEndsWithSession(t *testing.T) {
	h := startPrompt(t)
	h.p.onAsk(testAsk("ask_0000000000000001", time.Minute))
	h.p.onAsk(testAsk("ask_0000000000000002", time.Minute))
	h.waitShown(t, "ask_0000000000000001")
	h.p.onItems([]aisession.Item{{Kind: aisession.ItemMessage, Text: "last-words\n"}})
	close(h.done)
	select {
	case <-h.exited:
	case <-time.After(time.Second):
		t.Fatal("a waiting prompt held up the end of the session")
	}
	if !strings.Contains(h.out.String(), "last-words") {
		t.Fatalf("held item lost at the end:\n%s", h.out.String())
	}
	if strings.Contains(h.out.String(), "ask_0000000000000002") {
		t.Fatalf("a request was prompted after the session ended:\n%s", h.out.String())
	}
	h.noAnswer(t, "after the session ended")
}
