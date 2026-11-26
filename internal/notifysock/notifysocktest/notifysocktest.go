// Package notifysocktest holds fakes for the notify socket contract: a
// Sender that records events, an Acceptor that applies the daemon's checks,
// and an Appender over a fixed result.
package notifysocktest

import (
	"context"
	"fmt"
	"sync"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/notifysock"
)

// Sender records sent events. When Err is set, Send returns it instead.
type Sender struct {
	mu     sync.Mutex
	Err    error
	events []bus.Event
}

// Send implements notifysock.Sender.
func (s *Sender) Send(ctx context.Context, e bus.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	s.events = append(s.events, e)
	return nil
}

// Events returns a copy of the sent events.
func (s *Sender) Events() []bus.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bus.Event(nil), s.events...)
}

// Acceptor applies notifysock.Check and an ownership check, then hands the
// event to Publisher, like the daemon's Acceptor.
type Acceptor struct {
	Publisher bus.Publisher
	// Owns reports whether the daemon owns repo and registered worktree.
	// Nil accepts every repository and worktree.
	Owns func(repo bus.RepoID, worktree bus.WorktreeID) (repoOK, worktreeOK bool)
}

// Accept implements notifysock.Acceptor. A refusal wraps
// notifysock.ErrRejected and names the FrameError code.
func (a *Acceptor) Accept(ctx context.Context, e bus.Event) (bus.Event, error) {
	if code := notifysock.Check(e); code != "" {
		return bus.Event{}, fmt.Errorf("%w: %s", notifysock.ErrRejected, code)
	}
	if a.Owns != nil {
		repoOK, worktreeOK := a.Owns(e.Repo, e.Worktree)
		switch {
		case !repoOK:
			return bus.Event{}, fmt.Errorf("%w: %s", notifysock.ErrRejected, notifysock.ErrorUnknownRepo)
		case e.Worktree != "" && !worktreeOK:
			return bus.Event{}, fmt.Errorf("%w: %s", notifysock.ErrRejected, notifysock.ErrorUnknownWorktree)
		}
	}
	return a.Publisher.Publish(ctx, e)
}

// Appender returns Err from every Append, or the event when Err is nil.
type Appender struct {
	mu     sync.Mutex
	Err    error
	events []bus.Event
}

// Append implements notifysock.Appender.
func (a *Appender) Append(_ context.Context, e bus.Event) (bus.Event, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Err != nil {
		return bus.Event{}, a.Err
	}
	e.Seq = uint64(len(a.events) + 1)
	a.events = append(a.events, e)
	return e, nil
}

// Events returns a copy of the appended events.
func (a *Appender) Events() []bus.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]bus.Event(nil), a.events...)
}
