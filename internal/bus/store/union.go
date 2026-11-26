package store

import (
	"context"
	"errors"
	"iter"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// ErrReplayLimit: more than MaxReplay events are missed. The stream sends
// resync and a snapshot instead of replaying them.
var ErrReplayLimit = errors.New("more missed events than a stream replays")

// Replay returns the events from has not seen, oldest first, when there are
// at most MaxReplay of them. It returns ErrReplayLimit when there are more,
// and ErrCursorExpired when from points before the retained events.
func Replay(ctx context.Context, r Reader, from bus.Cursor) ([]bus.Event, error) {
	var out []bus.Event
	for e, err := range r.ReadFrom(ctx, from) {
		if err != nil {
			return nil, err
		}
		if len(out) == MaxReplay {
			return nil, ErrReplayLimit
		}
		out = append(out, e)
	}
	return out, nil
}

// Union returns a Follower over the stores of several repositories, as
// `agentflow events` reads every repository the daemon serves. ReadFrom
// merges them in (Time, Repo, Seq) order. If the cursor has expired for any
// of them, the first yield is ErrCursorExpired. Follow polls the members.
func Union(readers ...Reader) Follower {
	return &union{readers: readers, poll: followPoll}
}

type union struct {
	readers []Reader
	poll    time.Duration
}

// Head implements Reader.
func (u *union) Head(ctx context.Context) (bus.Cursor, error) {
	c := bus.Cursor{}
	for _, r := range u.readers {
		h, err := r.Head(ctx)
		if err != nil {
			return nil, err
		}
		for repo, seq := range h {
			if seq > c[repo] {
				c[repo] = seq
			}
		}
	}
	return c, nil
}

// before orders events across repositories by (Time, Repo, Seq).
func before(a, b bus.Event) bool {
	if !a.Time.Equal(b.Time) {
		return a.Time.Before(b.Time)
	}
	if a.Repo != b.Repo {
		return a.Repo < b.Repo
	}
	return a.Seq < b.Seq
}

// ReadFrom implements Reader.
func (u *union) ReadFrom(ctx context.Context, from bus.Cursor) iter.Seq2[bus.Event, error] {
	return func(yield func(bus.Event, error) bool) {
		type head struct {
			next func() (bus.Event, error, bool)
			e    bus.Event
			ok   bool
		}
		heads := make([]*head, 0, len(u.readers))
		stops := make([]func(), 0, len(u.readers))
		defer func() {
			for _, stop := range stops {
				stop()
			}
		}()
		for _, r := range u.readers {
			next, stop := iter.Pull2(r.ReadFrom(ctx, from))
			stops = append(stops, stop)
			heads = append(heads, &head{next: next})
		}
		// Pull every member's first event before yielding anything, so an
		// expired cursor is reported first.
		pull := func(h *head) bool {
			e, err, ok := h.next()
			if ok && err != nil {
				yield(bus.Event{}, err)
				return false
			}
			h.e, h.ok = e, ok
			return true
		}
		for _, h := range heads {
			if !pull(h) {
				return
			}
		}
		for {
			var least *head
			for _, h := range heads {
				if h.ok && (least == nil || before(h.e, least.e)) {
					least = h
				}
			}
			if least == nil {
				return
			}
			if !yield(least.e, nil) || !pull(least) {
				return
			}
		}
	}
}

// Follow implements Follower. Order is (Time, Repo, Seq) within each poll.
func (u *union) Follow(ctx context.Context, from bus.Cursor) iter.Seq2[bus.Event, error] {
	return func(yield func(bus.Event, error) bool) {
		cursor := from.Clone()
		for {
			for e, err := range u.ReadFrom(ctx, cursor) {
				if err != nil {
					yield(bus.Event{}, err)
					return
				}
				if !yield(e, nil) {
					return
				}
				cursor[e.Repo] = max(cursor[e.Repo], e.Seq)
			}
			timer := time.NewTimer(u.poll)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
