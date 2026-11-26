package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
)

// Event page sizes.
const (
	DefaultEventsLimit = 100
	MaxEventsLimit     = store.MaxReplay
)

// EventsQuery selects stored events of the current repository and of global
// (the user-level events: personal configuration and client settings).
type EventsQuery struct {
	// Since is a cursor from an earlier page or delta frame; empty starts at
	// the oldest retained event.
	Since string `json:"since,omitempty"`
	// Types are event type patterns such as git.** or config.layer.changed.
	Types []string `json:"types,omitempty"`
	// Topics are full topics, <repo>/<worktree>/<pattern>; * as the repo
	// means this repository (every repository when Served is set). A topic
	// may name global. Another repository's ID is accepted only with Served.
	Topics []string `json:"topics,omitempty"`
	// Limit caps the page; 0 means DefaultEventsLimit.
	Limit int `json:"limit,omitempty"`
	// Served, when set, widens the read from this repository and global to
	// every repository it lists as well (agentflow events --all-repos). It
	// returns the common git dirs of the repositories the console daemon
	// serves. It is not part of the MCP tool's input: agents work in one
	// repository.
	Served func(context.Context) ([]string, error) `json:"-"`
}

// EventsPage is one page of stored events and the cursor to resume from.
type EventsPage struct {
	// Repo is the repository the call ran in, when it has an event store.
	// Every event carries its own repository.
	Repo   bus.RepoID  `json:"repo,omitempty"`
	Events []bus.Event `json:"events"`
	Cursor string      `json:"cursor"`
	// More is true when the page stopped at Limit before the newest event.
	More bool `json:"more"`
}

// eventFilter is a parsed EventsQuery.
type eventFilter struct {
	since  bus.Cursor
	topics []bus.Topic
	limit  int
}

// eventScope says which repositories an events call may name.
type eventScope struct {
	current bus.RepoID // the repository the call runs in; "" without a store
	all     bool       // every served repository, not only current and global
	known   map[bus.RepoID]bool
}

// topics returns the topics that select pattern p across the scope.
func (sc eventScope) topics(p bus.Pattern) []bus.Topic {
	if sc.all {
		return []bus.Topic{{Repo: bus.Any, Worktree: bus.Any, Types: p}}
	}
	var out []bus.Topic
	if sc.current != "" {
		out = append(out, bus.Topic{Repo: sc.current, Worktree: bus.Any, Types: p})
	}
	return append(out, bus.Topic{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: p})
}

// check rejects a topic that names a repository outside the scope.
func (sc eventScope) check(repo bus.RepoID, raw string) error {
	switch {
	case repo == bus.Any, repo == bus.GlobalRepo, repo == sc.current:
		return nil
	case sc.all:
		if sc.known == nil || sc.known[repo] {
			return nil
		}
		return fmt.Errorf("topic %q names a repository the console daemon does not serve", raw)
	case sc.current == "":
		return nil // no store here: check the syntax only
	}
	return fmt.Errorf("topic %q names another repository; run agentflow events in that repository, or add --all-repos in a terminal", raw)
}

// parseEventsQuery checks q against sc.
func parseEventsQuery(q EventsQuery, sc eventScope) (eventFilter, error) {
	since, err := bus.ParseCursor(q.Since)
	if err != nil {
		return eventFilter{}, err
	}
	f := eventFilter{since: since, limit: q.Limit}
	switch {
	case f.limit == 0:
		f.limit = DefaultEventsLimit
	case f.limit < 0 || f.limit > MaxEventsLimit:
		return eventFilter{}, fmt.Errorf("limit must be between 1 and %d", MaxEventsLimit)
	}
	for _, raw := range q.Topics {
		t, err := bus.ParseTopic(raw)
		if err != nil {
			return eventFilter{}, err
		}
		if t.Repo == bus.Any && !sc.all {
			t.Repo = sc.current
		}
		if err := sc.check(t.Repo, raw); err != nil {
			return eventFilter{}, err
		}
		f.topics = append(f.topics, t)
	}
	for _, raw := range q.Types {
		p, err := bus.ParsePattern(raw)
		if err != nil {
			return eventFilter{}, err
		}
		f.topics = append(f.topics, sc.topics(p)...)
	}
	if len(f.topics) == 0 {
		f.topics = sc.topics("**")
	}
	return f, nil
}

func (a *App) commonDir(ctx context.Context) (string, error) {
	out, err := a.Repo.Git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return resolveDir(strings.TrimSpace(out)), nil
}

