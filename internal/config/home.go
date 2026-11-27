package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	osuser "os/user"
	"path/filepath"
	"strings"
	"sync"
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
var accountHome = func() (string, error) {
	u, err := osuser.Current()
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
	Repo   string // the working tree or git directory it is inside
}

func (e *HomeInRepositoryError) Error() string {
	return fmt.Sprintf("the agentflow home %s (from %s) is inside the repository %s, and the account's home folder "+
		"cannot be found to use instead; %s", e.Dir, e.Source, e.Repo, homeFix(e.Source))
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
	repo, inside := enclosingRepository(resolvePath(dir))
	if !inside || (accountErr == nil && sameDir(repo, account)) {
		return dir, nil
	}
	if accountErr != nil {
		return "", &HomeInRepositoryError{Source: source, Dir: dir, Repo: repo}
	}
	fallback, err := defaultHome(account)
	if err != nil {
		return "", err
	}
	warnHome(fmt.Sprintf("agentflow: ignoring the agentflow home %s (from %s): it is inside the repository %s, "+
		"which could then supply your personal settings and plugins. Using %s instead. To fix this, %s.\n",
		dir, source, repo, fallback, homeFix(source)))
	return fallback, nil
}

var homeWarned sync.Map

// warnHome prints msg once per process.
func warnHome(msg string) {
	if _, seen := homeWarned.LoadOrStore(msg, true); !seen && HomeWarnings != nil {
		_, _ = io.WriteString(HomeWarnings, msg)
	}
}

// isAccountFolder reports whether dir is ~/.agentflow or ~/.devflow of the
// OS account, after following links.
func isAccountFolder(dir, account string) bool {
	return sameDir(dir, filepath.Join(account, ".agentflow")) || sameDir(dir, filepath.Join(account, ".devflow"))
}

// sameDir compares two folders by their resolved path and, when both exist,
// by identity (case-insensitive file systems, hard-to-spell aliases).
func sameDir(a, b string) bool {
	ra, rb := resolvePath(a), resolvePath(b)
	if ra == rb {
		return true
	}
	ia, errA := os.Stat(ra)
	ib, errB := os.Stat(rb)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// resolvePath returns the absolute, physical form of p, which need not exist:
// links in its longest existing prefix are followed before any ".." after
// them is applied, as the file system would.
func resolvePath(p string) string {
	if !filepath.IsAbs(p) {
		if wd, err := os.Getwd(); err == nil {
			p = wd + string(filepath.Separator) + p
		}
	}
	var tail []string
	cur := p
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{r}, tail...)...)
		} else if !errors.Is(err, os.ErrNotExist) {
			break
		}
		parent, base := filepath.Split(strings.TrimRight(cur, string(filepath.Separator)))
		if parent == "" || parent == cur {
			break
		}
		tail = append([]string{base}, tail...)
		cur = parent
	}
	return filepath.Clean(p)
}

// enclosingRepository returns the nearest folder at or above dir that is a
// git working tree root (it has a .git entry: a primary checkout, a linked
// worktree or a submodule) or a git directory.
func enclosingRepository(dir string) (string, bool) {
	for d := dir; ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d, true
		}
		if isGitDir(d) {
			return d, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
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
