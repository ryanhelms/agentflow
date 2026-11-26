// Package bustest provides an in-memory bus.Bus and bus.DeadLetter for
// tests. The fake follows the bus contract (normalization, validation,
// interceptors, taps, topic filtering and the drop-oldest overflow rule) but
// favours simplicity over throughput.
package bustest

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// Bus is an in-memory bus.Bus.
type Bus struct {
	opts bus.Options

	pub    sync.Mutex // serializes Publish so delivery order matches chain order
	mu     sync.Mutex
	subs   map[*subscription]struct{}
	queues map[string]*queue
	closed bool
}

var _ bus.Bus = (*Bus)(nil)

// New returns a fake bus. Zero Options fields take the bus defaults.
func New(opts bus.Options) *Bus {
	if opts.Buffer <= 0 {
		opts.Buffer = bus.DefaultBuffer
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.DeadLetter == nil {
		opts.DeadLetter = &DeadLetters{}
	}
	return &Bus{opts: opts, subs: map[*subscription]struct{}{}, queues: map[string]*queue{}}
}

// DeadLetter returns the bus's Dead Letter Channel.
func (b *Bus) DeadLetter() bus.DeadLetter { return b.opts.DeadLetter }

// Publish implements bus.Publisher.
func (b *Bus) Publish(ctx context.Context, e bus.Event) (bus.Event, error) {
	b.pub.Lock()
	defer b.pub.Unlock()
	if b.isClosed() {
		return bus.Event{}, bus.ErrClosed
	}
	if e.Seq != 0 {
		return bus.Event{}, bus.ErrSequenced
	}
	e = e.Normalize(b.opts.Now())
	if err := e.Validate(); err != nil {
		return bus.Event{}, err
	}
	for _, i := range b.opts.Interceptors {
		var err error
		if e, err = i.Intercept(ctx, e); err != nil {
			return bus.Event{}, err
		}
	}
	for _, t := range b.opts.Taps {
		t.Tap(ctx, e)
	}
	b.mu.Lock()
	targets := make([]*subscription, 0, len(b.subs))
	for s := range b.subs {
		if bus.MatchAny(s.topics, e) {
			targets = append(targets, s)
		}
	}
	b.mu.Unlock()
	for _, s := range targets {
		s.push(e, b.opts.Now())
	}
	return e, nil
}

// Subscribe implements bus.Subscriber.
func (b *Bus) Subscribe(opts bus.SubscribeOptions, topics ...bus.Topic) (bus.Subscription, error) {
	if len(topics) == 0 {
		return nil, bus.ErrNoTopics
	}
	if opts.Buffer <= 0 {
		opts.Buffer = b.opts.Buffer
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, bus.ErrClosed
	}
	s := newSubscription(opts, append([]bus.Topic(nil), topics...), func(s *subscription) {
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
	})
	b.subs[s] = struct{}{}
	return s, nil
}

// Queue implements bus.Bus.
func (b *Bus) Queue(name string) bus.Queue {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.queues[name]
	if q == nil {
		q = &queue{ch: make(chan bus.Event, bus.DefaultQueueCapacity), done: make(chan struct{}), now: b.opts.Now}
		if b.closed {
			close(q.done)
		}
		b.queues[name] = q
	}
	return q
}

// Close implements bus.Bus.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return bus.ErrClosed
	}
	b.closed = true
	subs := make([]*subscription, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	for _, q := range b.queues {
		close(q.done)
	}
	b.mu.Unlock()
	for _, s := range subs {
		_ = s.Close()
	}
	return nil
}

func (b *Bus) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// subscription holds a bounded slice and a pump goroutine that feeds the
// unbuffered Events channel, so the overflow marker can be updated in place.
type subscription struct {
	name   string
	topics []bus.Topic
	limit  int
	out    chan bus.Event
	remove func(*subscription)

	mu       sync.Mutex
	cond     *sync.Cond
	buf      []bus.Event
	marker   bool   // buf[0] is the pending stream.overflow event
	pending  uint64 // drops since the subscriber last received an overflow event
	dropped  uint64
	closed   bool
	finished chan struct{}
}

func newSubscription(opts bus.SubscribeOptions, topics []bus.Topic, remove func(*subscription)) *subscription {
	s := &subscription{name: opts.Name, topics: topics, limit: opts.Buffer, out: make(chan bus.Event), remove: remove, finished: make(chan struct{})}
	s.cond = sync.NewCond(&s.mu)
	go s.pump()
	return s
}

func (s *subscription) push(e bus.Event, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	first := 0
	if s.marker {
		first = 1 // the overflow marker is never dropped and does not count
	}
	if len(s.buf)-first >= s.limit {
		s.buf = append(s.buf[:first], s.buf[first+1:]...)
		s.dropped++
		s.pending++
		payload, _ := json.Marshal(bus.StreamOverflow{Subscription: s.name, Dropped: s.pending})
		marker := bus.Event{Type: bus.TypeStreamOverflow, Source: "bus", Payload: payload}.Normalize(now)
		if s.marker {
			s.buf[0] = marker
		} else {
			s.buf = append([]bus.Event{marker}, s.buf...)
			s.marker = true
		}
	}
	s.buf = append(s.buf, e)
	s.cond.Signal()
}

func (s *subscription) pump() {
	defer close(s.out)
	for {
		s.mu.Lock()
		for len(s.buf) == 0 && !s.closed {
			s.cond.Wait()
		}
		if s.closed {
			s.mu.Unlock()
			return
		}
		e := s.buf[0]
		s.buf = s.buf[1:]
		if s.marker {
			s.marker = false
			s.pending = 0
		}
		s.mu.Unlock()
		select {
		case s.out <- e:
		case <-s.finished:
			return
		}
	}
}

func (s *subscription) Events() <-chan bus.Event { return s.out }

func (s *subscription) Dropped() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

func (s *subscription) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.finished)
	s.cond.Broadcast()
	s.mu.Unlock()
	s.remove(s)
	return nil
}

type queue struct {
	ch   chan bus.Event
	done chan struct{}
	now  func() time.Time
}

func (q *queue) Send(ctx context.Context, e bus.Event) error {
	e = e.Normalize(q.now())
	if err := e.Validate(); err != nil {
		return err
	}
	select {
	case <-q.done:
		return bus.ErrClosed
	default:
	}
	select {
	case q.ch <- e:
		return nil
	case <-q.done:
		return bus.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *queue) Receive(ctx context.Context) (bus.Event, error) {
	select {
	case e := <-q.ch:
		return e, nil
	case <-q.done:
		return bus.Event{}, bus.ErrClosed
	case <-ctx.Done():
		return bus.Event{}, ctx.Err()
	}
}

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
