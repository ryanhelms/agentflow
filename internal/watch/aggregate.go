package watch

import (
	"sort"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Key names one aggregation group: a worktree, or a repository's
// repository-wide paths (empty Worktree).
type Key struct {
	Repo     bus.RepoID
	Worktree bus.WorktreeID
}

// KeyOf returns the Key of t.
func KeyOf(t Target) Key { return Key{Repo: t.Repo, Worktree: t.Worktree} }

// Aggregator debounces Changes per Key into Batches. A Batch is due once
// its Key has seen no new Change for Quiet, or MaxDelay after its first
// Change, whichever comes first. It does no timing of its own: the caller
// passes the clock, asks Next for the earliest deadline and collects Due
// batches, so it is deterministic under test. It is safe for concurrent
// use.
type Aggregator struct {
	quiet, max time.Duration

	mu      sync.Mutex
	pending map[Key]*pendingBatch
}

type pendingBatch struct {
	batch       Batch
	first, last time.Time
}

// NewAggregator returns an Aggregator; zero durations take Quiet and
// MaxDelay.
func NewAggregator(quiet, maxDelay time.Duration) *Aggregator {
	if quiet <= 0 {
		quiet = Quiet
	}
	if maxDelay <= 0 {
		maxDelay = MaxDelay
	}
	if maxDelay < quiet {
		maxDelay = quiet
	}
	return &Aggregator{quiet: quiet, max: maxDelay, pending: map[Key]*pendingBatch{}}
}

// Add appends c to the batch of t's Key, received at now.
func (a *Aggregator) Add(t Target, c Change, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := KeyOf(t)
	p, ok := a.pending[k]
	if !ok {
		p = &pendingBatch{batch: Batch{Target: t}, first: now}
		a.pending[k] = p
	}
	p.batch.Target = t
	p.batch.Changes = append(p.batch.Changes, c)
	p.last = now
}

// Drop discards the pending batch of k.
func (a *Aggregator) Drop(k Key) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pending, k)
}

// Next returns the earliest time a pending batch becomes due.
func (a *Aggregator) Next() (time.Time, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var next time.Time
	for _, p := range a.pending {
		if d := p.due(a.quiet, a.max); next.IsZero() || d.Before(next) {
			next = d
		}
	}
	return next, !next.IsZero()
}

// Due removes and returns every batch due at now, oldest first.
func (a *Aggregator) Due(now time.Time) []Batch {
	a.mu.Lock()
	defer a.mu.Unlock()
	var due []*pendingBatch
	for k, p := range a.pending {
		if !now.Before(p.due(a.quiet, a.max)) {
			due = append(due, p)
			delete(a.pending, k)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].first.Before(due[j].first) })
	out := make([]Batch, len(due))
	for i, p := range due {
		out[i] = p.batch
	}
	return out
}

func (p *pendingBatch) due(quiet, maxDelay time.Duration) time.Time {
	q, m := p.last.Add(quiet), p.first.Add(maxDelay)
	if m.Before(q) {
		return m
	}
	return q
}
