package webui

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/bus"
)

// Test hooks for the external AI panel tests (package webui_test), which
// import internal/command/ops so the real ai.respond and ai.cancel Specs
// are registered. This package cannot import ops itself: ops imports it.

// AIConsoleForTest is a console for one worktree. Post sends a request as
// the page does, with a valid session, origin and request header.
type AIConsoleForTest struct{ s *server }

func NewAIConsoleForTest(dir string) AIConsoleForTest {
	aiLimits.mu.Lock()
	aiLimits.seen = nil
	aiLimits.mu.Unlock()
	return AIConsoleForTest{s: aiServer(dir)}
}

func (c AIConsoleForTest) Post(t *testing.T, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return harnessRequest(t, c.s, path, body, headers)
}

// SetAINowForTest fixes the clock of the routes' rate limit.
func SetAINowForTest(t *testing.T, now *time.Time) {
	old := aiNow
	aiNow = func() time.Time { return *now }
	t.Cleanup(func() { aiNow = old })
}

// AIRuntimeForTest is a daemon event runtime serving one worktree, with the
// ai projection reading store.
type AIRuntimeForTest struct {
	WorktreeID string
	RepoID     string
	rt         *eventRuntime
	l          *listener
}

func StartAIRuntimeForTest(t *testing.T, st aisession.Store, dir string) *AIRuntimeForTest {
	t.Helper()
	oldOpts := newAIOptions
	newAIOptions = func() AIOptions { return AIOptions{Store: st} }
	t.Cleanup(func() { newAIOptions = oldOpts })
	ctx := context.Background()
	w, _, err := inspectWorktree(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	rt, stop, err := startEventRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	repo, err := rt.attach(w)
	if err != nil {
		t.Fatal(err)
	}
	l, unlisten := repo.projector.listen()
	t.Cleanup(unlisten)
	return &AIRuntimeForTest{WorktreeID: w.ID, RepoID: string(repo.id), rt: rt, l: l}
}

// Emit publishes e on the runtime's bus, as the daemon's socket would.
func (r *AIRuntimeForTest) Emit(ctx context.Context, e bus.Event) error {
	_, err := r.rt.bus.Publish(ctx, e)
	return err
}

// WaitState waits up to 30 s for an ai state that satisfies ok.
func (r *AIRuntimeForTest) WaitState(t *testing.T, ok func(AIState) bool) AIState {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case up := <-r.l.ch:
			for _, s := range up.states {
				var st AIState
				if s.Projection == ProjectionAI && json.Unmarshal(s.State, &st) == nil && ok(st) {
					return st
				}
			}
		case <-deadline:
			t.Fatal("the ai projection never showed the expected state")
			return AIState{}
		}
	}
}

// AIAskRule is the ask ID format the routes accept.
var AIAskRule = aiAskRule
