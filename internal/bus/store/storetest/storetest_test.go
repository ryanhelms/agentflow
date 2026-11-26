package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/bustest"
	"github.com/ryanhelms/agentflow/internal/bus/store"
)

const repo bus.RepoID = "0123456789abcdef"

func event() bus.Event {
	return bus.Event{Type: bus.TypeGitRefsChanged, Source: "test", Repo: repo}.Normalize(time.Now())
}

func collect(t *testing.T, seq func(func(bus.Event, error) bool)) ([]uint64, error) {
	t.Helper()
	var out []uint64
	for e, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, e.Seq)
	}
	return out, nil
}

func TestAppendAssignsSeqAndReadFromResumes(t *testing.T) {
	s := New(repo)
	ctx := context.Background()
	for range 3 {
		if _, err := s.Append(ctx, event()); err != nil {
			t.Fatal(err)
		}
	}
	got, err := collect(t, s.ReadFrom(ctx, bus.Cursor{repo: 1}))
	if err != nil || len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("ReadFrom = %v, %v", got, err)
	}
	if head, _ := s.Head(ctx); head.String() != "v1:"+string(repo)+"=3" {
		t.Fatalf("Head = %v", head)
	}
	other := event()
	other.Repo = "ffffffffffffffff"
	if _, err := s.Append(ctx, other); !errors.Is(err, store.ErrWrongRepo) {
		t.Fatalf("wrong repo err = %v", err)
	}
	overflow := bus.Event{Type: bus.TypeStreamOverflow, Source: "bus", Repo: repo}.Normalize(time.Now())
	if _, err := s.Append(ctx, overflow); !errors.Is(err, store.ErrTransient) {
		t.Fatalf("transient err = %v", err)
	}
}

func TestReadFromExpiredCursor(t *testing.T) {
	s := New(repo)
	ctx := context.Background()
	for range 4 {
		_, _ = s.Append(ctx, event())
	}
	s.Expire(2)
	if _, err := collect(t, s.ReadFrom(ctx, bus.Cursor{repo: 1})); !errors.Is(err, store.ErrCursorExpired) {
		t.Fatalf("expired cursor err = %v", err)
	}
	if got, err := collect(t, s.ReadFrom(ctx, bus.Cursor{})); err != nil || len(got) != 2 || got[0] != 3 {
		t.Fatalf("empty cursor = %v, %v; want the retained events", got, err)
	}
	if got, err := collect(t, s.ReadFrom(ctx, bus.Cursor{repo: 2})); err != nil || len(got) != 2 {
		t.Fatalf("cursor at retention edge = %v, %v", got, err)
	}
}

func TestFollowYieldsNewEvents(t *testing.T) {
	s := New(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := make(chan uint64, 2)
	go func() {
		for e, err := range s.Follow(ctx, bus.Cursor{}) {
			if err != nil {
				return
			}
			got <- e.Seq
		}
	}()
	for want := uint64(1); want <= 2; want++ {
		if _, err := s.Append(ctx, event()); err != nil {
			t.Fatal(err)
		}
		select {
		case seq := <-got:
			if seq != want {
				t.Fatalf("followed seq %d, want %d", seq, want)
			}
		case <-ctx.Done():
			t.Fatal("follow stalled")
		}
	}
}

func TestInterceptorSequencesThroughBus(t *testing.T) {
	s := New(repo)
	b := bustest.New(bus.Options{Interceptors: []bus.Interceptor{store.Interceptor(func(r bus.RepoID) (store.Store, bool) {
		return s, r == repo
	})}})
	defer b.Close()
	ctx := context.Background()
	e, err := b.Publish(ctx, bus.Event{Type: bus.TypeGitRefsChanged, Source: "test", Repo: repo})
	if err != nil || e.Seq != 1 {
		t.Fatalf("Publish = %+v, %v", e, err)
	}
	overflow, err := b.Publish(ctx, bus.Event{Type: bus.TypeStreamOverflow, Source: "bus"})
	if err != nil || overflow.Seq != 0 {
		t.Fatalf("transient Publish = %+v, %v", overflow, err)
	}
	if _, err := b.Publish(ctx, bus.Event{Type: bus.TypeGitRefsChanged, Source: "test", Repo: "ffffffffffffffff"}); err == nil {
		t.Fatal("event for unknown repository accepted")
	}
}
