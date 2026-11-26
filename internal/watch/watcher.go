package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// EnvMode is the environment variable that forces a mode: AGENTFLOW_WATCH=poll
// makes every Target poll.
const EnvMode = "AGENTFLOW_WATCH"

// Options configures New. Zero fields take the package defaults.
type Options struct {
	// Mode forces a mode. Empty reads AGENTFLOW_WATCH; anything but
	// ModePoll means ModeNotify with per-Target fallback to polling.
	Mode Mode
	// Quiet and MaxDelay are the Aggregator's timings.
	Quiet, MaxDelay time.Duration
	// PollInterval and PollRefsInterval are the polling Source's timings.
	PollInterval, PollRefsInterval time.Duration
	// Translator turns Batches into events; nil means NewTranslator().
	// When it is a Tracker, Add and Remove call Track and Untrack.
	Translator Translator
	// OnError receives non-fatal errors: a Translator or Publish failure, a
	// Source error. Nil discards them.
	OnError func(error)
	// NewNotify creates the ModeNotify Source; nil means NewNotifySource.
	// Tests use it to inject failures.
	NewNotify func() (Source, error)
}

// FileWatcher is the phase 1 Watcher. It shares one notify Source and one
// polling Source between all Targets, watches each repository's
// repository-wide directories once, debounces Changes per worktree (and per
// repository for refs and worktrees) and publishes what the Translator
// makes of them.
type FileWatcher struct {
	opts   Options
	notify Source // nil when notifications are unavailable or polling is forced
	poll   Source
	agg    *Aggregator
	tr     Translator
	wake   chan struct{}

	mu      sync.Mutex
	targets map[Key]*targetState
	repos   map[bus.RepoID]*repoState
	user    *userState // the user-level group, nil until AddUser
	want    map[string]*wantDir
	running bool
	closed  bool
}

var _ Watcher = (*FileWatcher)(nil)

// group is a set of watched Paths that share one mode: a worktree's own
// paths, or a repository's repository-wide paths. Falling back to polling
// moves the whole group.
type group struct {
	mode  Mode
	roots []Path
	// recursive lists the effective recursive roots (refs/, or only
	// refs/heads when a polled refs/ is too large).
	recursive []string
	// dirs holds every directory the group acquired: roots and the
	// subdirectories of recursive roots.
	dirs map[string]bool
}

type targetState struct {
	t Target
	g *group
}

type repoState struct {
	t     Target // Repo and CommonDir only
	g     *group
	users int
}

// wantDir reference-counts the groups that want one directory in each
// mode, and records where it is actually watched. A directory wanted in
// ModeNotify that does not exist yet is polled until it appears.
type wantDir struct {
	notify, poll     int
	inNotify, inPoll bool
}

// New returns a FileWatcher. It fails only when the polling Source cannot
// start; a missing notification backend means every Target polls.
func New(opts Options) (*FileWatcher, error) {
	if opts.Mode == "" && os.Getenv(EnvMode) == string(ModePoll) {
		opts.Mode = ModePoll
	}
	if opts.Translator == nil {
		opts.Translator = NewTranslator()
	}
	if opts.OnError == nil {
		opts.OnError = func(error) {}
	}
	if opts.NewNotify == nil {
		opts.NewNotify = NewNotifySource
	}
	w := &FileWatcher{opts: opts, agg: NewAggregator(opts.Quiet, opts.MaxDelay), tr: opts.Translator,
		wake: make(chan struct{}, 1), targets: map[Key]*targetState{}, repos: map[bus.RepoID]*repoState{},
		want: map[string]*wantDir{}}
	w.poll = NewPollSource(PollOptions{Interval: opts.PollInterval, SlowInterval: opts.PollRefsInterval, Slow: w.slowDir})
	if opts.Mode != ModePoll {
		n, err := opts.NewNotify()
		if err != nil {
			opts.OnError(fmt.Errorf("file notifications unavailable, polling: %w", err))
		} else {
			w.notify = n
		}
	}
	return w, nil
}

// slowDir reports whether the polling Source scans dir only every
// PollRefsInterval: everything under a refs/ directory.
func (w *FileWatcher) slowDir(dir string) bool {
	sep := string(filepath.Separator)
	return strings.Contains(dir+sep, sep+"refs"+sep)
}

