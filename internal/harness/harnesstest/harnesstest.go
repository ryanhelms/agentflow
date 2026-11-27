// Package harnesstest keeps harness tests away from real harnesses ("Phase
// 6 test guard" in docs/knowledge/engineering/decisions/event-driven-platform.md).
//
//   - PATH points PATH, HOME and AGENTFLOW_HOME at temporary directories.
//     Every test that reaches harness detection, in any package, calls it.
//   - Bin writes a fake harness that only prints a version, for finder and
//     version tests.
//   - StatusCLI writes a fake sign-in status command that records how it was
//     started (working directory, arguments, environment names, never
//     values).
//
// Fake ACP agents come from internal/acp/acptest (lane 6.2).
//
// The fakes are /bin/sh scripts, so Bin and StatusCLI skip the test on
// Windows, where agentflow refuses .cmd and .bat files by design.
package harnesstest

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// ShellVariables are the names a /bin/sh fake may add to its own
// environment (PWD, OLDPWD, SHLVL, _). StatusCLI leaves them out of
// StatusCall.Env, since they come from the shell, not from agentflow.
var ShellVariables = []string{"OLDPWD", "PWD", "SHLVL", "_"}

// PATH sets PATH to dirs, which must be absolute directories inside the
// test's temporary directory (os.TempDir()), plus one more temporary
// directory holding a link to git (so tests can still make repositories).
// It sets HOME (and USERPROFILE), the XDG base directories and
// AGENTFLOW_HOME to fresh temporary directories, and returns the agentflow
// home. It uses tb.Setenv, so the test cannot be parallel. The new HOME has
// no git configuration: a test that commits sets its own identity (git -c
// user.name=... -c user.email=..., or GIT_AUTHOR_* and GIT_COMMITTER_*).
//
// Not yet: writing auto_probe: false and sign_in_check: false into the
// home's config.yml. That needs the ai configuration keys and lands with
// them; until then nothing in agentflow reads those keys.
func PATH(tb testing.TB, dirs ...string) string {
	tb.Helper()
	tmp := tempRoots()
	for _, d := range dirs {
		if d == "" || !filepath.IsAbs(d) {
			tb.Fatalf("harnesstest.PATH: %q is not an absolute directory", d)
			return ""
		}
		info, err := os.Stat(d)
		if err != nil || !info.IsDir() {
			tb.Fatalf("harnesstest.PATH: %s is not a directory", d)
			return ""
		}
		if !insideAny(resolve(d), tmp) {
			tb.Fatalf("harnesstest.PATH: %s is outside the temporary directory %s", d, os.TempDir())
			return ""
		}
	}
	entries := append([]string{}, dirs...)
	if tools := toolsDir(tb); tools != "" {
		entries = append(entries, tools)
	}
	tb.Setenv("PATH", strings.Join(entries, string(filepath.ListSeparator)))

	home := tb.TempDir()
	tb.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		tb.Setenv("USERPROFILE", home)
	}
	tb.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	tb.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	tb.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	tb.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	agentflowHome := tb.TempDir()
	tb.Setenv("AGENTFLOW_HOME", agentflowHome)
	return agentflowHome
}

// toolsDir links git, found on the PATH in force before PATH changes it,
// into a fresh temporary directory. It returns "" when git is not found or
// links cannot be made.
func toolsDir(tb testing.TB) string {
	git, err := exec.LookPath("git")
	if err != nil {
		return ""
	}
	if git, err = filepath.Abs(git); err != nil {
		return ""
	}
	dir := tb.TempDir()
	if err := os.Symlink(git, filepath.Join(dir, filepath.Base(git))); err != nil {
		return ""
	}
	return dir
}

// Bin writes an executable name into a fresh temporary directory and
// returns the directory, for PATH(tb, Bin(tb, "gemini", "1.2.3")). Whatever
// its arguments, the executable prints "<name> <version>" and exits 0: it
// is found and has a version but does not speak ACP.
func Bin(tb testing.TB, name, version string) string {
	tb.Helper()
	skipWindows(tb)
	checkName(tb, name)
	dir := tb.TempDir()
	writeScript(tb, filepath.Join(dir, name), "printf '%s\\n' "+shQuote(name+" "+version)+"\n")
	return dir
}

