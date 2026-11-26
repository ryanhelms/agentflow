package webui

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// keyLister is implemented by projections that can list every key they
// hold, which a snapshot needs.
type keyLister interface {
	AllKeys() []ProjectionKey
}

// streamUpdate is what the projector hands each stream: one event and the
// states it may have changed, or a resync order after the projector's own
// subscription overflowed.
type streamUpdate struct {
	event  bus.Event
	states []ProjectionState
	resync bool
}

// listenerBuffer bounds a stream's queue of updates. A slow stream loses
// updates and resynchronizes rather than holding up the projector.
const listenerBuffer = bus.DefaultBuffer

// maxBatch caps how many waiting events one projector pass coalesces.
const maxBatch = 64

type listener struct {
	ch   chan streamUpdate
	lost chan struct{} // capacity 1: an update was dropped since the last resync
}

// projector is one repository's projection engine. It subscribes to the bus
// once, recomputes each affected key once per batch of waiting events, and
// fans the result out to every stream of that repository. Each stream keeps
// the last state it sent per key and drops unchanged ones, so a client only
// receives a state when its JSON changed.
type projector struct {
	repo        bus.RepoID
	projections []Projection

	mu        sync.Mutex
	listeners map[*listener]struct{}
}

func newProjector(repo bus.RepoID, projections []Projection) *projector {
	return &projector{repo: repo, projections: projections, listeners: map[*listener]struct{}{}}
}

// listen registers a stream. Updates published after listen returns reach it.
func (p *projector) listen() (*listener, func()) {
	l := &listener{ch: make(chan streamUpdate, listenerBuffer), lost: make(chan struct{}, 1)}
	p.mu.Lock()
	p.listeners[l] = struct{}{}
	p.mu.Unlock()
	return l, func() {
		p.mu.Lock()
		delete(p.listeners, l)
		p.mu.Unlock()
	}
}

func (p *projector) broadcast(u streamUpdate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for l := range p.listeners {
		select {
		case l.ch <- u:
		default:
			select {
			case l.lost <- struct{}{}:
			default:
			}
		}
	}
}

func (p *projector) run(ctx context.Context, sub bus.Subscription) {
	defer sub.Close()
	for {
		var first bus.Event
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub.Events():
			if !ok {
				return
			}
			first = e
		}
		batch := []bus.Event{first}
	drain:
		for len(batch) < maxBatch {
			select {
			case e, ok := <-sub.Events():
				if !ok {
					break drain
				}
				batch = append(batch, e)
			default:
				break drain
			}
		}
		p.process(ctx, batch)
	}
}

type projectionSlot struct {
	projection int
	key        ProjectionKey
}

// process computes each affected state once and attaches it to the last
// event of the batch that touched its key. A stream.overflow marker means
// the projector missed events: every stream resynchronizes. Back-to-back
// markers produce one resync.
func (p *projector) process(ctx context.Context, batch []bus.Event) {
	last := map[projectionSlot]int{}
	var order []projectionSlot
	for i, e := range batch {
		if e.Type == bus.TypeStreamOverflow {
			continue
		}
		for pi, proj := range p.projections {
			for _, k := range proj.Keys(e) {
				slot := projectionSlot{pi, k}
				if _, seen := last[slot]; !seen {
					order = append(order, slot)
				}
				last[slot] = i
			}
		}
	}
	states := map[int][]ProjectionState{}
	for _, slot := range order {
		proj := p.projections[slot.projection]
		raw, err := proj.State(ctx, slot.key)
		if err != nil {
			continue // a later event or a snapshot repairs it
		}
		i := last[slot]
		states[i] = append(states[i], ProjectionState{Projection: proj.Name(), Repo: slot.key.Repo, Worktree: slot.key.Worktree, State: raw})
	}
	resyncSent := false
	for i, e := range batch {
		if e.Type == bus.TypeStreamOverflow {
			if !resyncSent {
				p.broadcast(streamUpdate{resync: true})
				resyncSent = true
			}
			continue
		}
		resyncSent = false
		p.broadcast(streamUpdate{event: e, states: states[i]})
	}
}

// snapshot computes every state selected by sel, fresh from git and
// configuration.
func (p *projector) snapshot(ctx context.Context, sel func(Projection, ProjectionKey) bool) []ProjectionState {
	out := []ProjectionState{}
	for _, proj := range p.projections {
		lister, ok := proj.(keyLister)
		if !ok {
			continue
		}
		for _, k := range lister.AllKeys() {
			if !sel(proj, k) {
				continue
			}
			raw, err := proj.State(ctx, k)
			if err != nil {
				continue
			}
			out = append(out, ProjectionState{Projection: proj.Name(), Repo: k.Repo, Worktree: k.Worktree, State: raw})
		}
	}
	return out
}

// selects reports whether a stream with topics receives the state of key in
// proj: some topic covers the key's worktree and an event type that can
// change proj.
func selects(topics []bus.Topic, proj Projection, key ProjectionKey) bool {
	for _, t := range topics {
		if t.Repo != bus.Any && t.Repo != key.Repo {
			continue
		}
		if t.Worktree != bus.Any && key.Worktree != "" && t.Worktree != key.Worktree {
			continue
		}
		for _, info := range bus.Types() {
			if !t.Types.Match(info.Type) {
				continue
			}
			for _, pt := range proj.Topics() {
				if pt.Types.Match(info.Type) {
					return true
				}
			}
		}
	}
	return false
}

var jsonNull = json.RawMessage("null")