func resolveDir(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return dir
}

// openRepoStore opens the store under common as a reader. ok is false when
// no event has been recorded there yet.
func openRepoStore(ctx context.Context, common string) (store.Store, bool, error) {
	s, err := OpenEventStore(ctx, common, store.RoleReader)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open event store: %w", err)
	}
	return s, true, nil
}

// openGlobalEvents opens the global store as a reader. ok is false when the
// console daemon has not created it.
func openGlobalEvents(ctx context.Context) (store.Store, bool, error) {
	s, err := OpenGlobalEventStore(ctx, store.RoleReader)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open global event store: %w", err)
	}
	return s, true, nil
}

func expiredHint(err error) error {
	if errors.Is(err, store.ErrCursorExpired) {
		return fmt.Errorf("%w; start again without --since (MCP: without since)", err)
	}
	return err
}

// servedPoll is how often a following --all-repos call looks for repositories
// the daemon started or stopped serving. Tests shorten it.
var servedPoll = 5 * time.Second

// eventMembers are the stores one events call reads together.
type eventMembers struct {
	stores  []store.Store
	current bus.RepoID // this repository, "" when it has no store yet
	served  []string   // the served common dirs the stores were opened for
}

func (m *eventMembers) close() {
	for _, s := range m.stores {
		_ = s.Close()
	}
}

// add keeps s unless a store of the same repository is already open.
func (m *eventMembers) add(s store.Store) {
	for _, have := range m.stores {
		if have.Repo() == s.Repo() {
			_ = s.Close()
			return
		}
	}
	m.stores = append(m.stores, s)
}

func (m *eventMembers) scope(all bool) eventScope {
	sc := eventScope{current: m.current, all: all}
	if len(m.stores) > 0 {
		sc.known = map[bus.RepoID]bool{}
		for _, s := range m.stores {
			sc.known[s.Repo()] = true
		}
	}
	return sc
}

// repos lists the repositories the member stores hold, sorted.
func (m *eventMembers) repos() []bus.RepoID {
	ids := make([]bus.RepoID, len(m.stores))
	for i, s := range m.stores {
		ids[i] = s.Repo()
	}
	slices.Sort(ids)
	return ids
}

func (m *eventMembers) readers() []store.Reader {
	rs := make([]store.Reader, len(m.stores))
	for i, s := range m.stores {
		rs[i] = s
	}
	return rs
}

// reader reads the member stores as one.
func (m *eventMembers) reader() store.Reader {
	if len(m.stores) == 1 {
		return m.stores[0]
	}
	return store.Union(m.readers()...)
}

// follower follows the member stores as one.
func (m *eventMembers) follower() (store.Follower, error) {
	if len(m.stores) == 1 {
		f, ok := m.stores[0].(store.Follower)
		if !ok {
			return nil, errors.New("this event store cannot follow new events")
		}
		return f, nil
	}
	return store.Union(m.readers()...), nil
}

