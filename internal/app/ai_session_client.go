package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// An AI session's own client (ADR, "Phase 7 agentflow's MCP server in a
// session"): the MCP server agentflow passes to a harness opens the App with
// client "ai-session:<id>". Its gate refuses every operation that is not a
// read (policy.NewSessionGate), and aisession.Answer refuses it, so a
// session can never write, start a session or answer an ask through
// agentflow.

// SessionLookup finds AI session id in the agentflow home: its worktree root
// and whether it has ended. OpenSession refuses every session while it is
// nil. Tests replace it.
var SessionLookup func(ctx context.Context, home, id string) (worktree string, ended bool, err error)

// ErrSessionUnknown: OpenSession found no running session with that ID
// whose worktree holds the directory.
var ErrSessionUnknown = errors.New("no running AI session with that ID in this worktree")

// OpenSession opens AI session id's read-only App on the repository in dir.
// It refuses unless the session record exists, has not ended, and its
// worktree contains dir.
func OpenSession(ctx context.Context, dir, id string) (*App, error) {
	if SessionLookup == nil {
		return nil, ErrSessionUnknown
	}
	home, err := config.Home()
	if err != nil {
		return nil, err
	}
	worktree, ended, err := SessionLookup(ctx, home, id)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionUnknown, err)
	}
	if ended || worktree == "" || !within(dir, worktree) {
		return nil, ErrSessionUnknown
	}
	return openSession(ctx, dir, id)
}

// openSession opens the App with session id's read-only client.
func openSession(ctx context.Context, dir, id string) (*App, error) {
	if id == "" || strings.ContainsAny(id, ": \t\r\n/\\") {
		return nil, fmt.Errorf("%q is not an AI session ID", id)
	}
	return open(ctx, dir, policy.SessionClientPrefix+id, "")
}

// within reports whether dir is root or below it, comparing resolved paths
// by component.
func within(dir, root string) bool {
	resolve := func(p string) (string, bool) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", false
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		return filepath.Clean(abs), true
	}
	d, ok1 := resolve(dir)
	r, ok2 := resolve(root)
	if !ok1 || !ok2 {
		return false
	}
	rel, err := filepath.Rel(r, d)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
