// Package watchtest provides a fake watch.Source that tests drive by hand.
package watchtest

import (
	"fmt"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/watch"
)

// Source is a fake watch.Source. Emit delivers a Change; Fail makes Add
// reject a directory with watch.ErrUnsupported.
type Source struct {
	mode    watch.Mode
	changes chan watch.Change
	errs    chan error

	mu     sync.Mutex
	dirs   map[string]bool
	fail   map[string]bool
	closed bool
}

var _ watch.Source = (*Source)(nil)

// New returns a fake Source reporting mode.
func New(mode watch.Mode) *Source {
	return &Source{mode: mode, changes: make(chan watch.Change, 64), errs: make(chan error, 8),
		dirs: map[string]bool{}, fail: map[string]bool{}}
}

// Mode implements watch.Source.
func (s *Source) Mode() watch.Mode { return s.mode }

// Fail makes later Add calls for dir fail with watch.ErrUnsupported.
func (s *Source) Fail(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[dir] = true
}

// Add implements watch.Source.
func (s *Source) Add(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail[dir] {
		return fmt.Errorf("add %s: %w", dir, watch.ErrUnsupported)
	}
	s.dirs[dir] = true
	return nil
}

// Remove implements watch.Source.
func (s *Source) Remove(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.dirs, dir)
	return nil
}

// Watching reports whether dir has been added and not removed.
func (s *Source) Watching(dir string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirs[dir]
}

// Emit delivers a change, stamped with the current time.
func (s *Source) Emit(path string, kind watch.Kind) {
	s.changes <- watch.Change{Path: path, Kind: kind, Time: time.Now()}
}

// EmitError delivers a non-fatal error.
func (s *Source) EmitError(err error) { s.errs <- err }

// Changes implements watch.Source.
func (s *Source) Changes() <-chan watch.Change { return s.changes }

// Errors implements watch.Source.
func (s *Source) Errors() <-chan error { return s.errs }

// Close implements watch.Source.
func (s *Source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.changes)
		close(s.errs)
	}
	return nil
}
