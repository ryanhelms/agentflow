package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
)

// sharedStore lets several opens of one fake store coexist: closing a member
// must not end the store for the test that still appends to it.
type sharedStore struct{ *storetest.Store }

func (sharedStore) Close() error { return nil }

const (
	repoB bus.RepoID = "b000000000000bbb"
	repoC bus.RepoID = "c000000000000ccc"
)

// storeSet fakes the event stores of one machine: one store per common git
// dir, plus the global store.
type storeSet struct {
	mu     sync.Mutex
	byDir  map[string]*storetest.Store
	global *storetest.Store
	roles  []store.Role
}

func newStoreSet(t *testing.T) *storeSet {
	t.Helper()
	set := &storeSet{byDir: map[string]*storetest.Store{}}
	oldRepo, oldGlobal := OpenEventStore, OpenGlobalEventStore
	OpenEventStore = func(_ context.Context, common string, role store.Role) (store.Store, error) {
		set.mu.Lock()
		defer set.mu.Unlock()
		set.roles = append(set.roles, role)
		if st := set.byDir[common]; st != nil {
			return sharedStore{st}, nil
		}
		return nil, fmt.Errorf("read event store repo-id: %w", os.ErrNotExist)
	}
	OpenGlobalEventStore = func(_ context.Context, role store.Role) (store.Store, error) {
		set.mu.Lock()
		defer set.mu.Unlock()
		set.roles = append(set.roles, role)
		if set.global == nil {
			return nil, os.ErrNotExist
		}
		return sharedStore{set.global}, nil
	}
	t.Cleanup(func() { OpenEventStore, OpenGlobalEventStore = oldRepo, oldGlobal })
	return set
}

func (s *storeSet) addRepo(dir string, id bus.RepoID) *storetest.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := storetest.New(id)
	s.byDir[dir] = st
	return st
}

func (s *storeSet) addGlobal() *storetest.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.global = storetest.New(bus.GlobalRepo)
	return s.global
}

func (s *storeSet) onlyReaders(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.roles) == 0 {
		t.Fatal("no store was opened")
	}
	for _, r := range s.roles {
		if r != store.RoleReader {
			t.Fatalf("opened a store as %v, want only RoleReader", r)
		}
	}
}

// emit appends one event at the given time. Worktree-scoped types go to the
// "main" worktree.
func emit(t *testing.T, st *storetest.Store, repo bus.RepoID, typ bus.Type, at time.Time, payload string) {
	t.Helper()
	e := bus.Event{Type: typ, Source: "test", Repo: repo, Time: at}
	if info, _ := bus.Lookup(typ); info.Scope == bus.ScopeWorktree {
		e.Worktree = "main"
	}
	if payload != "" {
		e.Payload = json.RawMessage(payload)
	}
	if _, err := st.Append(context.Background(), e.Normalize(at)); err != nil {
		t.Fatal(err)
	}
}

