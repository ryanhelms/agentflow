package webui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/watch"
)

// The daemon's user-level event sources (lane 2.1): the personal
// configuration layer, client settings files in the OS home and approval
// records. The contract is in the ADR, section "Phase 2 watch sources". The
// worktree-level sources (git, repository configuration, repository client
// settings) run in the watcher itself; this file only starts the part that
// has no worktree.

func init() {
	runtimeExtensions = append(runtimeExtensions, func() runtimeExtension { return &userEvents{} })
}

// osUserHome returns the OS user's home directory from os/user, never from
// $HOME, which a repository or agent can set. Tests replace it.
var osUserHome = func() (string, error) {
	account, err := user.Current()
	if err != nil || !filepath.IsAbs(account.HomeDir) {
		return "", errors.New("could not locate the OS user's home directory")
	}
	return account.HomeDir, nil
}

// userWatcher is what a watcher offers for the user-level directories.
// *watch.FileWatcher implements it.
type userWatcher interface {
	AddUser(u watch.UserTarget, tr watch.Translator) error
}

// userEvents is the runtimeExtension for the user-level sources. It watches
// one watch.UserTarget through the daemon's watcher, routes approval records
// to the repositories the daemon serves, and holds the lock that tells
// `agentflow doctor` which agentflow home the daemon watches.
type userEvents struct {
	tr      *watch.UserTranslator
	release func()
}

// start gives the approvals projection its source and watches the user
// target. A watcher that cannot watch it (a fake in a test) leaves the
// extension idle. Events of the global repository (the personal layer, user
// client settings) are published like any other; they are stored once the
// daemon opens the global store (openGlobalStore, the "global store" work).
func (x *userEvents) start(rt *eventRuntime) error {
	// The approvals projection reads the journal records, never the events
	// alone, so it needs its source whether or not the watcher runs.
	rt.setApprovalSource(newJournalApprovals(rt))
	uw, ok := rt.watcher.(userWatcher)
	if !ok {
		return nil
	}
	target, err := daemonUserTarget()
	if err != nil {
		return fmt.Errorf("user-level events: %w", err)
	}
	tr, err := watch.NewUserTranslator(target)
	if err != nil {
		return fmt.Errorf("user-level events: %w", err)
	}
	if err := uw.AddUser(target, tr); err != nil {
		return fmt.Errorf("user-level events: %w", err)
	}
	x.tr = tr
	if exists, owned := watch.OwnedDir(target.Home); !exists || owned {
		release, held, err := watch.HoldConsoleHome(tr.Target().Home)
		switch {
		case err != nil:
			fmt.Fprintln(os.Stderr, "agentflow: cannot mark the agentflow home as watched:", err)
		case held:
			x.release = release
		}
	}
	return nil
}

// attached routes the approval records of w's repository to its stream.
func (x *userEvents) attached(r *repoEvents, w registeredWorktree) {
	if x.tr != nil {
		x.tr.Serve(policy.ScopeKey(w.Common), r.id)
	}
}

func (x *userEvents) stop() {
	if x.release != nil {
		x.release()
	}
}

// daemonUserTarget is the user-level target of this daemon process: its
// agentflow home (config.HomeDir) and the OS user's home.
func daemonUserTarget() (watch.UserTarget, error) {
	home, err := config.HomeDir()
	if err != nil {
		return watch.UserTarget{}, err
	}
	if home, err = filepath.Abs(home); err != nil {
		return watch.UserTarget{}, err
	}
	userHome, err := osUserHome()
	if err != nil {
		return watch.UserTarget{}, err
	}
	return watch.UserTarget{Home: home, UserHome: userHome}, nil
}

// repoCommonDir returns the common git directory of a repository the
// runtime serves.
func repoCommonDir(rt *eventRuntime, id bus.RepoID) (string, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	r := rt.byID[id]
	if r == nil {
		return "", false
	}
	return r.common, true
}

// journalApprovals implements ApprovalSource over the approval journal: it
// lists the records of one served repository from the agentflow home of the
// daemon process, read-only, and only from a directory this user owns.
type journalApprovals struct {
	rt  *eventRuntime
	now func() time.Time
}

// newJournalApprovals returns the ApprovalSource of the approvals
// projection; start installs it with rt.setApprovalSource.
func newJournalApprovals(rt *eventRuntime) ApprovalSource {
	return journalApprovals{rt: rt, now: time.Now}
}

// Approvals lists the pending and approved records, and the finished ones
// from the last ApprovalsRecentAge, oldest issued first. A repository the
// daemon does not serve has none.
func (j journalApprovals) Approvals(ctx context.Context, repo bus.RepoID) ([]ApprovalItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	common, ok := repoCommonDir(j.rt, repo)
	if !ok {
		return nil, nil
	}
	home, err := config.HomeDir()
	if err != nil {
		return nil, err
	}
	dir := policy.ApprovalsDir(home)
	if exists, owned := watch.OwnedDir(dir); exists && !owned {
		return nil, errors.New("the approvals directory is not a directory owned by this user")
	}
	now := j.now()
	records, err := policy.ListJournal(dir, policy.ScopeKey(common), now.Add(-ApprovalsRecentAge))
	if err != nil {
		return nil, err
	}
	items := make([]ApprovalItem, 0, len(records))
	for _, r := range records {
		state := bus.ApprovalState(r.State)
		items = append(items, ApprovalItem{Approval: watch.ApprovalPayload(r), State: state,
			Expired: watch.ObservedExpiry(state, r.ExpiresAt, now)})
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].IssuedAt.Before(items[b].IssuedAt) })
	return items, nil
}

// CheckDaemonHome is the `agentflow doctor` check for the console daemon's
// agentflow home. Approval records that a client writes under another home
// never reach the daemon, so its events and the approvals panel miss them.
// The daemon marks the home it watches (watch.HoldConsoleHome); this asks
// whether the caller's home is marked. The result is not Checked when no
// daemon runs or the registry cannot be read.
func CheckDaemonHome(ctx context.Context) watch.HomeCheck {
	home, err := config.HomeDir()
	if err != nil {
		return watch.HomeCheck{}
	}
	running, _, err := DaemonStatus(ctx)
	if err != nil {
		return watch.HomeCheck{}
	}
	return watch.CheckHome(home, running)
}