// Add implements Watcher.
func (w *FileWatcher) Add(t Target) error {
	if !t.Repo.Valid() || t.Repo == bus.GlobalRepo || t.Worktree == "" || !t.Worktree.Valid() ||
		t.Root == "" || t.GitDir == "" || t.CommonDir == "" {
		return fmt.Errorf("watch: invalid target %+v", t)
	}
	t, err := canonical(t)
	if err != nil {
		return err
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errors.New("watch: watcher closed")
	}
	if _, ok := w.targets[KeyOf(t)]; ok {
		w.mu.Unlock()
		return fmt.Errorf("watch: %s/%s is already watched", t.Repo, t.Worktree)
	}
	r, ok := w.repos[t.Repo]
	if !ok {
		r = &repoState{t: Target{Repo: t.Repo, CommonDir: t.CommonDir}, g: w.newGroup(RepoPaths(t.CommonDir))}
		if err := w.start(r.g, t.CommonDir); err != nil {
			w.mu.Unlock()
			return err
		}
		w.repos[t.Repo] = r
	}
	ts := &targetState{t: t, g: w.newGroup(WorktreePaths(t))}
	if err := w.start(ts.g, t.CommonDir); err != nil {
		if !ok {
			w.releaseGroup(r.g)
			delete(w.repos, t.Repo)
		}
		w.mu.Unlock()
		return err
	}
	r.users++
	w.targets[KeyOf(t)] = ts
	w.mu.Unlock()

	// Watch first, then record the baseline: a change in between is
	// reported against the new state, never lost.
	if tr, ok := w.tr.(Tracker); ok {
		if err := tr.Track(context.Background(), t); err != nil {
			w.opts.OnError(err)
		}
	}
	return nil
}

// Remove implements Watcher.
func (w *FileWatcher) Remove(repo bus.RepoID, worktree bus.WorktreeID) error {
	k := Key{Repo: repo, Worktree: worktree}
	w.mu.Lock()
	ts, ok := w.targets[k]
	if !ok {
		w.mu.Unlock()
		return fmt.Errorf("watch: %s/%s is not watched", repo, worktree)
	}
	w.releaseGroup(ts.g)
	delete(w.targets, k)
	w.agg.Drop(k)
	if r := w.repos[repo]; r != nil {
		if r.users--; r.users <= 0 {
			w.releaseGroup(r.g)
			delete(w.repos, repo)
			w.agg.Drop(Key{Repo: repo})
		}
	}
	w.mu.Unlock()
	if tr, ok := w.tr.(Tracker); ok {
		tr.Untrack(ts.t)
	}
	return nil
}

// Mode implements Watcher. A worktree polls when its own paths or its
// repository's paths fell back to polling.
func (w *FileWatcher) Mode(repo bus.RepoID, worktree bus.WorktreeID) (Mode, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ts, ok := w.targets[Key{Repo: repo, Worktree: worktree}]
	if !ok {
		return "", false
	}
	if ts.g.mode == ModePoll {
		return ModePoll, true
	}
	if r := w.repos[repo]; r != nil && r.g.mode == ModePoll {
		return ModePoll, true
	}
	return ModeNotify, true
}

// Run implements Watcher. It returns nil when ctx is done and closes the
// Sources; a FileWatcher runs once.
func (w *FileWatcher) Run(ctx context.Context, pub bus.Publisher) error {
	w.mu.Lock()
	if w.running || w.closed {
		w.mu.Unlock()
		return errors.New("watch: Run called twice or after Close")
	}
	w.running = true
	w.mu.Unlock()
	defer func() { _ = w.Close() }()

	var nChanges <-chan Change
	var nErrs <-chan error
	if w.notify != nil {
		nChanges, nErrs = w.notify.Changes(), w.notify.Errors()
	}
	pChanges, pErrs := w.poll.Changes(), w.poll.Errors()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		next, ok := w.agg.Next()
		if d, has := w.nextTimed(); has && (!ok || d.Before(next)) {
			next, ok = d, true
		}
		if ok {
			timer.Reset(time.Until(next))
		} else {
			timer.Stop()
		}
		select {
		case <-ctx.Done():
			return nil
		case c, ok := <-nChanges:
			if !ok {
				nChanges = nil
				continue
			}
			w.handleChange(c, ModeNotify)
		case c, ok := <-pChanges:
			if !ok {
				pChanges = nil
				continue
			}
			w.handleChange(c, ModePoll)
		case err, ok := <-nErrs:
			if !ok {
				nErrs = nil
				continue
			}
			w.handleError(err)
		case err, ok := <-pErrs:
			if !ok {
				pErrs = nil
				continue
			}
			w.handleError(err)
		case <-timer.C:
		case <-w.wake:
		}
		w.flush(ctx, pub)
	}
}

// Close stops both Sources. Run calls it when it returns.
func (w *FileWatcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()
	var errs []error
	if w.notify != nil {
		errs = append(errs, w.notify.Close())
	}
	errs = append(errs, w.poll.Close())
	return errors.Join(errs...)
}

