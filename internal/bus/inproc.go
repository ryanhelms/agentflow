package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// overflowSource is the Source of the stream.overflow markers the bus makes.
const overflowSource = "bus"

// New returns the production in-process bus. Zero Options fields take the
// defaults: DefaultBuffer per subscription, time.Now, and an in-memory Dead
// Letter Channel keeping DeadLetterKeep letters (reach it with
// DeadLetterOf).
//
// Publish runs, in order: Normalize, Validate, the Interceptors, the Taps,
// then delivery to every matching subscription. Publish calls are
// serialized, so every subscriber sees one order. Close waits for an
// in-flight Publish to finish, then closes every subscription and queue.
func New(opts Options) Bus {
	if opts.Buffer <= 0 {
		opts.Buffer = DefaultBuffer
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.DeadLetter == nil {
		opts.DeadLetter = NewDeadLetter(DeadLetterKeep)
	}
	b := &inproc{opts: opts, queues: map[string]*queue{}, lastSeq: map[RepoID]uint64{}}
	b.subs.Store(&[]*subscription{})
	return b
}

// DeadLetterOf returns the Dead Letter Channel of a bus made by New (or any
// bus with a DeadLetter method, such as bustest.Bus), so consumers can pass
// it to Handle. It returns nil for other buses.
func DeadLetterOf(b Bus) DeadLetter {
	if d, ok := b.(interface{ DeadLetter() DeadLetter }); ok {
		return d.DeadLetter()
	}
	return nil
}

type inproc struct {
	opts Options

	// pub serializes Publish, so delivery order is chain order. It also
	// guards lastSeq.
	pub     sync.Mutex
	lastSeq map[RepoID]uint64
	closed  atomic.Bool

	// mu guards writes to subs, queues and the closed transition.
	mu     sync.Mutex
	subs   atomic.Pointer[[]*subscription] // copy-on-write, read lock-free by Publish
	queues map[string]*queue
}

var _ Bus = (*inproc)(nil)

func (b *inproc) DeadLetter() DeadLetter { return b.opts.DeadLetter }

// Publish implements Publisher.
func (b *inproc) Publish(ctx context.Context, e Event) (Event, error) {
	if e.Seq != 0 {
		return Event{}, ErrSequenced
	}
	now := b.opts.Now()
	e = e.Normalize(now)
	if err := e.Validate(); err != nil {
		return Event{}, err
	}

	b.pub.Lock()
	defer b.pub.Unlock()
	if b.closed.Load() {
		return Event{}, ErrClosed
	}
	for i, ic := range b.opts.Interceptors {
		var err error
		if e, err = intercept(ctx, ic, e); err != nil {
			return Event{}, fmt.Errorf("interceptor %d: %w", i, err)
		}
	}
	if err := b.checkSeq(e); err != nil {
		return Event{}, err
	}
	for i, t := range b.opts.Taps {
		b.tap(ctx, i, t, e)
	}
	for _, s := range *b.subs.Load() {
		if MatchAny(s.topics, e) {
			s.push(e, now)
		}
	}
	return e, nil
}

// checkSeq enforces the ADR's Seq rules on the event the chain returns:
// transient events stay unstored, and within a repository Seq only rises,
// so delivery order is Seq order. A stored event that breaks the rule is
// not delivered. Caller holds b.pub.
func (b *inproc) checkSeq(e Event) error {
	if e.Seq == 0 {
		return nil
	}
	if info, _ := Lookup(e.Type); info.Transient {
		return fmt.Errorf("%w: transient %s has seq %d", ErrInvalidEnvelope, e.Type, e.Seq)
	}
	if last := b.lastSeq[e.Repo]; e.Seq <= last {
		return fmt.Errorf("%w: seq %d for repository %s after %d", ErrInvalidEnvelope, e.Seq, e.Repo, last)
	}
	b.lastSeq[e.Repo] = e.Seq
	return nil
}

func intercept(ctx context.Context, ic Interceptor, e Event) (out Event, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = Event{}, fmt.Errorf("panicked: %v", r)
		}
	}()
	return ic.Intercept(ctx, e)
}