// servedDirs lists the served repositories' common dirs other than common:
// resolved, deduplicated and sorted, so two lists compare with slices.Equal.
func servedDirs(ctx context.Context, served func(context.Context) ([]string, error), common string) ([]string, error) {
	dirs, err := served(ctx)
	if err != nil {
		return nil, fmt.Errorf("list served repositories: %w", err)
	}
	seen := map[string]bool{common: true}
	var out []string
	for _, d := range dirs {
		d = resolveDir(d)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out, nil
}

// openMembers opens, as readers, this repository's store, the global store
// and, when served is set, the store of every served repository. A store
// that does not exist yet is left out.
func (a *App) openMembers(ctx context.Context, served func(context.Context) ([]string, error)) (m *eventMembers, err error) {
	m = &eventMembers{}
	defer func() {
		if err != nil {
			m.close()
			m = nil
		}
	}()
	common, err := a.commonDir(ctx)
	if err != nil {
		return m, err
	}
	s, ok, err := openRepoStore(ctx, common)
	if err != nil {
		return m, err
	}
	if ok {
		m.current = s.Repo()
		m.add(s)
	}
	g, ok, err := openGlobalEvents(ctx)
	if err != nil {
		return m, err
	}
	if ok {
		m.add(g)
	}
	if served != nil {
		if m.served, err = servedDirs(ctx, served, common); err != nil {
			return m, err
		}
		for _, dir := range m.served {
			s, ok, err := openRepoStore(ctx, dir)
			if err != nil {
				return m, err
			}
			if ok {
				m.add(s)
			}
		}
	}
	if len(m.stores) > bus.MaxCursorRepos {
		return m, fmt.Errorf("more than %d event stores; narrow the read to fewer repositories", bus.MaxCursorRepos)
	}
	return m, nil
}

// Events returns one page of stored events after q.Since: this repository's
// and global's, or with q.Served every served repository's too. It never
// writes and never takes a store's owner lock.
func (a *App) Events(ctx context.Context, q EventsQuery) (EventsPage, error) {
	m, err := a.openMembers(ctx, q.Served)
	if err != nil {
		return EventsPage{}, err
	}
	defer m.close()
	f, err := parseEventsQuery(q, m.scope(q.Served != nil))
	if err != nil {
		// Malformed input is reported even without a store, so a typo is
		// not mistaken for "no events".
		return EventsPage{}, err
	}
	if len(m.stores) == 0 {
		return EventsPage{Events: []bus.Event{}, Cursor: q.Since}, nil
	}
	page := EventsPage{Repo: m.current, Events: []bus.Event{}}
	cursor := f.since.Clone()
	for e, err := range m.reader().ReadFrom(ctx, f.since) {
		if err != nil {
			return EventsPage{}, expiredHint(err)
		}
		if bus.MatchAny(f.topics, e) {
			if len(page.Events) == f.limit {
				page.More = true
				break
			}
			page.Events = append(page.Events, e)
		}
		cursor = cursor.Advance(e)
	}
	page.Cursor = cursor.String()
	return page, nil
}

// FollowEvents prints, through emit, the stored events after q.Since and
// then every new one until ctx ends. ensureDaemon starts the console daemon
// first when it is not running: the daemon owns the stores and runs the
// watcher, and this call only reads (store.RoleReader). Limit is ignored.
//
// With q.Served set, it re-reads the served repositories every few seconds
// and, when the set changes, rebuilds the combined reader from the cursor
// it has reached, so no event is repeated or skipped.
func (a *App) FollowEvents(ctx context.Context, q EventsQuery, ensureDaemon func(context.Context) error, emit func(e bus.Event, cursor string) error) error {
	if ensureDaemon != nil {
		if err := ensureDaemon(ctx); err != nil {
			return fmt.Errorf("start the console daemon: %w", err)
		}
	}
	m, err := a.openMembers(ctx, q.Served)
	if err != nil {
		return err
	}
	if m.current == "" {
		m.close()
		return errors.New("the console daemon has not created this repository's event store; run agentflow ui --status")
	}
	q.Limit = 0
	f, err := parseEventsQuery(q, m.scope(q.Served != nil))
	if err != nil {
		m.close()
		return err
	}
	common, err := a.commonDir(ctx)
	if err != nil {
		m.close()
		return err
	}
	cursor := f.since.Clone()
	interval := servedPoll
	for {
		fctx, cancel := context.WithCancel(ctx)
		var rebuild atomic.Bool
		if q.Served != nil {
			go watchServed(fctx, interval, q.Served, common, m.served, func() {
				rebuild.Store(true)
				cancel()
			})
		}
		err := m.follow(fctx, f.topics, &cursor, emit)
		cancel()
		m.close()
		if err != nil {
			return err
		}
		if ctx.Err() != nil || !rebuild.Load() {
			return nil
		}
		if m, err = a.openMembers(ctx, q.Served); err != nil {
			return err
		}
	}
}

// follow emits the matching events after *cursor, moving *cursor forward,
// until ctx ends. A context error is the normal end, not a failure.
func (m *eventMembers) follow(ctx context.Context, topics []bus.Topic, cursor *bus.Cursor, emit func(bus.Event, string) error) error {
	follower, err := m.follower()
	if err != nil {
		return err
	}
	for e, err := range follower.Follow(ctx, *cursor) {
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return expiredHint(err)
		}
		*cursor = cursor.Advance(e)
		if !bus.MatchAny(topics, e) {
			continue
		}
		if err := emit(e, cursor.String()); err != nil {
			return err
		}
	}
	return nil
}

// watchServed calls changed once when the served repositories differ from
// have. It returns when ctx ends or after calling changed.
func watchServed(ctx context.Context, interval time.Duration, served func(context.Context) ([]string, error), common string, have []string, changed func()) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		dirs, err := servedDirs(ctx, served, common)
		if err == nil && !slices.Equal(dirs, have) {
			changed()
			return
		}
	}
}
