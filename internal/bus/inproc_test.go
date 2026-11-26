package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	repoA RepoID = "0123456789abcdef"
	repoB RepoID = "fedcba9876543210"
)

var everything = Topic{Repo: Any, Worktree: Any, Types: "**"}

func head(repo RepoID, wt WorktreeID, n int) Event {
	return Event{Type: TypeGitHeadChanged, Source: "test", Repo: repo, Worktree: wt,
		Payload: json.RawMessage(fmt.Sprintf(`{"ref":"refs/heads/b%d"}`, n))}
}

func refName(t testing.TB, e Event) string {
	t.Helper()
	var p GitHeadChanged
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p.Ref
}

// sequencer is a stand-in for store.Interceptor: it numbers durable events
// per repository.
type sequencer struct {
	mu  sync.Mutex
	seq map[RepoID]uint64
}

func (s *sequencer) Intercept(_ context.Context, e Event) (Event, error) {
	if info, _ := Lookup(e.Type); info.Transient {
		return e, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seq == nil {
		s.seq = map[RepoID]uint64{}
	}
	s.seq[e.Repo]++
	e.Seq = s.seq[e.Repo]
	return e, nil
}

func recv(t testing.TB, sub Subscription) Event {
	t.Helper()
	select {
	case e, ok := <-sub.Events():
		if !ok {
			t.Fatal("subscription closed")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return Event{}
}

func newBus(t testing.TB, opts Options) Bus {
	t.Helper()
	b := New(opts)
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestPublishNormalizesValidatesAndDelivers(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.FixedZone("x", 3600))
	b := newBus(t, Options{Now: func() time.Time { return now }})
	sub, err := b.Subscribe(SubscribeOptions{Name: "t"}, everything)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Publish(context.Background(), Event{Type: TypeGitIndexChanged, Source: "test", Repo: repoA, Worktree: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || !got.Time.Equal(now) || got.Time.Location() != time.UTC || got.SchemaVersion != SchemaVersion || string(got.Payload) != "{}" {
		t.Fatalf("not normalized: %+v", got)
	}
	if e := recv(t, sub); e.ID != got.ID {
		t.Fatalf("delivered %+v, want %+v", e, got)
	}

	ctx := context.Background()
	if _, err := b.Publish(ctx, Event{Type: TypeGitIndexChanged, Source: "t", Repo: repoA, Worktree: "main", Seq: 1}); !errors.Is(err, ErrSequenced) {
		t.Fatalf("caller Seq: %v", err)
	}
	if _, err := b.Publish(ctx, Event{Type: "op.applied", Source: "t", Repo: repoA}); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("unknown type: %v", err)
	}
	if _, err := b.Publish(ctx, Event{Type: TypeGitRefsChanged, Source: "t", Repo: repoA, Worktree: "main"}); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("invalid envelope: %v", err)
	}
}

func TestInterceptorChainRunsInOrderAndStops(t *testing.T) {
	var order []string
	step := func(name string, err error) Interceptor {
		return InterceptorFunc(func(_ context.Context, e Event) (Event, error) {
			order = append(order, name)
			e.CorrelationID += name
			return e, err
		})
	}
	var tapped []Event
	tap := TapFunc(func(_ context.Context, e Event) { tapped = append(tapped, e) })

	b := newBus(t, Options{Interceptors: []Interceptor{step("a", nil), step("b", nil)}, Taps: []Tap{tap}})
	got, err := b.Publish(context.Background(), head(repoA, "main", 1))
	if err != nil || got.CorrelationID != "ab" || strings.Join(order, "") != "ab" {
		t.Fatalf("chain: %v %q %v", err, got.CorrelationID, order)
	}
	if len(tapped) != 1 || tapped[0].CorrelationID != "ab" {
		t.Fatalf("tap saw %+v", tapped)
	}

	order, tapped = nil, nil
	boom := errors.New("boom")
	b2 := newBus(t, Options{Interceptors: []Interceptor{step("a", boom), step("b", nil)}, Taps: []Tap{tap}})
	sub, _ := b2.Subscribe(SubscribeOptions{}, everything)
	if _, err := b2.Publish(context.Background(), head(repoA, "main", 1)); !errors.Is(err, boom) {
		t.Fatalf("error not returned: %v", err)
	}
	if strings.Join(order, "") != "a" || len(tapped) != 0 {
		t.Fatalf("chain did not stop: %v, tapped %d", order, len(tapped))
	}
	select {
	case e := <-sub.Events():
		t.Fatalf("rejected event delivered: %+v", e)
	case <-time.After(20 * time.Millisecond):
	}

	panicky := InterceptorFunc(func(context.Context, Event) (Event, error) { panic("nope") })
	b3 := newBus(t, Options{Interceptors: []Interceptor{panicky}})
	if _, err := b3.Publish(context.Background(), head(repoA, "main", 1)); err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("interceptor panic: %v", err)
	}
}

func TestSeqRules(t *testing.T) {
	b := newBus(t, Options{Interceptors: []Interceptor{&sequencer{}}})
	sub, _ := b.Subscribe(SubscribeOptions{}, everything)
	ctx := context.Background()
	for i, repo := range []RepoID{repoA, repoA, repoB, repoA} {
		if _, err := b.Publish(ctx, head(repo, "main", i)); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for range 4 {
		e := recv(t, sub)
		got = append(got, fmt.Sprintf("%s=%d", e.Repo[:1], e.Seq))
	}
	if strings.Join(got, ",") != "0=1,0=2,f=1,0=3" {
		t.Fatalf("seq %v", got)
	}

	// An interceptor that goes backwards, or stores a transient event, is
	// rejected and nothing is delivered.
	var next uint64 = 5
	fixed := InterceptorFunc(func(_ context.Context, e Event) (Event, error) { e.Seq = next; return e, nil })
	b2 := newBus(t, Options{Interceptors: []Interceptor{fixed}})
	if _, err := b2.Publish(ctx, head(repoA, "main", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := b2.Publish(ctx, head(repoA, "main", 0)); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("repeated seq: %v", err)
	}
	next = 9
	if _, err := b2.Publish(ctx, Event{Type: TypeStreamOverflow, Source: "t"}); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("stored transient: %v", err)
	}
}

func TestTopicFiltering(t *testing.T) {
	b := newBus(t, Options{})
	if _, err := b.Subscribe(SubscribeOptions{}); !errors.Is(err, ErrNoTopics) {
		t.Fatalf("no topics: %v", err)
	}
	for _, bad := range []Topic{{Repo: "nope", Worktree: Any, Types: "**"}, {Repo: Any, Worktree: "", Types: "**"}, {Repo: Any, Worktree: Any, Types: "git.**.x"}} {
		if _, err := b.Subscribe(SubscribeOptions{}, bad); !errors.Is(err, ErrInvalidTopic) {
			t.Fatalf("topic %v: %v", bad, err)
		}
	}
	main, _ := b.Subscribe(SubscribeOptions{}, Topic{Repo: repoA, Worktree: "main", Types: "git.**"})
	cfg, _ := b.Subscribe(SubscribeOptions{}, Topic{Repo: Any, Worktree: Any, Types: "config.*.*"})
	ctx := context.Background()
	mustPublish := func(e Event) {
		t.Helper()
		if _, err := b.Publish(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	mustPublish(head(repoA, "other", 1))                                                      // other worktree
	mustPublish(head(repoB, "main", 2))                                                       // other repo
	mustPublish(Event{Type: TypeGitRefsChanged, Source: "t", Repo: repoA})                    // repo-wide: matches main
	mustPublish(Event{Type: TypeConfigLayerChanged, Source: "t", Repo: repoB, Worktree: "x"}) // cfg only
	mustPublish(head(repoA, "main", 3))

	if e := recv(t, main); e.Type != TypeGitRefsChanged {
		t.Fatalf("main got %s", e.Type)
	}
	if e := recv(t, main); refName(t, e) != "refs/heads/b3" {
		t.Fatalf("main got %+v", e)
	}
	if e := recv(t, cfg); e.Type != TypeConfigLayerChanged {
		t.Fatalf("cfg got %s", e.Type)
	}
	select {
	case e := <-cfg.Events():
		t.Fatalf("cfg got extra %s", e.Type)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestOverflowDropsOldestWithOneMarker(t *testing.T) {
	b := newBus(t, Options{Buffer: 4})
	sub, _ := b.Subscribe(SubscribeOptions{Name: "slow"}, everything)
	ctx := context.Background()
	const n = 20
	for i := 1; i <= n; i++ {
		if _, err := b.Publish(ctx, head(repoA, "main", i)); err != nil {
			t.Fatal(err)
		}
	}
	// The pump may already hold event 1; every other event waits in the
	// 4-slot ring behind one marker.
	var refs []string
	var markers []StreamOverflow
	for len(refs) == 0 || refs[len(refs)-1] != fmt.Sprintf("refs/heads/b%d", n) {
		e := recv(t, sub)
		if e.Type == TypeStreamOverflow {
			var p StreamOverflow
			_ = json.Unmarshal(e.Payload, &p)
			if e.Seq != 0 || e.Repo != "" || e.Source != overflowSource {
				t.Fatalf("marker envelope %+v", e)
			}
			markers = append(markers, p)
			refs = append(refs, "overflow")
			continue
		}
		refs = append(refs, refName(t, e))
	}
	// One marker waits at a time. A drop that races the hand-off of a
	// marker is reported by the next one, so the counts always add up.
	var reported uint64
	for _, m := range markers {
		if m.Subscription != "slow" || m.Dropped == 0 {
			t.Fatalf("marker %+v", m)
		}
		reported += m.Dropped
	}
	if len(markers) == 0 || reported != sub.Dropped() {
		t.Fatalf("markers %+v, dropped %d, refs %v", markers, sub.Dropped(), refs)
	}
	tail := strings.Join(refs[len(refs)-5:], ",")
	if tail != "overflow,refs/heads/b17,refs/heads/b18,refs/heads/b19,refs/heads/b20" {
		t.Fatalf("tail %s", tail)
	}
	if real := uint64(len(refs) - len(markers)); real+sub.Dropped() != n {
		t.Fatalf("delivered %d + dropped %d != %d", real, sub.Dropped(), n)
	}

	// A second overflow after the marker was read counts only new drops.
	before := sub.Dropped()
	for i := 1; i <= 10; i++ {
		_, _ = b.Publish(ctx, head(repoA, "main", 100+i))
	}
	var second []StreamOverflow
	for {
		e := recv(t, sub)
		if e.Type == TypeStreamOverflow {
			var p StreamOverflow
			_ = json.Unmarshal(e.Payload, &p)
			second = append(second, p)
			continue
		}
		if refName(t, e) == "refs/heads/b110" {
			break
		}
	}
	var secondDrops uint64
	for _, m := range second {
		secondDrops += m.Dropped
	}
	if len(second) == 0 || secondDrops != sub.Dropped()-before {
		t.Fatalf("second overflow %+v, dropped %d since", second, sub.Dropped()-before)
	}
}

func TestConcurrentPublishersAndSubscribers(t *testing.T) {
	b := newBus(t, Options{Interceptors: []Interceptor{&sequencer{}}})
	const pubs, per, subs = 8, 200, 6
	total := pubs * per
	results := make([][]string, subs)
	var wg sync.WaitGroup
	for i := range subs {
		sub, err := b.Subscribe(SubscribeOptions{Name: fmt.Sprint(i), Buffer: total + 1}, everything)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			last := map[RepoID]uint64{}
			for range total {
				e, ok := <-sub.Events()
				if !ok {
					t.Errorf("sub %d closed early", i)
					return
				}
				if e.Seq <= last[e.Repo] {
					t.Errorf("sub %d: seq %d after %d", i, e.Seq, last[e.Repo])
				}
				last[e.Repo] = e.Seq
				results[i] = append(results[i], e.ID)
			}
			if sub.Dropped() != 0 {
				t.Errorf("sub %d dropped %d", i, sub.Dropped())
			}
		}()
	}
	var pwg sync.WaitGroup
	for p := range pubs {
		pwg.Add(1)
		go func() {
			defer pwg.Done()
			repo := repoA
			if p%2 == 1 {
				repo = repoB
			}
			for j := range per {
				if _, err := b.Publish(context.Background(), head(repo, "main", j)); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	pwg.Wait()
	wg.Wait()
	for i := 1; i < subs; i++ {
		if strings.Join(results[i], ",") != strings.Join(results[0], ",") {
			t.Fatalf("subscriber %d saw a different order", i)
		}
	}
}

func TestConcurrentOverflowMarkersAddUp(t *testing.T) {
	b := newBus(t, Options{Buffer: 3})
	sub, _ := b.Subscribe(SubscribeOptions{Name: "slow"}, everything)
	var wg sync.WaitGroup
	for p := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 500 {
				_, _ = b.Publish(context.Background(), head(repoA, "main", p*1000+j))
			}
		}()
	}
	done := make(chan struct{})
	var markerDrops uint64
	go func() {
		defer close(done)
		for e := range sub.Events() {
			if e.Type == TypeStreamOverflow {
				var p StreamOverflow
				_ = json.Unmarshal(e.Payload, &p)
				if p.Dropped == 0 {
					t.Error("marker with zero drops")
				}
				markerDrops += p.Dropped
				continue
			}
			if refName(t, e) == "refs/heads/end" {
				return
			}
			time.Sleep(time.Microsecond)
		}
	}()
	wg.Wait()
	if _, err := b.Publish(context.Background(), Event{Type: TypeGitHeadChanged, Source: "t", Repo: repoA, Worktree: "main",
		Payload: json.RawMessage(`{"ref":"refs/heads/end"}`)}); err != nil {
		t.Fatal(err)
	}
	<-done
	if markerDrops == 0 || markerDrops != sub.Dropped() {
		t.Fatalf("markers reported %d drops, subscription %d", markerDrops, sub.Dropped())
	}
}

func TestTapPanicBecomesDeadLetter(t *testing.T) {
	dl := NewDeadLetter(0)
	bad := TapFunc(func(context.Context, Event) { panic("tap") })
	b := newBus(t, Options{Taps: []Tap{bad}, DeadLetter: dl})
	if DeadLetterOf(b) != dl {
		t.Fatal("DeadLetterOf")
	}
	sub, _ := b.Subscribe(SubscribeOptions{}, everything)
	got, err := b.Publish(context.Background(), head(repoA, "main", 1))
	if err != nil {
		t.Fatal(err)
	}
	if e := recv(t, sub); e.ID != got.ID {
		t.Fatal("not delivered")
	}
	if l := dl.Letters(); len(l) != 1 || l[0].Consumer != "tap:0" || !strings.Contains(l[0].Reason, "panicked") {
		t.Fatalf("letters %+v", l)
	}
}

func TestHandleOnBusDeadLetters(t *testing.T) {
	b := newBus(t, Options{})
	dl := DeadLetterOf(b)
	if dl == nil {
		t.Fatal("no default dead letter channel")
	}
	sub, _ := b.Subscribe(SubscribeOptions{Name: "h"}, everything)
	var handled atomic.Int32
	errc := make(chan error, 1)
	go func() {
		errc <- Handle(context.Background(), sub, "h", func(_ context.Context, e Event) error {
			handled.Add(1)
			switch refName(t, e) {
			case "refs/heads/b1":
				return errors.New("fail")
			case "refs/heads/b2":
				panic("crash")
			}
			return nil
		}, dl)
	}()
	for i := 1; i <= 3; i++ {
		_, _ = b.Publish(context.Background(), head(repoA, "main", i))
	}
	deadline := time.Now().Add(5 * time.Second)
	for handled.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	_ = sub.Close()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	l := dl.Letters()
	if len(l) != 2 || l[0].Reason != "fail" || !strings.Contains(l[1].Reason, "crash") || l[0].Consumer != "h" {
		t.Fatalf("letters %+v", l)
	}
}

func TestDeadLetterKeepsLast(t *testing.T) {
	dl := NewDeadLetter(3)
	for i := range 5 {
		dl.Dead(context.Background(), Letter{Consumer: fmt.Sprint(i)})
	}
	var got []string
	for _, l := range dl.Letters() {
		got = append(got, l.Consumer)
	}
	if strings.Join(got, "") != "234" {
		t.Fatalf("letters %v", got)
	}
	if n := len(NewDeadLetter(0).(*memoryDeadLetter).ring); n != DeadLetterKeep {
		t.Fatalf("default keep %d", n)
	}
}

func TestSubscriptionClose(t *testing.T) {
	b := newBus(t, Options{})
	sub, _ := b.Subscribe(SubscribeOptions{}, everything)
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sub.Close(); err != nil {
		t.Fatal("second Close", err)
	}
	if _, ok := <-sub.Events(); ok {
		t.Fatal("Events not closed")
	}
	if n := len(*b.(*inproc).subs.Load()); n != 0 {
		t.Fatalf("%d subscriptions left", n)
	}
	if _, err := b.Publish(context.Background(), head(repoA, "main", 1)); err != nil {
		t.Fatal(err)
	}
}

func TestCloseWaitsForInFlightPublish(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	slow := InterceptorFunc(func(_ context.Context, e Event) (Event, error) {
		close(entered)
		<-release
		return e, nil
	})
	b := New(Options{Interceptors: []Interceptor{slow}})
	sub, _ := b.Subscribe(SubscribeOptions{}, everything)
	q := b.Queue("work")

	pubErr := make(chan error, 1)
	go func() {
		_, err := b.Publish(context.Background(), head(repoA, "main", 1))
		pubErr <- err
	}()
	<-entered
	recvErr := make(chan error, 1)
	go func() {
		_, err := q.Receive(context.Background())
		recvErr <- err
	}()
	closed := make(chan error, 1)
	go func() { closed <- b.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned during Publish: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-pubErr; err != nil {
		t.Fatalf("in-flight Publish: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := <-recvErr; !errors.Is(err, ErrClosed) {
		t.Fatalf("blocked Receive: %v", err)
	}
	for range sub.Events() { // closed; any buffered event is discarded or drained
	}
	if _, err := b.Publish(context.Background(), head(repoA, "main", 2)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Publish after Close: %v", err)
	}
	if _, err := b.Subscribe(SubscribeOptions{}, everything); !errors.Is(err, ErrClosed) {
		t.Fatalf("Subscribe after Close: %v", err)
	}
	if err := b.Queue("new").Send(context.Background(), head(repoA, "main", 1)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Send after Close: %v", err)
	}
	if err := b.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("second Close: %v", err)
	}
}

func TestCloseDuringConcurrentPublish(t *testing.T) {
	for range 20 {
		b := New(Options{Buffer: 8})
		var wg sync.WaitGroup
		for range 4 {
			sub, _ := b.Subscribe(SubscribeOptions{}, everything)
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range sub.Events() {
				}
			}()
		}
		for p := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; ; j++ {
					if _, err := b.Publish(context.Background(), head(repoA, "main", p*1000+j)); err != nil {
						if !errors.Is(err, ErrClosed) {
							t.Error(err)
						}
						return
					}
				}
			}()
		}
		time.Sleep(time.Millisecond)
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
	}
}

func TestQueue(t *testing.T) {
	var intercepted, tapped atomic.Int32
	b := newBus(t, Options{
		Interceptors: []Interceptor{InterceptorFunc(func(_ context.Context, e Event) (Event, error) { intercepted.Add(1); return e, nil })},
		Taps:         []Tap{TapFunc(func(context.Context, Event) { tapped.Add(1) })},
	})
	sub, _ := b.Subscribe(SubscribeOptions{}, everything)
	q := b.Queue("work")
	if b.Queue("work") != q {
		t.Fatal("Queue returned a new queue for the same name")
	}
	ctx := context.Background()
	if err := q.Send(ctx, Event{Type: "nope", Source: "t"}); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("invalid Send: %v", err)
	}

	// Competing consumers: every item reaches exactly one receiver.
	const n = 500
	seen := make(chan string, n)
	var wg sync.WaitGroup
	cctx, cancel := context.WithCancel(ctx)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				e, err := q.Receive(cctx)
				if err != nil {
					return
				}
				seen <- e.ID
			}
		}()
	}
	for i := range n {
		if err := q.Send(ctx, head(repoA, "main", i)); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]bool{}
	for range n {
		id := <-seen
		if ids[id] {
			t.Fatalf("item %s received twice", id)
		}
		ids[id] = true
	}
	cancel()
	wg.Wait()
	if intercepted.Load() != 0 || tapped.Load() != 0 {
		t.Fatal("queue items passed the interceptors or taps")
	}
	select {
	case e := <-sub.Events():
		t.Fatalf("queue item fanned out: %+v", e)
	default:
	}

	// Back-pressure: a full queue makes Send wait until its context ends.
	for i := range DefaultQueueCapacity {
		if err := q.Send(ctx, head(repoA, "main", i)); err != nil {
			t.Fatal(err)
		}
	}
	tctx, tcancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer tcancel()
	if err := q.Send(tctx, head(repoA, "main", 0)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send on full queue: %v", err)
	}
	sent := make(chan error, 1)
	go func() { sent <- q.Send(ctx, head(repoA, "main", 999)) }()
	if _, err := q.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatalf("Send after space freed: %v", err)
	}
}

func BenchmarkPublish(b *testing.B) {
	for _, subs := range []int{0, 1, 8} {
		b.Run(fmt.Sprintf("subs=%d", subs), func(b *testing.B) {
			bs := New(Options{Interceptors: []Interceptor{&sequencer{}}})
			defer bs.Close()
			for range subs {
				sub, _ := bs.Subscribe(SubscribeOptions{}, Topic{Repo: repoA, Worktree: Any, Types: "git.**"})
				go func() {
					for range sub.Events() {
					}
				}()
			}
			e := head(repoA, "main", 1)
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := bs.Publish(ctx, e); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
