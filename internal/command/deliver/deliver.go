// Package deliver is the production command.Emitter: it hands the op.*
// events of CLI and MCP calls to the repository's event store by the
// single-writer rule (notifysock.Deliver), and never creates a store.
//
// It lives apart from internal/command because it needs internal/webui to
// name the worktree a call ran in, and internal/webui may import
// internal/command in later phases.
package deliver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/filelock"
	"github.com/ryanhelms/agentflow/internal/notifysock"
	"github.com/ryanhelms/agentflow/internal/webui"
)

// Budget bounds how long one Emit spends delivering an event. A command never
// waits long on a daemon that is not there.
const Budget = 500 * time.Millisecond

// Options configure Source. The zero value is the production setup.
type Options struct {
	// Warn receives the one warning of a process whose event was dropped.
	// Nil means os.Stderr.
	Warn io.Writer
	// Identify names the worktree containing dir. Nil means
	// webui.IdentifyWorktree.
	Identify func(ctx context.Context, dir string) (webui.WorktreeIdentity, error)
	// Locator finds the daemon's socket. Nil means webui.NotifyEndpoint.
	Locator notifysock.Locator
	// Open opens the existing store as store.RoleAppender. Nil means
	// store.Open.
	Open func(ctx context.Context, commonDir string, role store.Role) (store.Store, error)
}

// Source returns the command.EmitterSource of a CLI or MCP process. For an
// App whose repository has no event store it returns no Emitter, so the
// call publishes nothing and creates nothing under .git: only the daemon
// creates a store (ADR, phase 3 decision 6). The warning about a dropped
// event is written once per process.
func Source(o Options) command.EmitterSource {
	if o.Warn == nil {
		o.Warn = os.Stderr
	}
	if o.Identify == nil {
		o.Identify = webui.IdentifyWorktree
	}
	if o.Locator == nil {
		o.Locator = webui.NotifyEndpoint
	}
	if o.Open == nil {
		o.Open = store.Open
	}
	var once sync.Once
	warn := func(err error) {
		once.Do(func() { fmt.Fprintf(o.Warn, "agentflow: %v\n", err) })
	}
	return func(ctx context.Context, a *app.App) (command.Emitter, error) {
		if a == nil || a.Repo == nil {
			return nil, nil
		}
		id, err := o.Identify(ctx, a.Repo.Dir)
		if err != nil {
			return nil, nil // not a worktree agentflow can name: publish nothing
		}
		if !store.Exists(id.CommonDir) {
			return nil, nil
		}
		return &emitter{o: o, id: id, warn: warn}, nil
	}
}

// emitter delivers the events of one call to one repository's store.
type emitter struct {
	o    Options
	id   webui.WorktreeIdentity
	warn func(error)
}

// Emit implements command.Emitter. It opens the store for each event and
// closes it again: a call publishes two events, and an appender holds no
// lock between them.
func (e *emitter) Emit(ctx context.Context, ev bus.Event, worktree bool) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), Budget)
	defer cancel()
	// Exists again: the daemon may have removed the store since the call
	// started (agentflow remove). Opening it then would create it.
	if !store.Exists(e.id.CommonDir) {
		return errNoStore
	}
	s, err := e.o.Open(ctx, e.id.CommonDir, store.RoleAppender)
	if err != nil {
		e.warn(fmt.Errorf("event %s dropped: %w", ev.Type, err))
		return err
	}
	defer s.Close()
	ev.Repo = s.Repo()
	if worktree {
		ev.Worktree = e.id.Worktree
	}
	send := &retrySender{loc: e.o.Locator}
	defer send.close()
	appender := ownedFirst{Appender: s, lock: filepath.Join(store.Path(e.id.CommonDir), store.OwnerLock), loc: e.o.Locator}
	if notifysock.Deliver(ctx, ev, appender, send, e.warn) == notifysock.Dropped {
		return errDropped
	}
	return nil
}

var (
	errNoStore = errors.New("the repository has no event store")
	errDropped = errors.New("event dropped")
)

// retrySender dials the daemon on the first Send. When the daemon refuses the
// event's worktree (unknown_worktree: the console has not registered this
// worktree yet), it sends the event once more without the worktree, as a
// repository-wide event.
type retrySender struct {
	loc    notifysock.Locator
	client *notifysock.Client
	err    error
}

func (r *retrySender) Send(ctx context.Context, ev bus.Event) error {
	if r.client == nil && r.err == nil {
		r.client, r.err = notifysock.Dial(ctx, r.loc)
	}
	if r.err != nil {
		return r.err
	}
	err := r.client.Send(ctx, ev)
	if ev.Worktree != "" && unknownWorktree(err) {
		ev.Worktree = ""
		err = r.client.Send(ctx, ev)
	}
	return err
}

func (r *retrySender) close() {
	if r.client != nil {
		_ = r.client.Close()
	}
}

func unknownWorktree(err error) bool {
	var rej *notifysock.Rejection
	return errors.As(err, &rej) && rej.Code == notifysock.ErrorUnknownWorktree
}

// ownedFirst skips the store's lock wait when the daemon clearly owns the
// store: the owner lock is held and a daemon is registered. Append then
// reports store.ErrOwned at once, so Deliver goes to the socket within the
// budget. Otherwise it appends as usual. The same rule as internal/hooks.
type ownedFirst struct {
	notifysock.Appender
	lock string
	loc  notifysock.Locator
}

func (o ownedFirst) Append(ctx context.Context, e bus.Event) (bus.Event, error) {
	if lockHeld(o.lock) {
		if _, err := o.loc(ctx); err == nil {
			return bus.Event{}, store.ErrOwned
		}
	}
	return o.Appender.Append(ctx, e)
}

// lockHeld reports whether another process holds the store's owner lock.
func lockHeld(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false // no lock file yet: nobody owns the store
	}
	defer f.Close()
	ok, err := filelock.TryExclusive(f)
	return err == nil && !ok
}
