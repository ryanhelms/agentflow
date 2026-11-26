package watch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ryanhelms/agentflow/internal/filelock"
)

// The console daemon watches one agentflow home: the config.HomeDir() of its
// own process (AGENTFLOW_HOME, or ~/.agentflow). Approval records that a
// client writes under another home never reach the daemon, so no event and
// no approvals projection shows them. The daemon therefore holds a lock on a
// file in the home it watches, and `agentflow doctor` asks whether the
// client's own home is locked. A kernel lock needs no cleanup when the
// daemon crashes, and it cannot name a stale process.

// ConsoleLockName is the file in an agentflow home that the console daemon
// keeps locked while it watches the directory.
const ConsoleLockName = "console.lock"

// HoldConsoleHome marks home as watched by this daemon: it creates the
// directory if needed and takes the lock on ConsoleLockName until release
// is called or the process ends. held is false, with no error, when another
// process already holds it.
func HoldConsoleHome(home string) (release func(), held bool, err error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(home, ConsoleLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	ok, err := filelock.TryExclusive(f)
	if err != nil || !ok {
		_ = f.Close()
		return nil, false, err
	}
	// The file stays: a process waiting for it must contend on the same inode.
	return func() { _ = f.Close() }, true, nil
}

// ConsoleWatches reports whether a console daemon holds the lock of home.
// A home without the file, or an unlocked one, is not watched.
func ConsoleWatches(home string) (bool, error) {
	f, err := os.OpenFile(filepath.Join(home, ConsoleLockName), os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }() // also drops the lock taken below
	free, err := filelock.TryExclusive(f)
	return !free && err == nil, err
}

// HomeCheck is the result of CheckHome, the `agentflow doctor` line about
// the console daemon's home directory.
type HomeCheck struct {
	// Checked is false when no console daemon runs: there is nothing to
	// compare and doctor prints no line.
	Checked bool
	// Warn is true when the daemon does not watch the caller's home.
	Warn bool
	// Detail is the one-line message for the doctor report.
	Detail string
}

// CheckHome compares the caller's agentflow home with the console daemon's.
// daemonRunning says whether a daemon runs for this user (the caller asks
// the console registry); home is the caller's config.HomeDir().
func CheckHome(home string, daemonRunning bool) HomeCheck {
	if !daemonRunning {
		return HomeCheck{}
	}
	watched, err := ConsoleWatches(home)
	switch {
	case err != nil:
		return HomeCheck{Checked: true, Warn: true, Detail: fmt.Sprintf("cannot tell whether the console daemon watches %s: %v", home, err)}
	case watched:
		return HomeCheck{Checked: true, Detail: "the console daemon watches " + home}
	}
	return HomeCheck{Checked: true, Warn: true, Detail: "the console daemon does not watch " + home +
		": it runs with another AGENTFLOW_HOME, or started before it watched approvals. " +
		"Approvals made from here will not appear in the console. Run agentflow ui --stop, then start the console from this shell"}
}
