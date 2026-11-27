package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	osuser "os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The agentflow home holds the personal layer: config.yml (autonomy and the
// approval window), provider definitions, approvals and installed plugins,
// which run as the user. A repository must never supply it. A repository's
// environment (direnv, an editor or AI-client setting) can set AGENTFLOW_HOME,
// DEVFLOW_HOME or HOME, so HomeDir checks the folder it resolves to, whatever
// set it:
//
//   - The account's own folder (~/.agentflow or ~/.devflow under the OS
//     account's home from os/user, links followed) is always used. Only the
//     person can put it there.
//   - Any other folder inside a git working tree or a git directory is
//     ignored, unless that working tree's root is the OS account's home (a
//     dotfiles repository). Links and ".." are resolved first, so this covers
//     the primary checkout and every linked worktree of every repository,
//     which is what the console daemon and the MCP server need: they serve
//     several repositories with one home.
//
// An ignored folder is replaced by the account's own folder, with a warning on
// stderr. Callers never see a new error, so no caller can fail open on it.

// HomeDir returns the agentflow home without creating it: AGENTFLOW_HOME,
// then DEVFLOW_HOME, then ~/.agentflow (or a legacy ~/.devflow), unless that
// folder is inside a repository (see above).
func HomeDir() (string, error) {
	dir, source, err := requestedHome()
	if err != nil {
		return "", err
	}
	return checkHome(dir, source)
}

// HomeWarnings receives the warning printed when HomeDir ignores a folder.
// Tests replace it.
var HomeWarnings io.Writer = os.Stderr

// accountHome is the OS account's home directory from os/user, never $HOME.
// Tests replace it.
var accountHome = sync.OnceValues(lookupAccountHome)

// OverrideAccountHome makes HomeDir treat dir as the OS account's home until
// restore is called. Only tests call it, so that no test falls back to the
// developer's own ~/.agentflow.
func OverrideAccountHome(dir string) (restore func()) {
	old, oldSet := accountHome, accountOverridden
	accountHome = func() (string, error) { return dir, nil }
	accountOverridden = true
	return func() { accountHome, accountOverridden = old, oldSet }
}

// accountOverridden is true while OverrideAccountHome is in effect. In a test
// binary, HomeDir refuses to fall back to the real account folder without it.
var accountOverridden bool

// lookupAccountHome looks the account up by its user ID. user.Current is not
// used outside Windows: without cgo it falls back to $HOME when the user ID
// has no passwd entry (a container started with --user), and a repository can
// set $HOME. With no entry there is no account home, and so no exception.
func lookupAccountHome() (string, error) {
	var u *osuser.User
	var err error
	if runtime.GOOS == "windows" {
		u, err = osuser.Current()
	} else {
		u, err = osuser.LookupId(strconv.Itoa(os.Getuid()))
	}
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(u.HomeDir) {
		return "", errors.New("the account has no absolute home directory")
	}
	return u.HomeDir, nil
}

// HomeInRepositoryError is returned when the requested home is inside a
// repository and the account's own folder cannot be found to use instead.
type HomeInRepositoryError struct {
	Source string // AGENTFLOW_HOME, DEVFLOW_HOME or HOME
	Dir    string // the requested folder
	Reason string // why it is not used, as placement says
}

func (e *HomeInRepositoryError) Error() string {
	return fmt.Sprintf("the agentflow home %s (from %s) %s, and the account's home folder "+
		"cannot be found to use instead; %s", e.Dir, e.Source, e.Reason, homeFix(e.Source))
}

func homeFix(source string) string {
	switch source {
	case "HOME":
		return "set HOME to your account's home, or set AGENTFLOW_HOME to a folder outside every repository"
	case "DEVFLOW_HOME":
		return "unset DEVFLOW_HOME, or set AGENTFLOW_HOME to a folder outside every repository (for example ~/.agentflow)"
	}
	return "set " + source + " to a folder outside every repository (for example ~/.agentflow), or unset it"
}

func requestedHome() (dir, source string, err error) {
	if h := os.Getenv("AGENTFLOW_HOME"); h != "" {
		return h, "AGENTFLOW_HOME", nil
	}
	if h := os.Getenv("DEVFLOW_HOME"); h != "" {
		return h, "DEVFLOW_HOME", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	dir, err = defaultHome(home)
	return dir, "HOME", err
}

// defaultHome is ~/.agentflow under home, or a legacy ~/.devflow when only
// that exists.
func defaultHome(home string) (string, error) {
	dir := filepath.Join(home, ".agentflow")
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		return dir, err
	}
	legacy := filepath.Join(home, ".devflow")
	if _, err := os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
		return legacy, err
	}
	return dir, nil
}