func commonOf(t *testing.T, a *App) string {
	t.Helper()
	dir, err := a.commonDir(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func newGitApp(t *testing.T) *App {
	t.Helper()
	a, _ := eventsApp(t, nil)
	return a
}

func TestEventsReadsGlobalWithTheRepository(t *testing.T) {
	a := newGitApp(t)
	set := newStoreSet(t)
	repo := set.addRepo(commonOf(t, a), eventsRepo)
	global := set.addGlobal()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	emit(t, repo, eventsRepo, bus.TypeGitHeadChanged, base, "")
	emit(t, global, bus.GlobalRepo, bus.TypeConfigLayerChanged, base.Add(time.Second), `{"layer":"personal"}`)
	emit(t, repo, eventsRepo, bus.TypeConfigLayerChanged, base.Add(2*time.Second), `{"layer":"repo"}`)
	emit(t, global, bus.GlobalRepo, bus.TypeClientSettingsChanged, base.Add(3*time.Second), `{"client":"claude"}`)
	ctx := context.Background()

	page, err := a.Events(ctx, EventsQuery{})
	if err != nil || len(page.Events) != 4 || page.Repo != eventsRepo {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if want := "v1:" + string(eventsRepo) + "=2,global=2"; page.Cursor != want {
		t.Fatalf("cursor = %q, want %q", page.Cursor, want)
	}
	var order []string
	for _, e := range page.Events {
		order = append(order, string(e.Repo)+"/"+string(e.Type))
	}
	if want := []string{string(eventsRepo) + "/git.head.changed", "global/config.layer.changed", string(eventsRepo) + "/config.layer.changed", "global/client.settings.changed"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want time order %v", order, want)
	}

	for name, c := range map[string]struct {
		q    EventsQuery
		want int
	}{
		"a type reaches both stores":          {EventsQuery{Types: []string{"config.**"}}, 2},
		"global may be named":                 {EventsQuery{Topics: []string{"global/*/**"}}, 2},
		"star means this repository":          {EventsQuery{Topics: []string{"*/main/git.**"}}, 1},
		"this repository by name":             {EventsQuery{Topics: []string{string(eventsRepo) + "/*/config.**"}}, 1},
		"a topic and a type are both applied": {EventsQuery{Topics: []string{"global/*/client.**"}, Types: []string{"git.**"}}, 2},
	} {
		got, err := a.Events(ctx, c.q)
		if err != nil || len(got.Events) != c.want {
			t.Errorf("%s: %d events, %v (want %d)", name, len(got.Events), err, c.want)
		}
	}

	// Resume from the cursor: only newer events, from either store.
	emit(t, global, bus.GlobalRepo, bus.TypeConfigLayerChanged, base.Add(4*time.Second), `{"layer":"personal"}`)
	next, err := a.Events(ctx, EventsQuery{Since: page.Cursor})
	if err != nil || len(next.Events) != 1 || next.Events[0].Repo != bus.GlobalRepo || next.Events[0].Seq != 3 || next.Cursor != "v1:"+string(eventsRepo)+"=2,global=3" {
		t.Fatalf("next = %+v, %v", next, err)
	}

	// Another repository is not readable without --all-repos.
	_, err = a.Events(ctx, EventsQuery{Topics: []string{string(repoB) + "/*/**"}})
	if err == nil || !strings.Contains(err.Error(), "--all-repos") {
		t.Fatalf("foreign topic err = %v", err)
	}
	set.onlyReaders(t)
}

func TestEventsAllReposUnitesTheServedRepositories(t *testing.T) {
	a := newGitApp(t)
	set := newStoreSet(t)
	self := commonOf(t, a)
	repoA := set.addRepo(self, eventsRepo)
	dirB, dirC := filepath.Join(t.TempDir(), "b.git"), filepath.Join(t.TempDir(), "c.git")
	stB, stC := set.addRepo(dirB, repoB), set.addRepo(dirC, repoC)
	global := set.addGlobal()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	emit(t, repoA, eventsRepo, bus.TypeGitHeadChanged, base, "")
	emit(t, stB, repoB, bus.TypeGitHeadChanged, base.Add(time.Second), "")
	emit(t, stC, repoC, bus.TypeGitRefsChanged, base.Add(2*time.Second), "")
	emit(t, global, bus.GlobalRepo, bus.TypeConfigLayerChanged, base.Add(3*time.Second), `{"layer":"personal"}`)
	emit(t, repoA, eventsRepo, bus.TypeGitHeadChanged, base.Add(4*time.Second), "")
	ctx := context.Background()
	// The daemon lists the current repository too; it must be read once.
	served := func(context.Context) ([]string, error) { return []string{dirC, self, dirB, dirB}, nil }

	// Without Served: this repository and global only.
	page, err := a.Events(ctx, EventsQuery{})
	if err != nil || len(page.Events) != 3 {
		t.Fatalf("default page = %+v, %v", page, err)
	}
	for _, e := range page.Events {
		if e.Repo == repoB || e.Repo == repoC {
			t.Fatalf("default read returned %s", e.Repo)
		}
	}

	page, err = a.Events(ctx, EventsQuery{Served: served})
	if err != nil || len(page.Events) != 5 || page.More {
		t.Fatalf("all-repos page = %+v, %v", page, err)
	}
	var repos []bus.RepoID
	for _, e := range page.Events {
		repos = append(repos, e.Repo)
	}
	if want := []bus.RepoID{eventsRepo, repoB, repoC, bus.GlobalRepo, eventsRepo}; !slices.Equal(repos, want) {
		t.Fatalf("repositories in time order = %v, want %v", repos, want)
	}
	if want := "v1:" + string(eventsRepo) + "=2," + string(repoB) + "=1," + string(repoC) + "=1,global=1"; page.Cursor != want {
		t.Fatalf("cursor = %q, want %q", page.Cursor, want)
	}

	// The cursor is a vector: a newer event in one repository is the only one returned.
	emit(t, stB, repoB, bus.TypeGitHeadChanged, base.Add(5*time.Second), "")
	next, err := a.Events(ctx, EventsQuery{Served: served, Since: page.Cursor})
	if err != nil || len(next.Events) != 1 || next.Events[0].Repo != repoB || next.Events[0].Seq != 2 {
		t.Fatalf("next = %+v, %v", next, err)
	}

	// With Served, * as the repository means every repository.
	page, err = a.Events(ctx, EventsQuery{Served: served, Topics: []string{"*/*/git.head.changed"}})
	if err != nil || len(page.Events) != 4 {
		t.Fatalf("star topic = %+v, %v", page, err)
	}
	page, err = a.Events(ctx, EventsQuery{Served: served, Topics: []string{string(repoC) + "/*/**"}, Types: []string{"config.**"}})
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("named repository and type = %+v, %v", page, err)
	}
	if _, err := a.Events(ctx, EventsQuery{Served: served, Topics: []string{"dddddddddddddddd/*/**"}}); err == nil || !strings.Contains(err.Error(), "does not serve") {
		t.Fatalf("unserved topic err = %v", err)
	}
	// A served repository without a store yet is left out, not an error.
	missing := func(context.Context) ([]string, error) {
		return []string{filepath.Join(t.TempDir(), "nothing.git")}, nil
	}
	if page, err := a.Events(ctx, EventsQuery{Served: missing}); err != nil || len(page.Events) != 3 {
		t.Fatalf("missing served store = %+v, %v", page, err)
	}
	set.onlyReaders(t)
}

func TestFollowEventsAllReposRebuildsWhenTheServedSetChanges(t *testing.T) {
	old := servedPoll
	servedPoll = 20 * time.Millisecond
	t.Cleanup(func() { servedPoll = old })
	a := newGitApp(t)
	set := newStoreSet(t)
	repoA := set.addRepo(commonOf(t, a), eventsRepo)
	dirB, dirC := filepath.Join(t.TempDir(), "b.git"), filepath.Join(t.TempDir(), "c.git")
	stB, stC := set.addRepo(dirB, repoB), set.addRepo(dirC, repoC)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	emit(t, repoA, eventsRepo, bus.TypeGitHeadChanged, base, "")
	emit(t, stB, repoB, bus.TypeGitHeadChanged, base.Add(time.Second), "")
	emit(t, stC, repoC, bus.TypeGitHeadChanged, base.Add(2*time.Second), "")

	var mu sync.Mutex
	dirs := []string{dirB}
	served := func(context.Context) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(dirs), nil
	}
	var got []string
	seen := func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(got) }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- a.FollowEvents(ctx, EventsQuery{Served: served}, nil, func(e bus.Event, cursor string) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, fmt.Sprintf("%s=%d", e.Repo, e.Seq))
			return nil
		})
	}()
	waitFor(t, "the first two repositories", func() bool { return len(seen()) == 2 })

	// The daemon starts serving C: its history appears once, and nothing
	// already printed is printed again.
	mu.Lock()
	dirs = []string{dirB, dirC}
	mu.Unlock()
	waitFor(t, "the third repository", func() bool { return len(seen()) == 3 })
	emit(t, stB, repoB, bus.TypeGitHeadChanged, base.Add(3*time.Second), "")
	emit(t, stC, repoC, bus.TypeGitHeadChanged, base.Add(4*time.Second), "")
	waitFor(t, "new events from both", func() bool { return len(seen()) == 5 })
	// Give a duplicate time to show up.
	time.Sleep(150 * time.Millisecond)

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	have := seen()
	slices.Sort(have)
	want := []string{string(eventsRepo) + "=1", string(repoB) + "=1", string(repoB) + "=2", string(repoC) + "=1", string(repoC) + "=2"}
	slices.Sort(want)
	if !slices.Equal(have, want) {
		t.Fatalf("events = %v, want each exactly once: %v", have, want)
	}
}

func TestFollowEventsNeedsTheRepositoryStore(t *testing.T) {
	a := newGitApp(t)
	newStoreSet(t)
	err := a.FollowEvents(context.Background(), EventsQuery{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "has not created") {
		t.Fatalf("err = %v", err)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
