package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// AccountHome returns the OS account's home directory from os/user, never
// $HOME (see lookupAccountHome). Under OverrideAccountHome it returns the
// override.
func AccountHome() (string, error) {
	return accountHome()
}

// windowsRules turns on the Windows-only checks in UserDir: \\?\ and \\.\
// paths, and links or junctions in the folders that exist, count as
// impossible to check. The launcher agentflow.ps1 cannot resolve those, so
// UserDir refuses them too, and both pick the same folder. Tests change it.
var windowsRules = runtime.GOOS == "windows"

// UserDir applies HomeDir's rule to another per-user folder that a
// repository's environment could set, such as the install cache under
// XDG_CACHE_HOME, HOME or LOCALAPPDATA. dir (from the variable source) is
// used when it is the account's own folder (own), or when it is outside every
// git working tree and git directory, a repository rooted at the account's
// home excepted. Otherwise own is used, with a warning on stderr. what names
// the folder in that warning.
//
// It is stricter than HomeDir in one way, to give the same answer as the
// launchers' shell check: dir also counts as impossible to check when it is
// relative, has a "." or ".." element, or is a file. An empty dir means the
// variable is not set: own is used without a warning.
//
// The result is cleaned. Nothing is created.
func UserDir(dir, source, what string, own func() (string, error)) (string, error) {
	why := ""
	switch {
	case dir == "":
		why = "is not set"
	case !filepath.IsAbs(dir):
		why = "cannot be checked (its path is not absolute)"
	case hasDotElement(dir):
		why = "cannot be checked (its path has a . or .. folder)"
	case windowsRules && isDevicePath(dir):
		why = "cannot be checked (it is a device path)"
	}
	ownDir, ownErr := own()
	if why == "" {
		if ownErr == nil && sameDir(dir, ownDir) {
			return filepath.Clean(dir), nil
		}
		why = userDirProblem(dir)
	}
	if why == "" {
		return filepath.Clean(dir), nil
	}
	if ownErr != nil {
		return "", fmt.Errorf("the %s %s (from %s) %s, and the account's own folder cannot be found to use instead (%w); "+
			"set %s to a folder outside every repository", what, dir, source, why, ownErr, source)
	}
	if testing.Testing() && !accountOverridden {
		// A test must never read or write the developer's own folders.
		return "", fmt.Errorf("the %s %s (from %s) %s, and a test binary does not fall back to the real account "+
			"folder: put it outside the test's repository or call config.OverrideAccountHome", what, dir, source, why)
	}
	if dir != "" {
		warnHome(fmt.Sprintf("agentflow: ignoring the %s %s (from %s): it %s, so a repository could supply the "+
			"agentflow binary. Using %s instead.", what, dir, source, why, ownDir))
	}
	return filepath.Clean(ownDir), nil
}

// userDirProblem returns why dir, an absolute path without "." or "..", is
// not used, or "" when it is.
func userDirProblem(dir string) string {
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		return "cannot be checked (it is a file)"
	}
	if windowsRules {
		if link := linkAbove(dir); link != "" {
			return "cannot be checked (it goes through the link " + link + ")"
		}
	}
	account, err := accountHome()
	if inside, reason := insideRepository(dir, account, err == nil); inside {
		return reason
	}
	return ""
}

func hasDotElement(p string) bool {
	for _, e := range strings.FieldsFunc(p, isSeparator) {
		if e == "." || e == ".." {
			return true
		}
	}
	return false
}

func isSeparator(r rune) bool {
	return r == '/' || (windowsRules && r == '\\')
}

// isDevicePath reports a Windows \\?\ or \\.\ path, in either slash.
func isDevicePath(p string) bool {
	return len(p) >= 4 && isSeparator(rune(p[0])) && isSeparator(rune(p[1])) &&
		(p[2] == '?' || p[2] == '.') && isSeparator(rune(p[3]))
}

// linkAbove returns the first folder at or above dir that exists and is a
// link, junction or other reparse point, or "".
func linkAbove(dir string) string {
	for d := filepath.Clean(dir); ; {
		if fi, err := os.Lstat(d); err == nil && fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}
