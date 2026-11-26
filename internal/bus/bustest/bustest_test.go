package bustest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

const repo bus.RepoID = "0123456789abcdef"

func head(wt bus.WorktreeID) bus.Event {
	return bus.Event{Type: bus.TypeGitHeadChanged, Source: "test", Repo: repo, Worktree: wt}
}

func topic(t *testing.T, s string) bus.Topic {
	t.Helper()
	tp, err := bus.ParseTopic(s)
	if err != nil {
		t.Fatal(err)
	}
	return tp
}

func receive(t *testing.T, s bus.Subscription) bus.Event {
	t.Helper()
	select {
	case e := <-s.Events():
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
	return bus.Event{}
}

func TestPublishSubscribeFiltersAndChains(t *testing.T) {
	var seq uint64
	var tapped []bus.Type
	b := New(bus.Options{
		Interceptors: []bus.Interceptor{bus.InterceptorFunc(func(_ context.Context, e bus.Event) (bus.Event, error) {
			seq++
			e.Seq = seq
			return e, nil
		})},
		Taps: []bus.Tap{bus.TapFunc(func(_ context.Context, e bus.Event) { tapped = append(tapped, e.Type) })},
	})
	defer b.Close()
	ctx := context.Background()
	main, err := b.Subscribe(bus.SubscribeOptions{Name: "main"}, topic(t, "*/main/git.**"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Publish(ctx, head("other-1a2b3c4d")); err != nil {
		t.Fatal(err)
	}
	got, err := b.Publish(ctx, head("main"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.Seq != 2 || got.SchemaVersion != bus.SchemaVersion || string(got.Payload) != "{}" {
		t.Fatalf("published = %+v", got)
	}
	if e := receive(t, main); e.ID != got.ID {
		t.Fatalf("received %+v, want %s", e, got.ID)
	}
	if len(tapped) != 2 {
		t.Fatalf("tap saw %v", tapped)
	}
	if _, err := b.Publish(ctx, bus.Event{Type: "op.applied", Source: "x", Repo: repo}); !errors.Is(err, bus.ErrUnknownType) {
		t.Fatalf("unknown type err = %v", err)
	}
	sequenced := head("main")
	sequenced.Seq = 9
	if _, err := b.Publish(ctx, sequenced); !errors.Is(err, bus.ErrSequenced) {
		t.Fatalf("sequenced err = %v", err)
	}
	if _, err := b.Subscribe(bus.SubscribeOptions{}); !errors.Is(err, bus.ErrNoTopics) {
		t.Fatalf("no topics err = %v", err)
	}
}

func TestInterceptorErrorFailsPublish(t *testing.T) {
	boom := errors.New("disk full")
	b := New(bus.Options{Interceptors: []bus.Interceptor{bus.InterceptorFunc(func(context.Context, bus.Event) (bus.Event, error) {
		return bus.Event{}, boom
	})}})
	defer b.Close()
	if _, err := b.Publish(context.Background(), head("main")); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestOverflowDropsOldestAndMarksOnce(t *testing.T) {
	b := New(bus.Options{})
	defer b.Close()
	sub, err := b.Subscribe(bus.SubscribeOptions{Name: "slow", Buffer: 2}, topic(t, "*/*/**"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 6 {
		e, err := b.Publish(context.Background(), head("main"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	dropped := sub.Dropped()
	if dropped == 0 {
		t.Fatal("nothing dropped")
	}
	index := map[string]int{}
	for i, id := range ids {
		index[id] = i
	}
	// The pump may take a marker while publishing continues, so a burst can
	// produce more than one marker; their counts must add up to Dropped.
	var markers, delivered int
	var reported uint64
	last := -1
	for delivered+int(dropped) < len(ids) {
		e := receive(t, sub)
		if e.Type == bus.TypeStreamOverflow {
			markers++
			var p bus.StreamOverflow
			if err := json.Unmarshal(e.Payload, &p); err != nil || p.Dropped == 0 || p.Subscription != "slow" || e.Seq != 0 {
				t.Fatalf("overflow = %+v %s", p, e.Payload)
			}
			reported += p.Dropped
			continue
		}
		if index[e.ID] <= last {
			t.Fatalf("out of order: event %d after %d", index[e.ID], last)
		}
		last = index[e.ID]
		delivered++
	}
	if markers == 0 || reported != dropped || last != len(ids)-1 {
		t.Fatalf("markers=%d reported=%d dropped=%d last=%d want %d", markers, reported, dropped, last, len(ids)-1)
	}
}

func TestQueueCompetingConsumers(t *testing.T) {
	b := New(bus.Options{})
	q := b.Queue("work")
	if b.Queue("work") != q {
		t.Fatal("Queue not reused")
	}
	ctx := context.Background()
	for range 2 {
		if err := q.Send(ctx, head("main")); err != nil {
			t.Fatal(err)
		}
	}
	first, err1 := q.Receive(ctx)
	second, err2 := q.Receive(ctx)
	if err1 != nil || err2 != nil || first.ID == second.ID {
		t.Fatalf("receive = %v %v", err1, err2)
	}
	short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, err := q.Receive(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("empty receive err = %v", err)
	}
	_ = b.Close()
	if _, err := q.Receive(ctx); !errors.Is(err, bus.ErrClosed) {
		t.Fatalf("closed receive err = %v", err)
	}
}

func TestCloseEndsSubscriptions(t *testing.T) {
	b := New(bus.Options{})
	sub, _ := b.Subscribe(bus.SubscribeOptions{}, topic(t, "*/*/**"))
	_ = b.Close()
	select {
	case _, ok := <-sub.Events():
		if ok {
			t.Fatal("event after close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscription not closed")
	}
	if _, err := b.Publish(context.Background(), head("main")); !errors.Is(err, bus.ErrClosed) {
		t.Fatalf("publish after close err = %v", err)
	}
	if b.Close() == nil {
		t.Fatal("second Close succeeded")
	}
}

func TestDeadLettersKeepsLast(t *testing.T) {
	d := &DeadLetters{}
	for i := range bus.DeadLetterKeep + 5 {
		d.Dead(context.Background(), bus.Letter{Consumer: string(rune('a' + i%26))})
	}
	if n := len(d.Letters()); n != bus.DeadLetterKeep {
		t.Fatalf("kept %d", n)
	}
}