func checkHome(dir, source string) (string, error) {
	account, accountErr := accountHome()
	if accountErr == nil && isAccountFolder(dir, account) {
		return dir, nil
	}
	inside, reason := insideRepository(dir, account, accountErr == nil)
	if !inside {
		return dir, nil
	}
	if accountErr != nil {
		return "", &HomeInRepositoryError{Source: source, Dir: dir, Reason: reason}
	}
	if testing.Testing() && !accountOverridden {
		// A test must never read or write the developer's own home.
		return "", fmt.Errorf("the agentflow home %s (from %s) %s, and a test binary does not fall back to the "+
			"real account folder: put the test's home outside its repository or call config.OverrideAccountHome", dir, source, reason)
	}
	fallback, err := defaultHome(account)
	if err != nil {
		return "", err
	}
	warnHome(fmt.Sprintf("agentflow: ignoring the agentflow home %s (from %s): it %s, so a repository could "+
		"supply your personal settings and plugins. Using %s instead. To fix this, %s.",
		dir, source, reason, fallback, homeFix(source)))
	return fallback, nil
}

// InsideRepository reports whether dir, which need not exist, is inside a
// git working tree (a primary checkout, linked worktree or submodule) or a git
// directory, after resolving links and "..". A repository whose root is the
// OS account's home (from os/user, never $HOME) does not count: that is a
// dotfiles repository. It fails closed: a dir that cannot be resolved or
// checked counts as inside. why says which, for a message ("is inside the
// repository R", "cannot be checked (...)"); it is "" when inside is false.
//
// HomeDir uses it for the agentflow home; use it for any directory a
// repository's environment could have set (HOME, XDG_*_HOME, CODEX_HOME).
func InsideRepository(dir string) (inside bool, why string) {
	account, err := accountHome()
	return insideRepository(dir, account, err == nil)
}

func insideRepository(dir, account string, haveAccount bool) (bool, string) {
	resolved, ok := resolvePath(dir)
	if !ok {
		return true, "cannot be checked (its path does not resolve)"
	}
	repo, inside, ok := enclosingRepository(resolved)
	switch {
	case !ok:
		return true, "cannot be checked (a folder above it cannot be read)"
	case !inside, haveAccount && sameDir(repo, account):
		return false, ""
	}
	return true, "is inside the repository " + repo
}

var (
	homeWarned sync.Map
	homeNotes  struct {
		sync.Mutex
		list []string
	}
)

// warnHome prints msg once per process and keeps it for HomeNotices.
func warnHome(msg string) {
	if _, seen := homeWarned.LoadOrStore(msg, true); seen {
		return
	}
	homeNotes.Lock()
	homeNotes.list = append(homeNotes.list, msg)
	homeNotes.Unlock()
	if HomeWarnings != nil {
		_, _ = io.WriteString(HomeWarnings, msg+"\n")
	}
}

// HomeNotices returns the warnings HomeDir has printed in this process, for
// output an agent or person reads where stderr is not shown (the
// session-start hook).
func HomeNotices() []string {
	homeNotes.Lock()
	defer homeNotes.Unlock()
	return slices.Clone(homeNotes.list)
}

// isAccountFolder reports whether dir is ~/.agentflow or ~/.devflow of the
// OS account, after following links.
func isAccountFolder(dir, account string) bool {
	return sameDir(dir, filepath.Join(account, ".agentflow")) || sameDir(dir, filepath.Join(account, ".devflow"))
}

// sameDir compares two folders by their resolved path and, when both exist,
// by identity (case-insensitive file systems, hard-to-spell aliases).
func sameDir(a, b string) bool {
	ra, okA := resolvePath(a)
	rb, okB := resolvePath(b)
	if !okA || !okB {
		return false
	}
	if ra == rb {
		return true
	}
	ia, errA := os.Stat(ra)
	ib, errB := os.Stat(rb)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// resolvePath returns the absolute, physical form of p, which need not exist:
// links in its longest existing prefix are followed before any ".." after
// them is applied, as the file system would. ok is false when that cannot be
// done (a link loop, a file used as a folder, no working directory).
func resolvePath(p string) (string, bool) {
	if !filepath.IsAbs(p) {
		wd, err := os.Getwd()
		if err != nil {
			return "", false
		}
		p = wd + string(filepath.Separator) + p
	}
	var tail []string
	cur := p
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(append([]string{r}, tail...)...), true
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		parent, base := filepath.Split(strings.TrimRight(cur, string(filepath.Separator)))
		if parent == "" || parent == cur {
			return "", false
		}
		tail = append([]string{base}, tail...)
		cur = parent
	}
}

// enclosingRepository returns the nearest folder at or above dir that is a
// git working tree root (it has a .git entry: a primary checkout, a linked
// worktree or a submodule) or a git directory. ok is false when a folder on
// the way cannot be checked.
func enclosingRepository(dir string) (repo string, inside, ok bool) {
	for d := dir; ; {
		_, err := os.Lstat(filepath.Join(d, ".git"))
		switch {
		case err == nil:
			return d, true, true
		case !errors.Is(err, os.ErrNotExist):
			return "", false, false
		}
		if isGitDir(d) {
			return d, true, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false, true
		}
		d = parent
	}
}

func isGitDir(d string) bool {
	head, err := os.Lstat(filepath.Join(d, "HEAD"))
	if err != nil || !head.Mode().IsRegular() {
		return false
	}
	for _, sub := range []string{"objects", "refs"} {
		if fi, err := os.Stat(filepath.Join(d, sub)); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}
