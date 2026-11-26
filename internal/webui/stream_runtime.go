package webui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/bustest"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/watch"
)

// Swap points for the phase 1 implementations that land in parallel lanes.
// Each is one line to change; the event store's is app.OpenEventStore.
var (
	// newEventBus builds the daemon's bus. Lane 1.1: return bus.New(opts).
	newEventBus = func(opts bus.Options) bus.Bus { return bustest.New(opts) }
	// newWatcher builds the daemon's watcher. Lane 1.3: return the watch
	// package's Watcher (fsnotify with the polling fallback).
	newWatcher = func() watch.Watcher { return idleWatcher{} }
	// openEventStore opens a repository's store. Lane 1.2 swaps
	// app.OpenEventStore, so the CLI and the daemon change together.
	openEventStore = func(ctx context.Context, commonDir string, role store.Role) (store.Store, error) {
		return app.OpenEventStore(ctx, commonDir, role)
	}
)

// idleWatcher produces no events. It stands in until lane 1.3's watcher.
type idleWatcher struct{}

func (idleWatcher) Add(watch.Target) error                             { return nil }
func (idleWatcher) Remove(bus.RepoID, bus.WorktreeID) error            { return nil }
func (idleWatcher) Mode(bus.RepoID, bus.WorktreeID) (watch.Mode, bool) { return "", false }
func (idleWatcher) Run(ctx context.Context, _ bus.Publisher) error     { <-ctx.Done(); return nil }

// eventRuntime is the daemon's event plumbing: one bus, one store per
// repository opened as store.RoleOwner, one projector per repository and
// one watcher for every registered worktree.
type eventRuntime struct {
	ctx     context.Context
	bus     bus.Bus
	watcher watch.Watcher
	wg      sync.WaitGroup

	mu    sync.Mutex
	repos map[string]*repoEvents // by registeredWorktree.CloneID
	byID  map[bus.RepoID]*repoEvents
}

func startEventRuntime(ctx context.Context) (*eventRuntime, func()) {
	ctx, cancel := context.WithCancel(ctx)
	rt := &eventRuntime{ctx: ctx, watcher: newWatcher(), repos: map[string]*repoEvents{}, byID: map[bus.RepoID]*repoEvents{}}
	rt.bus = newEventBus(bus.Options{Interceptors: []bus.Interceptor{store.Interceptor(rt.store)}})
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		if err := rt.watcher.Run(ctx, rt.bus); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "agentflow: event watcher stopped:", err)
		}
	}()
	return rt, func() {
		cancel()
		_ = rt.bus.Close()
		rt.wg.Wait()
		rt.mu.Lock()
		defer rt.mu.Unlock()
		for _, r := range rt.repos {
			_ = r.store.Close()
		}
	}
}

func (rt *eventRuntime) store(id bus.RepoID) (store.Store, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	r := rt.byID[id]
	if r == nil {
		return nil, false
	}
	return r.store, true
}

// attach makes sure w's repository has a store and a projector, and that
// the watcher follows w.
func (rt *eventRuntime) attach(w registeredWorktree) (*repoEvents, error) {
	rt.mu.Lock()
	r := rt.repos[w.CloneID]
	if r == nil {
		s, err := openEventStore(rt.ctx, w.Common, store.RoleOwner)
		if err != nil {
			rt.mu.Unlock()
			return nil, err
		}
		r = &repoEvents{id: s.Repo(), common: w.Common, store: s, worktrees: map[bus.WorktreeID]registeredWorktree{}}
		r.projector = newProjector(r.id, []Projection{&overviewProjection{repo: r}})
		sub, err := rt.bus.Subscribe(bus.SubscribeOptions{Name: "projector/" + string(r.id)}, bus.Topic{Repo: r.id, Worktree: bus.Any, Types: "**"})
		if err != nil {
			_ = s.Close()
			rt.mu.Unlock()
			return nil, err
		}
		rt.repos[w.CloneID] = r
		rt.byID[r.id] = r
		rt.wg.Add(1)
		go func() {
			defer rt.wg.Done()
			r.projector.run(rt.ctx, sub)
		}()
	}
	rt.mu.Unlock()
	r.addWorktree(w)
	target := watch.Target{Repo: r.id, Worktree: bus.WorktreeID(w.ID), Root: w.Root, GitDir: w.GitDir, CommonDir: w.Common}
	if err := rt.watcher.Add(target); err != nil {
		fmt.Fprintf(os.Stderr, "agentflow: cannot watch worktree %s: %v\n", w.ID, err)
	}
	return r, nil
}

// repoEvents is one repository's share of the event runtime.
type repoEvents struct {
	id        bus.RepoID
	common    string
	store     store.Store
	projector *projector

	mu        sync.Mutex
	worktrees map[bus.WorktreeID]registeredWorktree
}

func (r *repoEvents) addWorktree(w registeredWorktree) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.worktrees[bus.WorktreeID(w.ID)] = w
}

func (r *repoEvents) worktree(id bus.WorktreeID) (registeredWorktree, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.worktrees[id]
	return w, ok
}

func (r *repoEvents) worktreeIDs() []bus.WorktreeID {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]bus.WorktreeID, 0, len(r.worktrees))
	for id := range r.worktrees {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// EnsureDaemon makes sure the console daemon runs and serves the worktree
// containing dir, through the same start path as `agentflow ui --no-open`,
// without opening a browser. The daemon then owns the repository's event
// store and runs its watcher.
func EnsureDaemon(ctx context.Context, dir string) error {
	_, err := LaunchCLI(ctx, dir, 0, Destination{Screen: "overview"}, true)
	return err
}
