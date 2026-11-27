package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ryanhelms/agentflow/internal/policy"
)

func stubSessionLookup(t *testing.T, worktree string, ended bool, err error) {
	t.Helper()
	old := SessionLookup
	t.Cleanup(func() { SessionLookup = old })
	SessionLookup = func(context.Context, string, string) (string, bool, error) { return worktree, ended, err }
}

// Only OpenSession makes a session client, and only for a running session
// whose worktree holds the directory. Its gate reads and refuses writes.
func TestOpenSession(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	t.Setenv(SessionEnv, "")
	if _, err := Open(ctx, dir, "ai-session:ais_1"); !errors.Is(err, policy.ErrReservedSessionClient) {
		t.Fatalf("Open with the reserved prefix: %v", err)
	}

	SessionLookup = nil
	if _, err := OpenSession(ctx, dir, "ais_1"); !errors.Is(err, ErrSessionUnknown) {
		t.Fatalf("without a lookup: %v", err)
	}
	stubSessionLookup(t, "", false, errors.New("missing"))
	if _, err := OpenSession(ctx, dir, "ais_1"); !errors.Is(err, ErrSessionUnknown) {
		t.Fatalf("missing record: %v", err)
	}
	stubSessionLookup(t, dir, true, nil)
	if _, err := OpenSession(ctx, dir, "ais_1"); !errors.Is(err, ErrSessionUnknown) {
		t.Fatalf("ended session: %v", err)
	}
	stubSessionLookup(t, filepath.Join(dir, "api"), false, nil)
	if _, err := OpenSession(ctx, dir, "ais_1"); !errors.Is(err, ErrSessionUnknown) {
		t.Fatalf("directory outside the session's worktree: %v", err)
	}
	stubSessionLookup(t, dir, false, nil)
	if _, err := OpenSession(ctx, dir, "ais:1"); err == nil {
		t.Fatal("accepted an invalid ID")
	}
	a, err := OpenSession(ctx, filepath.Join(dir, "api"), "ais_1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Client != "ai-session:ais_1" {
		t.Fatalf("client %q", a.Client)
	}
	if _, err := a.Gate.Check(policy.Local, "commit", map[string]any{"a": 1}, "", false); !errors.Is(err, policy.ErrSessionReadOnly) {
		t.Fatalf("a session write: %v", err)
	}
}

// Every process below a session's harness has SessionEnv set: Open then
// opens the session's read-only client, whatever client the caller names,
// and AgentEnvironment reports an agent.
func TestOpenInsideSessionIsReadOnly(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "AGENTFLOW_CODEX_HOST_ACTIVE"} {
		t.Setenv(name, "")
	}
	t.Setenv(SessionEnv, "ais_2")
	if got := AgentEnvironment(); got != SessionEnv {
		t.Fatal("AgentEnvironment ignores the session variable")
	}
	a, err := Open(ctx, dir, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if a.Client != "ai-session:ais_2" {
		t.Fatalf("client %q", a.Client)
	}
	if _, err := a.Gate.Check(policy.Local, "commit", map[string]any{"a": 1}, "", false); !errors.Is(err, policy.ErrSessionReadOnly) {
		t.Fatalf("a write inside a session: %v", err)
	}
	t.Setenv(SessionEnv, "bad:id")
	if _, err := Open(ctx, dir, "claude"); err == nil {
		t.Fatal("an invalid session ID opened a writable client")
	}
}
