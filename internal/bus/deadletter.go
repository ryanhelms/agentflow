package bus

import (
	"context"
	"sync"
)

// NewDeadLetter returns an in-memory Dead Letter Channel that keeps the
// last keep letters (DeadLetterKeep when keep <= 0). It is the default of
// New and is safe for concurrent use.
func NewDeadLetter(keep int) DeadLetter {
	if keep <= 0 {
		keep = DeadLetterKeep
	}
	return &memoryDeadLetter{ring: make([]Letter, keep)}
}

type memoryDeadLetter struct {
	mu      sync.Mutex
	ring    []Letter
	head, n int
}

// Dead implements DeadLetter. When full it forgets the oldest letter.
func (d *memoryDeadLetter) Dead(_ context.Context, l Letter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.n == len(d.ring) {
		d.ring[d.head] = l
		d.head = (d.head + 1) % len(d.ring)
		return
	}
	d.ring[(d.head+d.n)%len(d.ring)] = l
	d.n++
}

// Letters implements DeadLetter, oldest first.
func (d *memoryDeadLetter) Letters() []Letter {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Letter, d.n)
	for i := range out {
		out[i] = d.ring[(d.head+i)%len(d.ring)]
	}
	return out
}