// Rescan makes every Target compare its whole state again, as after a
// queue overflow. Changes that change nothing produce no events, except
// git.refs.changed with empty refs ("rescan").
func (w *FileWatcher) Rescan() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range w.repos {
		w.rescanRepo(r)
	}
	for _, ts := range w.targets {
		w.rescanTarget(ts)
	}
	w.rescanUser()
	w.kick()
}

// flush translates and publishes every due Batch, then the events the user
// Translator has due by time.
func (w *FileWatcher) flush(ctx context.Context, pub bus.Publisher) {
	now := time.Now()
	for _, b := range w.agg.Due(now) {
		tr := w.tr
		if b.Target.Repo == bus.GlobalRepo {
			if tr = w.userTranslator(); tr == nil {
				continue
			}
		}
		evs, err := tr.Translate(ctx, b)
		if err != nil {
			w.opts.OnError(fmt.Errorf("translate %s/%s: %w", b.Target.Repo, b.Target.Worktree, err))
		}
		w.publish(ctx, pub, evs)
	}
	w.tick(ctx, pub, now)
}

func (w *FileWatcher) handleError(err error) {
	if !errors.Is(err, ErrOverflow) {
		w.opts.OnError(fmt.Errorf("watch: %w", err))
	}
	w.Rescan()
}

// handleChange keeps the watched directories in step with the file system
// (a directory that appeared, a removed one, a new refs/ subdirectory),
// then routes the change and any changes it implies to the Aggregator.
func (w *FileWatcher) handleChange(c Change, from Mode) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	changes := []Change{c}
	fi, statErr := os.Lstat(c.Path)
	isDir := statErr == nil && fi.IsDir()

	// A wanted directory appeared: move it from polling to notifications.
	if wd, ok := w.want[c.Path]; ok && isDir && c.Kind != Removed && wd.notify > 0 && !wd.inNotify && w.notify != nil {
		if err := w.reconcile(c.Path); err != nil {
			w.fallbackDir(c.Path)
		} else if wd.inNotify && !w.recursiveRoot(c.Path) {
			changes = append(changes, listChanges(c.Path, c.Time)...)
		}
	}

	// A watched directory went away.
	if c.Kind == Removed && errors.Is(statErr, fs.ErrNotExist) {
		w.dirGone(c.Path, from)
	}

	// A new directory under a recursive root (refs/heads/feature/).
	if isDir && c.Kind != Removed {
		changes = append(changes, w.growRecursive(c.Path, c.Time)...)
	}

	now := time.Now()
	for _, ch := range changes {
		w.route(ch, now)
	}
	w.kick()
}

// route adds c to the batch of every Key it concerns: the worktree whose
// git dir or configuration it touches, or the repository for
// repository-wide types.
func (w *FileWatcher) route(c Change, now time.Time) {
	repoDone := map[bus.RepoID]bool{}
	// refs/ or worktrees/ itself appearing or going (git removes an empty
	// worktrees/ with the last linked worktree) concerns the repository,
	// though Classify maps only the entries below them.
	for id, r := range w.repos {
		if c.Path == filepath.Join(r.t.CommonDir, "worktrees") || c.Path == filepath.Join(r.t.CommonDir, "refs") {
			repoDone[id] = true
			w.agg.Add(r.t, c, now)
		}
	}
	if us := w.user; us != nil && us.concerns(c.Path) {
		w.agg.Add(us.t, c, now)
	}
	for _, ts := range w.targets {
		typ, ok := Classify(ts.t, c.Path)
		if !ok {
			continue
		}
		if info, _ := bus.Lookup(typ); info.Scope == bus.ScopeRepo {
			if !repoDone[ts.t.Repo] {
				repoDone[ts.t.Repo] = true
				if r := w.repos[ts.t.Repo]; r != nil {
					w.agg.Add(r.t, c, now)
				}
			}
			continue
		}
		w.agg.Add(ts.t, c, now)
	}
}

func (w *FileWatcher) kick() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *FileWatcher) rescanTarget(ts *targetState) {
	now := time.Now()
	for _, p := range []string{
		filepath.Join(ts.t.GitDir, "HEAD"),
		filepath.Join(ts.t.GitDir, "index"),
		filepath.Join(ts.t.GitDir, "MERGE_HEAD"), // any operation marker makes the Translator check all
		filepath.Join(ts.t.Root, ".agentflow", "config.yml"),
		filepath.Join(ts.t.Root, ".mcp.json"), // any client settings file makes the Translator check all
	} {
		w.agg.Add(ts.t, Change{Path: p, Kind: Modified, Time: now}, now)
	}
}

