package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
)

// Event page sizes.
const (
	DefaultEventsLimit = 100
	MaxEventsLimit     = store.MaxReplay
)

// EventsQuery selects stored events of the current repository.
type EventsQuery struct {
	// Since is a cursor from an earlier page or delta frame; empty starts at
	// the oldest retained event.
	Since string `json:"since,omitempty"`
	// Types are event type patterns such as git.** or config.layer.changed.
	Types []string `json:"types,omitempty"`
	// Topics are full topics, <repo>/<worktree>/<pattern>; * as the repo
	// means this repository.
	Topics []string `json:"topics,omitempty"`
	// Limit caps the page; 0 means DefaultEventsLimit.
	Limit int `json:"limit,omitempty"`
}

// EventsPage is one page of stored events and the cursor to resume from.
type EventsPage struct {
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

// parseEventsQuery checks q against repo. An empty repo checks only syntax.
func parseEventsQuery(q EventsQuery, repo bus.RepoID) (eventFilter, error) {
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
		if t.Repo == bus.Any {
			t.Repo = repo
		}
		if repo != "" && t.Repo != repo {
			return eventFilter{}, fmt.Errorf("topic %q names another repository; run agentflow events in that repository", raw)
		}
		f.topics = append(f.topics, t)
	}
	for _, raw := range q.Types {
		p, err := bus.ParsePattern(raw)
		if err != nil {
			return eventFilter{}, err
		}
		f.topics = append(f.topics, bus.Topic{Repo: repo, Worktree: bus.Any, Types: p})
	}
	if len(f.topics) == 0 {
		f.topics = []bus.Topic{{Repo: repo, Worktree: bus.Any, Types: "**"}}
	}
	return f, nil
}

func (a *App) commonDir(ctx context.Context) (string, error) {
	out, err := a.Repo.Git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(out)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir, nil
}

// openEvents opens this repository's store as a reader. ok is false when no
// event has been recorded here yet.
func (a *App) openEvents(ctx context.Context) (store.Store, bool, error) {
	common, err := a.commonDir(ctx)
	if err != nil {
		return nil, false, err
	}
	s, err := OpenEventStore(ctx, common, store.RoleReader)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open event store: %w", err)
	}
	return s, true, nil
}

func expiredHint(err error) error {
	if errors.Is(err, store.ErrCursorExpired) {
		return fmt.Errorf("%w; start again without --since (MCP: without since)", err)
	}
	return err
}

// Events returns one page of this repository's stored events after
// q.Since. It never writes and never takes the store's owner lock.
func (a *App) Events(ctx context.Context, q EventsQuery) (EventsPage, error) {
	s, ok, err := a.openEvents(ctx)
	if err != nil {
		return EventsPage{}, err
	}
	if !ok {
		// Still reject malformed input, so a typo is not reported as "no events".
		if _, err := parseEventsQuery(q, ""); err != nil {
			return EventsPage{}, err
		}
		return EventsPage{Events: []bus.Event{}, Cursor: q.Since}, nil
	}
	defer s.Close()
	f, err := parseEventsQuery(q, s.Repo())
	if err != nil {
		return EventsPage{}, err
	}
	page := EventsPage{Repo: s.Repo(), Events: []bus.Event{}}
	cursor := f.since.Clone()
	for e, err := range s.ReadFrom(ctx, f.since) {
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
// first when it is not running: the daemon owns the store and runs the
// watcher, and this call only reads (store.RoleReader). Limit is ignored.
func (a *App) FollowEvents(ctx context.Context, q EventsQuery, ensureDaemon func(context.Context) error, emit func(e bus.Event, cursor string) error) error {
	if ensureDaemon != nil {
		if err := ensureDaemon(ctx); err != nil {
			return fmt.Errorf("start the console daemon: %w", err)
		}
	}
	s, ok, err := a.openEvents(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the console daemon has not created this repository's event store; run agentflow ui --status")
	}
	defer s.Close()
	follower, ok := s.(store.Follower)
	if !ok {
		return errors.New("this event store cannot follow new events")
	}
	q.Limit = 0
	f, err := parseEventsQuery(q, s.Repo())
	if err != nil {
		return err
	}
	cursor := f.since.Clone()
	for e, err := range follower.Follow(ctx, f.since) {
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return expiredHint(err)
		}
		cursor = cursor.Advance(e)
		if !bus.MatchAny(f.topics, e) {
			continue
		}
		if err := emit(e, cursor.String()); err != nil {
			return err
		}
	}
	return nil
}
