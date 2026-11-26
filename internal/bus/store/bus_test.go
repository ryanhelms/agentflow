package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// TestRealBusSequencesThroughFileStores runs the production bus with the
// store interceptor in front of two file stores, as the daemon does.
func TestRealBusSequencesThroughFileStores(t *testing.T) {
	const publishers, perPublisher = 8, 50
	stores := map[bus.RepoID]*fileStore{}
	var repos []bus.RepoID
	for range 2 {
		s := mustOpen(t, t.TempDir(), RoleOwner, testOptions())
		stores[s.Repo()] = s
		repos = append(repos, s.Repo())
	}
	b := bus.New(bus.Options{Interceptors: []bus.Interceptor{Interceptor(func(r bus.RepoID) (Store, bool) {
		s, ok := stores[r]
		return s, ok
	})}})
	defer func() { _ = b.Close() }()
	total := publishers * perPublisher
	sub, err := b.Subscribe(bus.SubscribeOptions{Name: "test", Buffer: total + 16}, bus.Topic{Repo: bus.Any, Worktree: bus.Any, Types: "**"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, total)
	for p := range publishers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perPublisher {
				repo := repos[(p+i)%len(repos)]
				if _, err := b.Publish(ctx, bus.Event{Type: bus.TypeGitRefsChanged, Source: "test", Repo: repo}); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent publish: %v", err)
	}

	// Subscribers see each repository's Seq rise by exactly one.
	last := map[bus.RepoID]uint64{}
	for range total {
		e := <-sub.Events()
		if e.Seq != last[e.Repo]+1 {
			t.Fatalf("repository %s delivered seq %d after %d", e.Repo, e.Seq, last[e.Repo])
		}
		last[e.Repo] = e.Seq
	}
	if sub.Dropped() != 0 {
		t.Fatalf("dropped %d events", sub.Dropped())
	}
	var sum uint64
	for _, repo := range repos {
		wantRange(t, seqs(t, stores[repo], bus.Cursor{}), 1, last[repo])
		sum += last[repo]
	}
	if sum != uint64(total) {
		t.Fatalf("stored %d events, want %d", sum, total)
	}

	// Only the store assigns Seq.
	if _, err := b.Publish(ctx, bus.Event{Type: bus.TypeGitRefsChanged, Source: "test", Repo: repos[0], Seq: 99}); !errors.Is(err, bus.ErrSequenced) {
		t.Fatalf("publish with Seq = %v, want ErrSequenced", err)
	}

	// Transient events reach subscribers but never the store.
	before := stores[repos[0]].mustHead(t)
	e, err := b.Publish(ctx, bus.Event{Type: bus.TypeStreamOverflow, Source: "test", Repo: repos[0]})
	if err != nil || e.Seq != 0 {
		t.Fatalf("transient publish = seq %d, %v", e.Seq, err)
	}
	if got := <-sub.Events(); got.Type != bus.TypeStreamOverflow || got.Seq != 0 {
		t.Fatalf("subscriber got %+v, want the transient event", got)
	}
	if after := stores[repos[0]].mustHead(t); after[repos[0]] != before[repos[0]] {
		t.Fatalf("store head moved from %v to %v on a transient event", before, after)
	}
	wantRange(t, seqs(t, stores[repos[0]], bus.Cursor{}), 1, last[repos[0]])
}

func (s *fileStore) mustHead(t testing.TB) bus.Cursor {
	t.Helper()
	c, err := s.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}