func (w *FileWatcher) rescanRepo(r *repoState) {
	now := time.Now()
	for _, p := range []string{filepath.Join(r.t.CommonDir, "refs"), filepath.Join(r.t.CommonDir, "worktrees")} {
		w.agg.Add(r.t, Change{Path: p, Kind: Modified, Time: now}, now)
	}
}

func (w *FileWatcher) newGroup(roots []Path) *group {
	mode := ModeNotify
	if w.notify == nil {
		mode = ModePoll
	}
	return &group{mode: mode, roots: roots, dirs: map[string]bool{}}
}

// start acquires every root of g. When notifications cannot watch one of
// them, the whole group moves to polling.
func (w *FileWatcher) start(g *group, common string) error {
	for _, p := range g.roots {
		err := w.addPath(g, p, common)
		if err == nil {
			continue
		}
		w.releaseGroup(g)
		if errors.Is(err, ErrUnsupported) && g.mode == ModeNotify {
			w.opts.OnError(fmt.Errorf("watch: polling instead: %w", err))
			g.mode = ModePoll
			return w.start(g, common)
		}
		return err
	}
	return nil
}

func (w *FileWatcher) addPath(g *group, p Path, common string) error {
	if Excluded(common, p.Dir) {
		return nil
	}
	if !p.Recursive {
		return w.acquireInto(g, p.Dir)
	}
	root := p.Dir
	if g.mode == ModePoll && countEntries(root, PollRefsMaxEntries) > PollRefsMaxEntries {
		root = filepath.Join(root, "heads")
	}
	g.recursive = append(g.recursive, root)
	for _, d := range walkDirs(root, common) {
		if err := w.acquireInto(g, d); err != nil {
			return err
		}
	}
	return nil
}

func (w *FileWatcher) acquireInto(g *group, dir string) error {
	if g.dirs[dir] {
		return nil
	}
	if err := w.acquire(dir, g.mode); err != nil {
		return err
	}
	g.dirs[dir] = true
	return nil
}

func (w *FileWatcher) releaseGroup(g *group) {
	for d := range g.dirs {
		w.release(d, g.mode)
	}
	g.dirs = map[string]bool{}
	g.recursive = nil
}

func (w *FileWatcher) acquire(dir string, mode Mode) error {
	wd := w.want[dir]
	if wd == nil {
		wd = &wantDir{}
		w.want[dir] = wd
	}
	if mode == ModeNotify {
		wd.notify++
	} else {
		wd.poll++
	}
	if err := w.reconcile(dir); err != nil {
		w.release(dir, mode)
		return err
	}
	return nil
}

func (w *FileWatcher) release(dir string, mode Mode) {
	wd := w.want[dir]
	if wd == nil {
		return
	}
	if mode == ModeNotify && wd.notify > 0 {
		wd.notify--
	} else if mode == ModePoll && wd.poll > 0 {
		wd.poll--
	}
	if err := w.reconcile(dir); err != nil {
		w.opts.OnError(err)
	}
}

// reconcile makes the Sources watch dir as its reference counts want.
func (w *FileWatcher) reconcile(dir string) error {
	wd := w.want[dir]
	needNotify := wd.notify > 0 && w.notify != nil
	if needNotify && !wd.inNotify {
		err := w.notify.Add(dir)
		switch {
		case err == nil:
			wd.inNotify = true
		case errors.Is(err, fs.ErrNotExist):
			// Polled below until it appears.
		default:
			return err
		}
	}
	if !needNotify && wd.inNotify {
		_ = w.notify.Remove(dir)
		wd.inNotify = false
	}
	wantPoll := wd.poll > 0 || (needNotify && !wd.inNotify)
	if wantPoll && !wd.inPoll {
		if err := w.poll.Add(dir); err != nil {
			return err
		}
		wd.inPoll = true
	}
	if !wantPoll && wd.inPoll {
		_ = w.poll.Remove(dir)
		wd.inPoll = false
	}
	if wd.notify == 0 && wd.poll == 0 && !wd.inNotify && !wd.inPoll {
		delete(w.want, dir)
	}
	return nil
}

// dirGone handles the removal of a watched directory: subdirectories of a
// recursive root are released; roots wait (polled) for it to reappear.
func (w *FileWatcher) dirGone(dir string, from Mode) {
	sep := string(filepath.Separator)
	for _, g := range w.groups() {
		for d := range g.dirs {
			if (d == dir || strings.HasPrefix(d, dir+sep)) && !g.isRoot(d) {
				w.release(d, g.mode)
				delete(g.dirs, d)
			}
		}
	}
	for d, wd := range w.want {
		if (d == dir || strings.HasPrefix(d, dir+sep)) && wd.inNotify && from == ModeNotify {
			_ = w.notify.Remove(d)
			wd.inNotify = false
			if err := w.reconcile(d); err != nil {
				w.opts.OnError(err)
			}
		}
	}
}

