package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

func TestUnionMergesByTimeRepoSeq(t *testing.T) {
	c := newClock()
	opt := testOptions()
	opt.now = c.now
	a := mustOpen(t, t.TempDir(), RoleOwner, opt)
	b := mustOpen(t, t.TempDir(), RoleOwner, opt)
	// a: t0, t2; b: t1, t2. At t2 the smaller repo ID goes first.
	appendN(t, a, 1)
	c.advance(time.Second)
	appendN(t, b, 1)
	c.advance(time.Second)
	appendN(t, a, 1)
	appendN(t, b, 1)
	first, second := a.Repo(), b.Repo()
	if second < first {
		first, second = second, first
	}
	u := Union(a, b)
	type ref struct {
		repo bus.RepoID
		seq  uint64
	}
	var got []ref
	for e, err := range u.ReadFrom(context.Background(), bus.Cursor{}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ref{e.Repo, e.Seq})
	}
	want := []ref{{a.Repo(), 1}, {b.Repo(), 1}, {first, 2}, {second, 2}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	head, err := u.Head(context.Background())
	if err != nil || head[a.Repo()] != 2 || head[b.Repo()] != 2 {
		t.Fatalf("Head = %v, %v", head, err)
	}
	// A cursor that has seen a's events reads only b's.
	n := 0
	for e, err := range u.ReadFrom(context.Background(), bus.Cursor{a.Repo(): 2}) {
		if err != nil || e.Repo != b.Repo() {
			t.Fatalf("resumed read yielded %+v, %v", e, err)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("resumed read yielded %d events", n)
	}
}

func TestUnionReportsExpiryFirst(t *testing.T) {
	opt := testOptions()
	a := mustOpen(t, t.TempDir(), RoleOwner, opt)
	appendN(t, a, 3)
	opt.segmentBytes = 512
	opt.retainBytes = 1024
	b := mustOpen(t, t.TempDir(), RoleOwner, opt)
	appendN(t, b, 50)
	n := 0
	for _, err := range Union(a, b).ReadFrom(context.Background(), bus.Cursor{b.Repo(): 1}) {
		n++
		if !errors.Is(err, ErrCursorExpired) {
			t.Fatalf("yield %d err = %v, want ErrCursorExpired first", n, err)
		}
	}
	if n != 1 {
		t.Fatalf("yielded %d times", n)
	}
}

func TestUnionFollow(t *testing.T) {
	a := mustOpen(t, t.TempDir(), RoleOwner, testOptions())
	b := mustOpen(t, t.TempDir(), RoleOwner, testOptions())
	u := Union(a, b).(*union)
	u.poll = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	got := make(chan bus.Event, 64)
	go func() {
		for e, err := range u.Follow(ctx, bus.Cursor{}) {
			if err != nil {
				return
			}
			got <- e
		}
	}()
	counts := map[bus.RepoID]uint64{}
	for range 10 {
		appendN(t, a, 1)
		appendN(t, b, 1)
	}
	for range 20 {
		select {
		case e := <-got:
			if e.Seq != counts[e.Repo]+1 {
				t.Fatalf("repo %s: seq %d after %d", e.Repo, e.Seq, counts[e.Repo])
			}
			counts[e.Repo] = e.Seq
		case <-ctx.Done():
			t.Fatalf("follow stalled at %v", counts)
		}
	}
}
