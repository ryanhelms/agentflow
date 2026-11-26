package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/filelock"
	"github.com/ryanhelms/agentflow/internal/notifysock"
	"github.com/ryanhelms/agentflow/internal/webui"
)

// NotifyBudget bounds how long a hook spends publishing its events after
// its own work. A hook never waits on a daemon that is not there.
const NotifyBudget = 300 * time.Millisecond

// Reason codes for blocked and failed hook.ran events. Fixed identifiers,
// never text.
const (
	ReasonConvention      = "convention"
	ReasonRepoBoundary    = "repo_boundary"
	ReasonNeverList       = "never_list"
	ReasonProtectedBranch = "protected_branch"
	ReasonError           = "error"
)

// notifyEvent is one event a hook publishes; deliverEvents fills in the
// repository and worktree.
type notifyEvent struct {
	Type    bus.Type
	Hook    string
	Payload any
}

// deliverEvents hands events to the event store of the worktree containing
// dir through notifysock.Deliver, without a warning function: git and
// agents read hook output, so a dropped hook event is silent. Tests
// replace it.
var deliverEvents = func(ctx context.Context, dir string, events ...notifyEvent) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), NotifyBudget)
	defer cancel()
	id, err := webui.IdentifyWorktree(ctx, dir)
	if err != nil {
		return
	}
	s, err := app.OpenEventStore(ctx, id.CommonDir, store.RoleAppender)
	if err != nil {
		return
	}
	defer s.Close()
	send := &lazySender{loc: notifyLocator}
	defer send.close()
	appender := ownedFirst{Appender: s, lock: filepath.Join(store.Path(id.CommonDir), store.OwnerLock), loc: notifyLocator}
	for _, ne := range events {
		raw, err := json.Marshal(ne.Payload)
		if err != nil {
			continue
		}
		e := bus.Event{Type: ne.Type, Source: "hook:" + ne.Hook, Repo: s.Repo(), Worktree: id.Worktree, Payload: raw}
		if notifysock.Deliver(ctx, e, appender, send, nil) == notifysock.Dropped && ctx.Err() != nil {
			return
		}
	}
}

// notifyLocator finds the daemon. Tests replace it.
var notifyLocator notifysock.Locator = webui.NotifyEndpoint

// ownedFirst skips the store's lock wait when the daemon clearly owns the
// store: the owner lock is held and a daemon is registered. Append then
// reports store.ErrOwned at once, so Deliver goes to the socket within the
// hook's budget. Otherwise it appends as usual, which waits out a brief
// overlap with another appender.
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

// lazySender dials the daemon on the first Send and reuses the connection.
type lazySender struct {
	loc    notifysock.Locator
	client *notifysock.Client
	err    error
}

func (l *lazySender) Send(ctx context.Context, e bus.Event) error {
	if l.client == nil && l.err == nil {
		l.client, l.err = notifysock.Dial(ctx, l.loc)
	}
	if l.err != nil {
		return l.err
	}
	return l.client.Send(ctx, e)
}

func (l *lazySender) close() {
	if l.client != nil {
		_ = l.client.Close()
	}
}

// hookRan publishes hook.ran for a hook that started at start and ended
// with err. failuresOnly publishes only blocked and failed outcomes
// (pre-bash, which runs before every shell command).
func hookRan(ctx context.Context, hook, client string, start time.Time, err error, failuresOnly bool) {
	outcome, reason := classify(err)
	if failuresOnly && outcome == bus.HookPassed {
		return
	}
	dir, werr := os.Getwd()
	if werr != nil {
		return
	}
	deliverEvents(ctx, dir, hookRanEvent(hook, client, start, outcome, reason))
}

func hookRanEvent(hook, client string, start time.Time, outcome bus.HookOutcome, reason string) notifyEvent {
	return notifyEvent{Type: bus.TypeHookRan, Hook: hook, Payload: bus.HookRan{
		Hook: hook, Client: client, Outcome: outcome, Reason: reason, DurationMS: time.Since(start).Milliseconds(),
	}}
}

// classify maps a hook's result to an outcome and a reason code.
func classify(err error) (bus.HookOutcome, string) {
	if err == nil {
		return bus.HookPassed, ""
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return bus.HookBlocked, ee.Reason
	}
	return bus.HookFailed, ReasonError
}

// fromHookCommand reports whether this process runs as `agentflow hook
// ...`. CommitMsg also serves `agentflow lint-msg`, which the pre-push
// hook and CI run for every commit and which is not a hook invocation.
// Tests replace it.
var fromHookCommand = func() bool { return len(os.Args) > 1 && os.Args[1] == "hook" }

// truncate cuts s to at most n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