// growRecursive acquires a new directory (and its subdirectories) under a
// recursive root and returns Created changes for the files already in it,
// which may have been written before the directory was watched.
func (w *FileWatcher) growRecursive(dir string, at time.Time) []Change {
	var out []Change
	for _, g := range w.groups() {
		common := w.commonOf(g)
		for _, root := range g.recursive {
			if dir != root && !strings.HasPrefix(dir, root+string(filepath.Separator)) {
				continue
			}
			if Excluded(common, dir) {
				continue
			}
			for _, d := range walkDirs(dir, common) {
				if err := w.acquireInto(g, d); err != nil {
					w.fallbackGroup(g)
					return nil
				}
			}
			out = append(out, walkFiles(dir, common, at)...)
		}
	}
	return out
}

// fallbackDir moves every notify group that wants dir to polling.
func (w *FileWatcher) fallbackDir(dir string) {
	for _, g := range w.groups() {
		if g.dirs[dir] && g.mode == ModeNotify {
			w.fallbackGroup(g)
		}
	}
}

// fallbackGroup moves g to polling and rescans what it covers, since
// changes may have been missed while it switched.
func (w *FileWatcher) fallbackGroup(g *group) {
	if g.mode == ModePoll {
		return
	}
	w.releaseGroup(g)
	g.mode = ModePoll
	common := w.commonOf(g)
	for _, p := range g.roots {
		if err := w.addPath(g, p, common); err != nil {
			w.opts.OnError(err)
		}
	}
	for _, r := range w.repos {
		if r.g == g {
			w.rescanRepo(r)
		}
	}
	for _, ts := range w.targets {
		if ts.g == g {
			w.rescanTarget(ts)
		}
	}
	if w.user != nil && w.user.g == g {
		w.rescanUser()
	}
	w.opts.OnError(errors.New("watch: notifications failed, polling instead"))
}

func (w *FileWatcher) groups() []*group {
	out := make([]*group, 0, len(w.repos)+len(w.targets)+1)
	for _, r := range w.repos {
		out = append(out, r.g)
	}
	for _, ts := range w.targets {
		out = append(out, ts.g)
	}
	if w.user != nil {
		out = append(out, w.user.g)
	}
	return out
}

func (w *FileWatcher) commonOf(g *group) string {
	for _, r := range w.repos {
		if r.g == g {
			return r.t.CommonDir
		}
	}
	for _, ts := range w.targets {
		if ts.g == g {
			return ts.t.CommonDir
		}
	}
	return ""
}

func (w *FileWatcher) recursiveRoot(dir string) bool {
	for _, g := range w.groups() {
		for _, r := range g.recursive {
			if r == dir {
				return true
			}
		}
	}
	return false
}

func (g *group) isRoot(dir string) bool {
	for _, p := range g.roots {
		if p.Dir == dir {
			return true
		}
	}
	for _, r := range g.recursive {
		if r == dir {
			return true
		}
	}
	return false
}

// walkDirs lists root and every directory below it, skipping excluded ones.
// A missing root is still listed so that it is polled until it appears.
func walkDirs(root, common string) []string {
	out := []string{root}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root || !d.IsDir() {
			return nil
		}
		if Excluded(common, path) {
			return filepath.SkipDir
		}
		out = append(out, path)
		return nil
	})
	return out
}

// walkFiles returns a Created change for every file below root.
func walkFiles(root, common string, at time.Time) []Change {
	var out []Change
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || Excluded(common, path) {
			return nil
		}
		out = append(out, Change{Path: path, Kind: Created, Time: at})
		return nil
	})
	return out
}

// listChanges returns a Created change for every entry of dir.
func listChanges(dir string, at time.Time) []Change {
	list, _ := os.ReadDir(dir)
	out := make([]Change, 0, len(list))
	for _, e := range list {
		out = append(out, Change{Path: filepath.Join(dir, e.Name()), Kind: Created, Time: at})
	}
	return out
}

// countEntries counts entries below root, stopping once it passes limit.
func countEntries(root string, limit int) int {
	n := 0
	stop := errors.New("stop")
	_ = filepath.WalkDir(root, func(_ string, _ fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if n++; n > limit {
			return stop
		}
		return nil
	})
	return n
}
