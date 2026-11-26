package webui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/watch"
)

// The daemon's event plumbing. Tests replace these to inject fakes.
var (
	// newEventBus builds the daemon's in-process bus.
	newEventBus = bus.New
	// newWatcher builds the daemon's watcher: file notifications with the
	// polling fallback, translating git and config changes into events.
	newWatcher = func() (watch.Watcher, error) {
		return watch.New(watch.Options{
			Translator: watch.NewTranslator(),
			OnError: func(err error) {
				fmt.Fprintln(os.Stderr, "agentflow: event watcher:", err)
			},
		})
	}
	// openEventStore opens a repository's store, through the same
	// app.OpenEventStore the CLI uses.
	openEventStore = func(ctx context.Context, commonDir string, role store.Role) (store.Store, error) {
		return app.OpenEventStore(ctx, commonDir, role)
	}
)

// runtimeExtension is a phase 2 source or sink that runs with the event
// runtime: the user-level watcher and approval translator (lane 2.1), the
// notify socket (lane 2.2) and the GitHub poller (lane 2.3). Each lane
// registers a factory in runtimeExtensions from its own file's init, so
// lanes never edit this file to start their part. See the ADR, "Phase 2,
// File ownership".
type runtimeExtension interface {
	// start runs once, after the bus exists and before any worktree is
	// attached. Long-running work uses rt.ctx and rt.wg.
	start(rt *eventRuntime) error
	// attached is called after w joined repository r (a new repository or
	// a new worktree of a known one), outside rt.mu.
	attached(r *repoEvents, w registeredWorktree)
	// stop runs once, before the bus closes, in reverse start order.
	stop()
}

// runtimeExtensions are the factories startEventRuntime starts, in order.
var runtimeExtensions []func() runtimeExtension

// eventRuntime is the daemon's event plumbing: one bus, one store per
// repository opened as store.RoleOwner, one projector per repository and
// one watcher for every registered worktree.
type eventRuntime struct {
	ctx     context.Context
	bus     bus.Bus
	watcher watch.Watcher
	wg      sync.WaitGroup
	exts    []runtimeExtension

	mu    sync.Mutex
	repos map[string]*repoEvents // by registeredWorktree.CloneID
	byID  map[bus.RepoID]*repoEvents
}

func startEventRuntime(ctx context.Context) (*eventRuntime, func(), error) {
	w, err := newWatcher()
	if err != nil {
		return nil, nil, fmt.Errorf("start event watcher: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	rt := &eventRuntime{ctx: ctx, watcher: w, repos: map[string]*repoEvents{}, byID: map[bus.RepoID]*repoEvents{}}
	rt.bus = newEventBus(bus.Options{Interceptors: []bus.Interceptor{store.Interceptor(rt.store)}})
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		if err := rt.watcher.Run(ctx, rt.bus); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "agentflow: event watcher stopped:", err)
		}
	}()
	for _, factory := range runtimeExtensions {
		ext := factory()
		// A failed extension costs only its own events; the console and
		// the git and configuration events keep working.
		if err := ext.start(rt); err != nil {
			fmt.Fprintln(os.Stderr, "agentflow: event source:", err)
			continue
		}
		rt.exts = append(rt.exts, ext)
	}
	return rt, func() {
		for i := len(rt.exts) - 1; i >= 0; i-- {
			rt.exts[i].stop()
		}
		cancel()
		_ = rt.bus.Close()
		rt.wg.Wait()
		rt.mu.Lock()
		defer rt.mu.Unlock()
		for _, r := range rt.repos {
			_ = r.store.Close()
		}
	}, nil
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
	if !r.addWorktree(w) {
		return r, nil // already watched, for example under another host name
	}
	target := watch.Target{Repo: r.id, Worktree: bus.WorktreeID(w.ID), Root: w.Root, GitDir: w.GitDir, CommonDir: w.Common}
	if err := rt.watcher.Add(target); err != nil {
		fmt.Fprintf(os.Stderr, "agentflow: cannot watch worktree %s: %v\n", w.ID, err)
	}
	for _, ext := range rt.exts {
		ext.attached(r, w)
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

// addWorktree records w and reports whether it is new.
func (r *repoEvents) addWorktree(w registeredWorktree) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, seen := r.worktrees[bus.WorktreeID(w.ID)]
	r.worktrees[bus.WorktreeID(w.ID)] = w
	return !seen
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
