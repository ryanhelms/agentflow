// Package bustest provides a bus.Bus and a bus.DeadLetter for tests.
//
// New wraps the production bus (bus.New), so tests exercise the real
// normalization, validation, interceptor, tap, topic and overflow rules, and
// adds DeadLetter for direct access to the Dead Letter Channel.
package bustest

import (
	"context"
	"sync"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Bus is a bus.Bus for tests.
type Bus struct {
	bus.Bus
	dl bus.DeadLetter
}

var _ bus.Bus = (*Bus)(nil)

// New returns a test bus. Zero Options fields take the bus defaults, except
// that a nil DeadLetter becomes a *DeadLetters.
func New(opts bus.Options) *Bus {
	if opts.DeadLetter == nil {
		opts.DeadLetter = &DeadLetters{}
	}
	return &Bus{Bus: bus.New(opts), dl: opts.DeadLetter}
}

// DeadLetter returns the bus's Dead Letter Channel.
func (b *Bus) DeadLetter() bus.DeadLetter { return b.dl }

// DeadLetters is an in-memory bus.DeadLetter keeping the last
// bus.DeadLetterKeep letters.
type DeadLetters struct {
	mu      sync.Mutex
	letters []bus.Letter
}

var _ bus.DeadLetter = (*DeadLetters)(nil)

// Dead implements bus.DeadLetter.
func (d *DeadLetters) Dead(_ context.Context, l bus.Letter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.letters = append(d.letters, l)
	if over := len(d.letters) - bus.DeadLetterKeep; over > 0 {
		d.letters = append([]bus.Letter(nil), d.letters[over:]...)
	}
}

// Letters implements bus.DeadLetter.
func (d *DeadLetters) Letters() []bus.Letter {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]bus.Letter(nil), d.letters...)
}