// tap runs one Wire Tap. A panicking tap cannot stop delivery: the panic
// becomes a dead letter.
func (b *inproc) tap(ctx context.Context, i int, t Tap, e Event) {
	defer func() {
		if r := recover(); r != nil {
			b.opts.DeadLetter.Dead(ctx, Letter{Event: e, Consumer: fmt.Sprintf("tap:%d", i),
				Reason: fmt.Sprintf("tap panicked: %v", r), Time: b.opts.Now().UTC()})
		}
	}()
	t.Tap(ctx, e)
}

// Subscribe implements Subscriber.
func (b *inproc) Subscribe(opts SubscribeOptions, topics ...Topic) (Subscription, error) {
	if len(topics) == 0 {
		return nil, ErrNoTopics
	}
	for _, t := range topics {
		if err := validTopic(t); err != nil {
			return nil, err
		}
	}
	if opts.Buffer <= 0 {
		opts.Buffer = b.opts.Buffer
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed.Load() {
		return nil, ErrClosed
	}
	s := newSubscription(opts, append([]Topic(nil), topics...), b.opts.Now, b.unsubscribe)
	old := *b.subs.Load()
	next := make([]*subscription, len(old), len(old)+1)
	copy(next, old)
	next = append(next, s)
	b.subs.Store(&next)
	return s, nil
}

func validTopic(t Topic) error {
	switch {
	case t.Repo != Any && !t.Repo.Valid(),
		t.Worktree == "", t.Worktree != Any && !t.Worktree.Valid():
		return fmt.Errorf("%w: %q", ErrInvalidTopic, t.String())
	}
	_, err := ParsePattern(string(t.Types))
	return err
}

func (b *inproc) unsubscribe(s *subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	old := *b.subs.Load()
	next := make([]*subscription, 0, len(old))
	for _, o := range old {
		if o != s {
			next = append(next, o)
		}
	}
	b.subs.Store(&next)
}

// Queue implements Bus.
func (b *inproc) Queue(name string) Queue {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.queues[name]
	if q == nil {
		q = &queue{ch: make(chan Event, DefaultQueueCapacity), done: make(chan struct{}), now: b.opts.Now}
		if b.closed.Load() {
			close(q.done)
		}
		b.queues[name] = q
	}
	return q
}

// Close implements Bus. It waits for an in-flight Publish, then closes
// every subscription (discarding buffered events) and every queue, and
// returns once every subscription's goroutine has stopped.
func (b *inproc) Close() error {
	b.mu.Lock()
	if b.closed.Swap(true) {
		b.mu.Unlock()
		return ErrClosed
	}
	for _, q := range b.queues {
		close(q.done)
	}
	b.mu.Unlock()

	// Holding pub waits for an in-flight Publish; any later one sees closed.
	b.pub.Lock()
	defer b.pub.Unlock()
	for _, s := range *b.subs.Load() {
		_ = s.Close()
	}
	return nil
}

// subscription is a bounded ring of events plus a pump goroutine that feeds
// the unbuffered Events channel. The pending stream.overflow marker is kept
// outside the ring, logically in front of it, so it is never dropped and
// its count can rise in place.
type subscription struct {
	name   string
	topics []Topic
	now    func() time.Time
	out    chan Event
	remove func(*subscription)

	dropped atomic.Uint64

	mu         sync.Mutex
	ring       []Event
	head, n    int
	marker     bool      // a stream.overflow marker waits in front of the ring
	markerTime time.Time // when the waiting marker's first drop happened
	pending    uint64    // drops counted by the waiting marker
	offered    uint64    // the count in the marker the pump is offering
	closed     bool

	wake     chan struct{} // capacity 1: the ring gained an event
	finished chan struct{} // closed by Close
	stopped  chan struct{} // closed when the pump returns
}

func newSubscription(opts SubscribeOptions, topics []Topic, now func() time.Time, remove func(*subscription)) *subscription {
	s := &subscription{
		name: opts.Name, topics: topics, now: now, out: make(chan Event), remove: remove,
		ring: make([]Event, opts.Buffer), wake: make(chan struct{}, 1),
		finished: make(chan struct{}), stopped: make(chan struct{}),
	}
	go s.pump()
	return s
}

// push appends e, dropping the oldest buffered event when the ring is full.
// It never blocks on the consumer.
func (s *subscription) push(e Event, now time.Time) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.n == len(s.ring) {
		s.ring[s.head] = Event{} // release the payload
		s.head = (s.head + 1) % len(s.ring)
		s.n--
		s.dropped.Add(1)
		s.pending++
		if !s.marker {
			s.marker, s.markerTime = true, now
		}
	}
	s.ring[(s.head+s.n)%len(s.ring)] = e
	s.n++
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// next takes the event at the front. A waiting overflow marker is returned
// with marker set and stays waiting, so further drops still raise its count
// until the consumer takes it (see pump). Otherwise it pops the oldest
// buffered event.
func (s *subscription) next() (e Event, marker, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.marker {
		e, s.offered = s.overflowEvent(Event{})
		return e, true, true
	}
	if s.n == 0 {
		return Event{}, false, false
	}
	e = s.ring[s.head]
	s.ring[s.head] = Event{}
	s.head = (s.head + 1) % len(s.ring)
	s.n--
	return e, false, true
}

// overflowEvent builds the waiting marker with the current drop count,
// keeping prev's ID when prev is set. Caller holds s.mu.
func (s *subscription) overflowEvent(prev Event) (Event, uint64) {
	payload, _ := json.Marshal(StreamOverflow{Subscription: s.name, Dropped: s.pending})
	e := Event{ID: prev.ID, Type: TypeStreamOverflow, Source: overflowSource, Time: s.markerTime, Payload: payload}
	return e.Normalize(s.now()), s.pending
}

func (s *subscription) refreshMarker(prev Event) Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var e Event
	e, s.offered = s.overflowEvent(prev)
	return e
}

