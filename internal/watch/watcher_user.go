package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// userState is the watcher's one user-level group: the directories of a
// UserTarget and the Translator for them. Its batches are keyed under the
// placeholder Target{Repo: bus.GlobalRepo}, which no worktree can have.
type userState struct {
	u  UserTarget
	t  Target
	g  *group
	tr Translator
}

// AddUser starts watching the user-level directories of u (UserPaths) and
// translates what changes there with tr, normally a UserTranslator made
// from the same u. Like worktrees, the directories are watched
// non-recursively, with the polling fallback, and one that does not exist
// yet (for example ~/.agentflow/approvals before the first approval) is
// polled until it appears.
//
// A directory that exists but is a symbolic link, or belongs to another
// user, is not watched, and neither is anything below an agentflow home
// that fails the same check; OnError reports each one. A directory that
// appears later is not checked again, but every file the translators read
// from it is (UserTranslator reads only regular files the user owns).
//
// When tr is a Tracker, AddUser calls Track once the directories are
// watched, with the placeholder Target. When it is Timed, it receives the
// watcher's wake-up function. AddUser may be called once.
func (w *FileWatcher) AddUser(u UserTarget, tr Translator) error {
	u, err := CanonicalUser(u)
	if err != nil {
		return err
	}
	if tr == nil {
		return errors.New("watch: a user target needs a Translator")
	}
	var roots []Path
	homeOK := true
	if exists, ok := OwnedDir(u.Home); exists && !ok {
		homeOK = false
		w.opts.OnError(fmt.Errorf("watch: not watching %s: not a directory owned by this user", u.Home))
	}
	for _, p := range UserPaths(u) {
		inHome := p.Dir == u.Home
		if _, below := under(u.Home, p.Dir); below {
			inHome = true
		}
		if inHome && !homeOK {
			continue
		}
		if p.Dir != u.Home { // the home was checked above
			if exists, ok := OwnedDir(p.Dir); exists && !ok {
				w.opts.OnError(fmt.Errorf("watch: not watching %s: not a directory owned by this user", p.Dir))
				continue
			}
		}
		roots = append(roots, p)
	}
	us := &userState{u: u, t: Target{Repo: bus.GlobalRepo}, tr: tr}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errors.New("watch: watcher closed")
	}
	if w.user != nil {
		w.mu.Unlock()
		return errors.New("watch: the user target is already watched")
	}
	us.g = w.newGroup(roots)
	if err := w.start(us.g, ""); err != nil {
		w.mu.Unlock()
		return err
	}
	w.user = us
	w.mu.Unlock()

	if timed, ok := tr.(Timed); ok {
		timed.SetWake(w.kick)
	}
	// Watch first, then record the baseline: a change in between is
	// reported against the new state, never lost.
	if tracker, ok := tr.(Tracker); ok {
		if err := tracker.Track(context.Background(), us.t); err != nil {
			w.opts.OnError(err)
		}
	}
	w.kick()
	return nil
}

// CanonicalUser makes the paths of u absolute and free of symbolic links,
// so they compare equal to the paths a Source reports (macOS, for example,
// reports /private/var for /var). For a path that does not exist yet it
// resolves the longest prefix that does. Home and UserHome must be set and
// absolute.
func CanonicalUser(u UserTarget) (UserTarget, error) {
	for _, p := range []*string{&u.Home, &u.UserHome} {
		if *p == "" || !filepath.IsAbs(*p) {
			return UserTarget{}, fmt.Errorf("watch: user target needs absolute Home and UserHome, got %q", *p)
		}
		*p = resolveExisting(filepath.Clean(*p))
	}
	return u, nil
}

// resolveExisting resolves symbolic links in the longest existing prefix of
// p and keeps the rest as it is.
func resolveExisting(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(real)
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolveExisting(parent), filepath.Base(p))
}

// OwnedDir reports whether dir may be watched as a user-level directory.
// exists says whether anything is at dir; ok is false when something is
// there that is not a real directory (a symbolic link counts as not real)
// owned by the user this process runs as. A missing directory is ok: it is
// watched once it appears. On Windows the owner is not checked.
func OwnedDir(dir string) (exists, ok bool) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return false, errors.Is(err, fs.ErrNotExist)
	}
	return true, fi.IsDir() && ownedByCurrentUser(fi)
}

// concerns reports whether path concerns the user target: a classified
// file, or the approvals directory itself, which the watcher uses to ask
// the translator for a full scan.
func (us *userState) concerns(path string) bool {
	if path == policy.ApprovalsDir(us.u.Home) {
		return true
	}
	_, _, ok := ClassifyUser(us.u, path)
	return ok
}

// rescanUser makes the user translator compare everything again, as after a
// queue overflow. Called with w.mu held.
func (w *FileWatcher) rescanUser() {
	us := w.user
	if us == nil {
		return
	}
	now := time.Now()
	paths := []string{filepath.Join(us.u.Home, "config.yml"), policy.ApprovalsDir(us.u.Home)}
	for _, f := range UserClientSettings {
		paths = append(paths, filepath.Join(us.u.UserHome, filepath.FromSlash(f.Path)))
	}
	for _, p := range paths {
		w.agg.Add(us.t, Change{Path: p, Kind: Modified, Time: now}, now)
	}
}

// userTranslator returns the Translator of the user target, nil when none.
func (w *FileWatcher) userTranslator() Translator {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.user == nil {
		return nil
	}
	return w.user.tr
}

// nextTimed returns the earliest deadline of the user Translator.
func (w *FileWatcher) nextTimed() (time.Time, bool) {
	if timed, ok := w.userTranslator().(Timed); ok {
		return timed.NextDeadline()
	}
	return time.Time{}, false
}

// tick publishes the events the user Translator has due at now.
func (w *FileWatcher) tick(ctx context.Context, pub bus.Publisher, now time.Time) {
	timed, ok := w.userTranslator().(Timed)
	if !ok {
		return
	}
	evs, err := timed.Tick(ctx, now)
	if err != nil {
		w.opts.OnError(fmt.Errorf("translate user timers: %w", err))
	}
	w.publish(ctx, pub, evs)
}

func (w *FileWatcher) publish(ctx context.Context, pub bus.Publisher, evs []bus.Event) {
	for _, e := range evs {
		if _, err := pub.Publish(ctx, e); err != nil && ctx.Err() == nil {
			w.opts.OnError(fmt.Errorf("publish %s: %w", e.Type, err))
		}
	}
}
