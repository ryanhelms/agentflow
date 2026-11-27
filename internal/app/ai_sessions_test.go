package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/aisession"
)

// startOnly is a Manager that records StartRequests; nothing else is called.
type startOnly struct {
	aisession.Manager
	starts []aisession.StartRequest
}

func (m *startOnly) Prepare(_ context.Context, r aisession.StartRequest) (aisession.Plan, error) {
	return aisession.Plan{Harness: r.Harness, Worktree: r.Dir}, nil
}

func (m *startOnly) Start(_ context.Context, r aisession.StartRequest) (aisession.Session, error) {
	m.starts = append(m.starts, r)
	return aisession.Session{ID: "ais_1", Harness: r.Harness, Worktree: r.Dir, State: aisession.StateStarting, Host: aisession.HostInfo{Mode: aisession.HostDetached}}, nil
}

func (m *startOnly) Get(context.Context, aisession.ID) (aisession.Session, []aisession.AskRecord, error) {
	return aisession.Session{ID: "ais_1", State: aisession.StateWaiting}, []aisession.AskRecord{{ID: "ask_1", Kind: aisession.KindWrite, CanAllow: true}}, nil
}

func (m *startOnly) Items(context.Context, aisession.ID, int64, int) ([]aisession.Item, int64, error) {
	return nil, 0, nil
}

func withManager(t *testing.T, m aisession.Manager) {
	t.Helper()
	old := NewAIManager
	t.Cleanup(func() { NewAIManager = old })
	NewAIManager = func(aisession.Options) aisession.Manager { return m }
}

// A session runs in the App's own worktree: the start request's directory
// is the repository's, never an input.
func TestAIRunUsesTheAppWorktree(t *testing.T) {
	dir := newRepo(t)
	t.Setenv(SessionEnv, "")
	a, err := Open(context.Background(), dir, "cli")
	if err != nil {
		t.Fatal(err)
	}
	m := &startOnly{}
	withManager(t, m)
	res, err := a.AIRun(context.Background(), aisession.Options{}, AIRunRequest{Harness: "fake", Prompt: "x", Detach: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.starts) != 1 || m.starts[0].Dir != a.Repo.Dir || !res.Detached || !strings.Contains(res.Next, "ai status ais_1 --follow") {
		t.Fatalf("starts %+v result %+v", m.starts, res)
	}
}

// Without a Manager nothing runs; a session requester is refused nested
// before the Manager is asked.
func TestAIRunRefusals(t *testing.T) {
	dir := newRepo(t)
	t.Setenv(SessionEnv, "")
	a, err := Open(context.Background(), dir, "cli")
	if err != nil {
		t.Fatal(err)
	}
	withManager(t, nil)
	NewAIManager = nil
	if _, err := a.AIRun(context.Background(), aisession.Options{}, AIRunRequest{Harness: "fake", Prompt: "x"}); !errors.Is(err, ErrNoAISessions) {
		t.Fatalf("no manager: %v", err)
	}
	m := &startOnly{}
	withManager(t, m)
	ctx := aisession.WithRequester(context.Background(), aisession.Requester{Kind: aisession.RequesterSession})
	var refusal *aisession.Refusal
	if _, err := a.AIRun(ctx, aisession.Options{}, AIRunRequest{Harness: "fake", Prompt: "x"}); !errors.As(err, &refusal) || refusal.Code != aisession.RefuseNested {
		t.Fatalf("session requester: %v", err)
	}
	if _, err := a.AIPlan(ctx, aisession.Options{}, AIRunRequest{Harness: "fake", Prompt: "x"}); !errors.As(err, &refusal) || refusal.Code != aisession.RefuseNested {
		t.Fatalf("session plan: %v", err)
	}
	if len(m.starts) != 0 {
		t.Fatal("a session started a session")
	}
}

// ai status shows waiting asks without a write's file text: AIAsk has no
// field for it.
func TestAIStatusAsksCarryNoFileText(t *testing.T) {
	dir := newRepo(t)
	t.Setenv(SessionEnv, "")
	a, err := Open(context.Background(), dir, "cli")
	if err != nil {
		t.Fatal(err)
	}
	withManager(t, &startOnly{})
	out, err := a.AIStatus(context.Background(), aisession.Options{}, "ais_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"ask_1"`) || strings.Contains(string(b), "old_text") || strings.Contains(string(b), "new_text") || strings.Contains(string(b), `"diff"`) {
		t.Fatalf("status %s", b)
	}
	if !strings.Contains(string(b), `"items":[]`) {
		t.Fatalf("no items should be [], got %s", b)
	}
}