// StatusCall is one run of a StatusCLI fake.
type StatusCall struct {
	Dir  string   // the working directory, links resolved
	Args []string // the arguments
	Env  []string // the environment's names, sorted, without ShellVariables
}

// StatusLog reads what a StatusCLI fake recorded.
type StatusLog struct {
	dir string
}

// StatusCLI writes an executable name in dir: a fake sign-in status
// command. Each run records its working directory, arguments and
// environment names (never values) under tb.TempDir(), prints line on
// stdout (nothing when line is empty) and exits with exit. An exit below 0
// makes it hang until it is killed, after recording.
func StatusCLI(tb testing.TB, dir, name string, exit int, line string) *StatusLog {
	tb.Helper()
	skipWindows(tb)
	checkName(tb, name)
	if strings.ContainsAny(line, "\n\r") {
		tb.Fatalf("harnesstest.StatusCLI: line must be one line")
	}
	rec := tb.TempDir()
	var b strings.Builder
	fmt.Fprintf(&b, "rec=%s/call-$$\n", shQuote(rec))
	b.WriteString("{\n")
	b.WriteString("  printf 'dir %s\\n' \"$(pwd -P)\"\n")
	b.WriteString("  for a in \"$@\"; do printf 'arg %s\\n' \"$a\"; done\n")
	b.WriteString("  /usr/bin/env | while IFS= read -r l; do\n")
	b.WriteString("    case $l in [A-Za-z_]*=*) printf 'env %s\\n' \"${l%%=*}\" ;; esac\n")
	b.WriteString("  done\n")
	b.WriteString("} > \"$rec\"\n")
	if line != "" {
		fmt.Fprintf(&b, "printf '%%s\\n' %s\n", shQuote(line))
	}
	if exit < 0 {
		b.WriteString("exec sleep 3600\n")
	} else {
		fmt.Fprintf(&b, "exit %d\n", exit)
	}
	writeScript(tb, filepath.Join(dir, name), b.String())
	return &StatusLog{dir: rec}
}

// Calls returns the recorded runs, oldest first.
func (l *StatusLog) Calls() ([]StatusCall, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, err
	}
	type rec struct {
		call StatusCall
		at   int64
		name string
	}
	var recs []rec
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "call-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		c, err := readCall(filepath.Join(l.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec{call: c, at: info.ModTime().UnixNano(), name: e.Name()})
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].at != recs[j].at {
			return recs[i].at < recs[j].at
		}
		return recs[i].name < recs[j].name
	})
	out := make([]StatusCall, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.call)
	}
	return out, nil
}

func readCall(path string) (StatusCall, error) {
	f, err := os.Open(path)
	if err != nil {
		return StatusCall{}, err
	}
	defer f.Close()
	var c StatusCall
	s := bufio.NewScanner(f)
	for s.Scan() {
		kind, value, _ := strings.Cut(s.Text(), " ")
		switch kind {
		case "dir":
			c.Dir = value
		case "arg":
			c.Args = append(c.Args, value)
		case "env":
			shell := false
			for _, n := range ShellVariables {
				shell = shell || n == value
			}
			if !shell {
				c.Env = append(c.Env, value)
			}
		}
	}
	sort.Strings(c.Env)
	return c, s.Err()
}

func skipWindows(tb testing.TB) {
	tb.Helper()
	if runtime.GOOS == "windows" {
		tb.Skip("harnesstest fakes are /bin/sh scripts; agentflow does not start .cmd or .bat files")
	}
}

func checkName(tb testing.TB, name string) {
	tb.Helper()
	if !harnessdef.ValidCommandName(name) {
		tb.Fatalf("harnesstest: %q is not a command name", name)
	}
}

// writeScript writes an executable /bin/sh script, mode 0755, so the PATH
// checks (not writable by group or others) accept it.
func writeScript(tb testing.TB, path, body string) {
	tb.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil { //nolint:gosec // a fake harness must be executable; 0755 is what the PATH checks accept
		tb.Fatal(err)
	}
}

// shQuote quotes s for /bin/sh.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func tempRoots() []string {
	tmp := filepath.Clean(os.TempDir())
	roots := []string{tmp}
	if real, err := filepath.EvalSymlinks(tmp); err == nil && real != tmp {
		roots = append(roots, real)
	}
	return roots
}

func resolve(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

func insideAny(path string, bases []string) bool {
	for _, base := range bases {
		rel, err := filepath.Rel(base, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}
