package bus

import (
	"context"
	"errors"
	"time"
)

// Defaults for the production bus. Options fields left at zero use these.
const (
	// DefaultBuffer is each subscription's buffer, in events.
	DefaultBuffer = 256
	// DefaultQueueCapacity is each Point-to-Point queue's capacity.
	DefaultQueueCapacity = 64
	// DeadLetterKeep is how many dead letters the default Dead Letter
	// Channel keeps in memory.
	DeadLetterKeep = 256
)

// Errors returned by bus implementations.
var (
	ErrClosed = errors.New("bus closed")
	// ErrSequenced is returned when a publisher sets Event.Seq; only the
	// store assigns it.
	ErrSequenced = errors.New("event already has a sequence number")
	// ErrNoTopics is returned by Subscribe without topics.
	ErrNoTopics = errors.New("subscription needs at least one topic")
)

// Publisher puts events on the Publish-Subscribe Channel.
//
// Publish normalizes e (Event.Normalize), validates it (Event.Validate),
// runs it through the Interceptor chain (the store assigns Seq there), runs
// every Tap, then offers it to each matching subscription. It returns the
// event as delivered. Publish never blocks on a slow subscriber: a full
// subscription drops its oldest events (see Subscription). Publish is safe
// for concurrent use; events are delivered to every subscriber in the order
// their Publish calls passed the Interceptor chain.
type Publisher interface {
	Publish(ctx context.Context, e Event) (Event, error)
}

// SubscribeOptions configures one subscription.
type SubscribeOptions struct {
	// Name identifies the subscriber in stream.overflow payloads and dead
	// letters, for example "sse:3f2a", "projection:overview".
	Name string
	// Buffer is the subscription's capacity in events; 0 means
	// DefaultBuffer.
	Buffer int
}

// Subscriber registers Event-Driven Consumers on the Publish-Subscribe
// Channel (GoF Observer).
type Subscriber interface {
	// Subscribe returns a subscription receiving every event published
	// after the call that matches any of topics. It does not replay: read
	// the store from a Cursor for history, then drop live events the
	// cursor has Seen.
	Subscribe(opts SubscribeOptions, topics ...Topic) (Subscription, error)
}

// Subscription is one subscriber's bounded buffer.
//
// Overflow rule: when the buffer is full, the bus drops the oldest buffered
// event and appends the new one, so Publish never blocks. After dropping,
// the bus makes sure exactly one stream.overflow event (Transient,
// StreamOverflow payload, Seq 0) is waiting in the buffer: it adds one, or
// raises the Dropped count of the one already waiting. A consumer that reads
// stream.overflow has missed events and must resynchronize: re-read state
// from its source of truth, or the store from its last Cursor.
type Subscription interface {
	// Events delivers matching events in order. It is closed by Close and
	// by the bus's Close.
	Events() <-chan Event
	// Dropped is the total number of events dropped for this subscription.
	Dropped() uint64
	// Close unsubscribes. It is idempotent.
	Close() error
}

// Queue is a Point-to-Point Channel: each event sent is received by exactly
// one of its Competing Consumers. Queues carry work items (for example
// "recompute the overview of worktree W"), not observations: their events
// skip the Interceptor chain, Taps and the store, and are never fanned out.
// The event must still pass Event.Validate after Event.Normalize.
type Queue interface {
	// Send blocks while the queue is full, until ctx is done (back-pressure,
	// unlike the drop-oldest rule of subscriptions).
	Send(ctx context.Context, e Event) error
	// Receive blocks until an event arrives, ctx is done or the bus closes.
	Receive(ctx context.Context) (Event, error)
}

// Bus is the in-process message bus. One daemon or standalone process
// owns one Bus for every repository it serves.
type Bus interface {
	Publisher
	Subscriber
	// Queue returns the named Point-to-Point Channel, creating it with
	// DefaultQueueCapacity on first use.
	Queue(name string) Queue
	// Close closes every subscription and queue. Later calls return
	// ErrClosed.
	Close() error
}

// Interceptor is one filter in the Pipes and Filters chain every published
// event passes before delivery (GoF Chain of Responsibility). It may return
// a changed event (the store sets Seq) or an error, which fails Publish and
// stops the chain. Interceptors run in order, serialized, so they must not
// call Publish.
type Interceptor interface {
	Intercept(ctx context.Context, e Event) (Event, error)
}

// InterceptorFunc adapts a function to Interceptor.
type InterceptorFunc func(ctx context.Context, e Event) (Event, error)

// Intercept calls f.
func (f InterceptorFunc) Intercept(ctx context.Context, e Event) (Event, error) { return f(ctx, e) }

// Tap is a Wire Tap: it observes each event after the Interceptor chain
// and before delivery. It cannot change or reject the event. Taps run
// synchronously on the publishing goroutine, so they must be quick and must
// not call Publish. The audit tap (lane 1.1) hands op.* and approval.*
// events to policy.Gate.Audit; other families are observations, not
// actions, and are not audited.
type Tap interface {
	Tap(ctx context.Context, e Event)
}

// TapFunc adapts a function to Tap.
type TapFunc func(ctx context.Context, e Event)

// Tap calls f.
func (f TapFunc) Tap(ctx context.Context, e Event) { f(ctx, e) }

// Letter is one undeliverable message on the Dead Letter Channel.
type Letter struct {
	Event Event `json:"event"`
	// Consumer is the subscription or queue consumer that failed.
	Consumer string `json:"consumer"`
	// Reason is the error or recovered panic, as text.
	Reason string    `json:"reason"`
	Time   time.Time `json:"time"`
}

// DeadLetter is the Dead Letter Channel. Handle sends a letter when a
// handler returns an error or panics; later phases send one when a plugin
// crashes mid-delivery. Dropped overflow events are not dead letters: they
// are counted by Subscription.Dropped and reported by stream.overflow.
type DeadLetter interface {
	Dead(ctx context.Context, l Letter)
	// Letters returns the retained letters, oldest first. The default
	// channel keeps the last DeadLetterKeep in memory; a persistent one
	// arrives with plugins (phase 5).
	Letters() []Letter
}

// Handler consumes one event. Returning an error sends the event to the
// Dead Letter Channel; the subscription keeps running.
type Handler func(ctx context.Context, e Event) error

// Options configures the production bus (lane 1.1 provides New(Options)).
type Options struct {
	// Interceptors run in order on every published event. The store's
	// interceptor (store.Interceptor) comes after validation and before
	// any interceptor that needs Seq.
	Interceptors []Interceptor
	// Taps observe every event after the Interceptors.
	Taps []Tap
	// DeadLetter receives undeliverable events; nil means an in-memory
	// channel keeping DeadLetterKeep letters.
	DeadLetter DeadLetter
	// Buffer is the default subscription buffer; 0 means DefaultBuffer.
	Buffer int
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}
