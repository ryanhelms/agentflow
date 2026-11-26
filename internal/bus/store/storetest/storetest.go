// Package storetest provides an in-memory store.Store and store.Follower
// for tests.
package storetest

import (
	"context"
	"iter"
	"sync"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
)

// Store is an in-memory store.Store for one repository.
type Store struct {
	repo bus.RepoID

	mu      sync.Mutex
	events  []bus.Event // retained events, Seq ascending
	next    uint64      // next Seq to assign
	changed chan struct{}
	closed  bool
}

var (
	_ store.Store    = (*Store)(nil)
	_ store.Follower = (*Store)(nil)
)

// New returns an empty store for repo.
func New(repo bus.RepoID) *Store {
	return &Store{repo: repo, next: 1, changed: make(chan struct{})}
}

// Repo implements store.Store.
func (s *Store) Repo() bus.RepoID { return s.repo }

// Append implements store.Store.
func (s *Store) Append(_ context.Context, e bus.Event) (bus.Event, error) {
	if info, ok := bus.Lookup(e.Type); ok && info.Transient {
		return bus.Event{}, store.ErrTransient
	}
	if e.Repo != s.repo {
		return bus.Event{}, store.ErrWrongRepo
	}
	if e.Seq != 0 {
		return bus.Event{}, bus.ErrSequenced
	}
	if err := e.Validate(); err != nil {
		return bus.Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return bus.Event{}, bus.ErrClosed
	}
	e.Seq = s.next
	s.next++
	s.events = append(s.events, e)
	close(s.changed)
	s.changed = make(chan struct{})
	return e, nil
}

// Expire drops every event with Seq at or below seq, as retention would.
func (s *Store) Expire(seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := 0
	for i < len(s.events) && s.events[i].Seq <= seq {
		i++
	}
	s.events = append([]bus.Event(nil), s.events[i:]...)
}

// Head implements store.Reader.
func (s *Store) Head(context.Context) (bus.Cursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := bus.Cursor{}
	if s.next > 1 {
		c[s.repo] = s.next - 1
	}
	return c, nil
}

// after returns the events after seen, whether seen has expired, and a
// channel closed on the next Append.
func (s *Store) after(seen uint64) ([]bus.Event, bool, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The oldest retained Seq is events[0].Seq, or next when everything
	// has expired. A reader that has seen something must have seen
	// everything before it; a reader that has seen nothing starts there.
	oldest := s.next
	if len(s.events) > 0 {
		oldest = s.events[0].Seq
	}
	if seen > 0 && seen+1 < oldest {
		return nil, true, s.changed
	}
	var out []bus.Event
	for _, e := range s.events {
		if e.Seq > seen {
			out = append(out, e)
		}
	}
	return out, false, s.changed
}

// ReadFrom implements store.Reader.
func (s *Store) ReadFrom(ctx context.Context, from bus.Cursor) iter.Seq2[bus.Event, error] {
	return s.read(ctx, from, false)
}

// Follow implements store.Follower.
func (s *Store) Follow(ctx context.Context, from bus.Cursor) iter.Seq2[bus.Event, error] {
	return s.read(ctx, from, true)
}

func (s *Store) read(ctx context.Context, from bus.Cursor, follow bool) iter.Seq2[bus.Event, error] {
	return func(yield func(bus.Event, error) bool) {
		seen := from[s.repo]
		for {
			events, expired, changed := s.after(seen)
			if expired {
				yield(bus.Event{}, store.ErrCursorExpired)
				return
			}
			for _, e := range events {
				if err := ctx.Err(); err != nil {
					yield(bus.Event{}, err)
					return
				}
				if !yield(e, nil) {
					return
				}
				seen = e.Seq
			}
			if !follow {
				return
			}
			select {
			case <-changed:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Close implements store.Store.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
