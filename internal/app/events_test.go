package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
	"github.com/ryanhelms/agentflow/internal/gitx"
)

const eventsRepo bus.RepoID = "0123456789abcdef"

func eventsApp(t *testing.T, st *storetest.Store) (*App, *[]store.Role) {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	var roles []store.Role
	old := OpenEventStore
	OpenEventStore = func(_ context.Context, common string, role store.Role) (store.Store, error) {
		roles = append(roles, role)
		if st == nil {
			return nil, os.ErrNotExist
		}
		if _, err := os.Stat(common); err != nil {
			t.Errorf("common dir %q: %v", common, err)
		}
		return st, nil
	}
	t.Cleanup(func() { OpenEventStore = old })
	return &App{Repo: &gitx.Repo{Dir: dir}}, &roles
}

func appendEvents(t *testing.T, st *storetest.Store, types ...bus.Type) {
	t.Helper()
	for _, typ := range types {
		e := bus.Event{Type: typ, Source: "test", Repo: eventsRepo}
		if info, _ := bus.Lookup(typ); info.Scope == bus.ScopeWorktree {
			e.Worktree = "main"
		}
		if _, err := st.Append(context.Background(), e.Normalize(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEventsPagesWithCursor(t *testing.T) {
	st := storetest.New(eventsRepo)
	appendEvents(t, st, bus.TypeGitHeadChanged, bus.TypeConfigLayerChanged, bus.TypeGitRefsChanged, bus.TypeGitHeadChanged)
	a, roles := eventsApp(t, st)
	ctx := context.Background()

	page, err := a.Events(ctx, EventsQuery{Limit: 2})
	if err != nil || len(page.Events) != 2 || !page.More || page.Cursor != "v1:"+string(eventsRepo)+"=2" || page.Repo != eventsRepo {
		t.Fatalf("page 1 = %+v, %v", page, err)
	}
	page, err = a.Events(ctx, EventsQuery{Since: page.Cursor})
	if err != nil || len(page.Events) != 2 || page.More || page.Events[0].Seq != 3 || page.Cursor != "v1:"+string(eventsRepo)+"=4" {
		t.Fatalf("page 2 = %+v, %v", page, err)
	}
	// Filtered events still move the cursor, so the next page skips them.
	page, err = a.Events(ctx, EventsQuery{Types: []string{"config.**"}})
	if err != nil || len(page.Events) != 1 || page.Events[0].Seq != 2 || page.Cursor != "v1:"+string(eventsRepo)+"=4" {
		t.Fatalf("typed page = %+v, %v", page, err)
	}
	page, err = a.Events(ctx, EventsQuery{Topics: []string{"*/main/git.**"}})
	if err != nil || len(page.Events) != 3 {
		t.Fatalf("topic page = %+v, %v", page, err)
	}
	for _, r := range *roles {
		if r != store.RoleReader {
			t.Fatalf("opened the store as %v, want only RoleReader", r)
		}
	}
}

func TestEventsRejectsBadQueries(t *testing.T) {
	st := storetest.New(eventsRepo)
	appendEvents(t, st, bus.TypeGitHeadChanged, bus.TypeGitHeadChanged, bus.TypeGitHeadChanged)
	a, _ := eventsApp(t, st)
	ctx := context.Background()
	for _, q := range []EventsQuery{
		{Since: "bogus"},
		{Types: []string{"Git.*"}},
		{Topics: []string{"fedcba9876543210/*/**"}},
		{Topics: []string{"global/*/**"}},
		{Limit: MaxEventsLimit + 1},
		{Limit: -1},
	} {
		if _, err := a.Events(ctx, q); err == nil {
			t.Errorf("Events(%+v) succeeded", q)
		}
	}
	st.Expire(2)
	if _, err := a.Events(ctx, EventsQuery{Since: "v1:" + string(eventsRepo) + "=1"}); !errors.Is(err, store.ErrCursorExpired) {
		t.Fatalf("expired cursor err = %v", err)
	}
}

func TestEventsWithoutStore(t *testing.T) {
	a, _ := eventsApp(t, nil)
	page, err := a.Events(context.Background(), EventsQuery{})
	if err != nil || len(page.Events) != 0 || page.Events == nil || page.Cursor != "" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if _, err := a.Events(context.Background(), EventsQuery{Types: []string{"bad pattern"}}); err == nil {
		t.Fatal("a malformed pattern was accepted without a store")
	}
}

func TestFollowEventsEnsuresDaemonAndStreams(t *testing.T) {
	st := storetest.New(eventsRepo)
	appendEvents(t, st, bus.TypeGitHeadChanged, bus.TypeConfigLayerChanged)
	a, roles := eventsApp(t, st)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ensured := 0
	var got []string
	done := make(chan error, 1)
	go func() {
		done <- a.FollowEvents(ctx, EventsQuery{Since: "v1:" + string(eventsRepo) + "=1", Types: []string{"git.**"}},
			func(context.Context) error { ensured++; return nil },
			func(e bus.Event, cursor string) error {
				got = append(got, cursor)
				if len(got) == 1 {
					cancel()
				}
				return nil
			})
	}()
	time.Sleep(50 * time.Millisecond)
	appendEvents(t, st, bus.TypeGitHeadChanged)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ensured != 1 || len(got) != 1 || got[0] != "v1:"+string(eventsRepo)+"=3" || (*roles)[0] != store.RoleReader {
		t.Fatalf("ensured %d, cursors %v, roles %v", ensured, got, *roles)
	}

	failing := errors.New("no daemon")
	if err := a.FollowEvents(context.Background(), EventsQuery{}, func(context.Context) error { return failing }, nil); !errors.Is(err, failing) {
		t.Fatalf("ensure failure = %v", err)
	}
}