// markerTaken settles the marker the consumer just received. Drops that
// raced the hand-off were not in its count; they stay pending and the next
// marker reports them, so the markers' counts always add up to Dropped.
func (s *subscription) markerTaken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending -= s.offered
	s.offered = 0
	if s.pending == 0 {
		s.marker = false
	} else {
		s.markerTime = s.now()
	}
}

func (s *subscription) pump() {
	defer close(s.stopped)
	defer close(s.out)
	for {
		e, marker, ok := s.next()
		if !ok {
			select {
			case <-s.wake:
				continue
			case <-s.finished:
				return
			}
		}
		for {
			if marker {
				// Prefer a pending wake, so a marker offered after a burst
				// carries the burst's whole count.
				select {
				case <-s.wake:
					e = s.refreshMarker(e)
				default:
				}
			}
			select {
			case s.out <- e:
			case <-s.wake:
				if marker {
					e = s.refreshMarker(e) // more drops may have raised the count
				}
				continue
			case <-s.finished:
				return
			}
			break
		}
		if marker {
			s.markerTaken()
		}
	}
}

// Events implements Subscription.
func (s *subscription) Events() <-chan Event { return s.out }

// Dropped implements Subscription.
func (s *subscription) Dropped() uint64 { return s.dropped.Load() }

// Close implements Subscription. It returns once the Events channel is
// closed.
func (s *subscription) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.stopped
		return nil
	}
	s.closed = true
	s.ring, s.n, s.marker = nil, 0, false
	close(s.finished)
	s.mu.Unlock()
	s.remove(s)
	<-s.stopped
	return nil
}

// queue is a Point-to-Point Channel with back-pressure.
type queue struct {
	ch   chan Event
	done chan struct{}
	now  func() time.Time
}

// Send implements Queue.
func (q *queue) Send(ctx context.Context, e Event) error {
	if e.Seq != 0 {
		return ErrSequenced
	}
	e = e.Normalize(q.now())
	if err := e.Validate(); err != nil {
		return err
	}
	select {
	case <-q.done:
		return ErrClosed
	default:
	}
	select {
	case q.ch <- e:
		return nil
	case <-q.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Receive implements Queue. Once the bus is closed it returns ErrClosed,
// even if items were still queued.
func (q *queue) Receive(ctx context.Context) (Event, error) {
	select {
	case <-q.done:
		return Event{}, ErrClosed
	default:
	}
	select {
	case e := <-q.ch:
		return e, nil
	case <-q.done:
		return Event{}, ErrClosed
	case <-ctx.Done():
		return Event{}, ctx.Err()
	}
}
