package update

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
)

// cacheFixture fakes the account's home and makes a repository and a folder
// outside every repository, all under t.TempDir().
type cacheFixture struct {
	account, repo, outside string
}

func realTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	f := &cacheFixture{account: realTempDir(t), repo: realTempDir(t), outside: realTempDir(t)}
	t.Cleanup(config.OverrideAccountHome(f.account))
	old := config.HomeWarnings
	config.HomeWarnings = io.Discard
	t.Cleanup(func() { config.HomeWarnings = old })
	git(t, f.repo, "init", "-q")
	git(t, f.account, "init", "-q") // a dotfiles repository at the account's home
	return f
}

// cacheCases are the inputs both the Go check and the launchers' shell check
// must answer the same way.
func (f *cacheFixture) cases() []struct {
	name string
	env  map[string]string
	want string
} {
	own := filepath.Join(f.account, ".cache", "agentflow")
	return []struct {
		name string
		env  map[string]string
		want string
	}{
		{"XDG_CACHE_HOME outside", map[string]string{"XDG_CACHE_HOME": f.outside, "HOME": f.repo}, filepath.Join(f.outside, "agentflow")},
		{"HOME outside", map[string]string{"HOME": f.outside}, filepath.Join(f.outside, ".cache", "agentflow")},
		{"repository-set XDG_CACHE_HOME", map[string]string{"XDG_CACHE_HOME": f.repo + "/.cache", "HOME": f.outside}, own},
		{"repository-set HOME", map[string]string{"HOME": f.repo}, own},
		{"empty XDG_CACHE_HOME and repository-set HOME", map[string]string{"XDG_CACHE_HOME": "", "HOME": f.repo}, own},
		{"XDG_CACHE_HOME in the git directory", map[string]string{"XDG_CACHE_HOME": f.repo + "/.git"}, own},
		{"relative XDG_CACHE_HOME", map[string]string{"XDG_CACHE_HOME": ".cache", "HOME": f.outside}, own},
		{"XDG_CACHE_HOME with ..", map[string]string{"XDG_CACHE_HOME": f.repo + "/../" + filepath.Base(f.outside)}, own},
		{"nothing set", map[string]string{}, own},
		{"the account's own cache", map[string]string{"HOME": f.account}, own},
		{"inside the dotfiles repository at the account home", map[string]string{"XDG_CACHE_HOME": f.account + "/xdg"}, filepath.Join(f.account, "xdg", "agentflow")},
	}
}

func TestCacheRootNeverInsideRepository(t *testing.T) {
	f := newCacheFixture(t)
	for _, tc := range f.cases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cacheRoot("linux", func(k string) string { return tc.env[k] }, ownCacheRoot)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("cacheRoot = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCacheRootWindowsLOCALAPPDATA(t *testing.T) {
	f := newCacheFixture(t)
	own := filepath.Join(f.account, "AppData", "Local", "agentflow")
	ownFn := func(string) (string, error) { return own, nil }
	for name, tc := range map[string]struct{ local, want string }{
		"outside":                   {f.outside, f.outside + string(filepath.Separator) + "agentflow"},
		"repository-set":            {f.repo, own},
		"repository-set git dir":    {filepath.Join(f.repo, ".git"), own},
		"not set, HOME is not used": {"", own},
		"relative":                  {"AppData", own},
		"the account's own folder":  {filepath.Dir(own), own},
	} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{"LOCALAPPDATA": tc.local, "HOME": f.outside, "XDG_CACHE_HOME": f.outside}
			got, err := cacheRoot("windows", func(k string) string { return env[k] }, ownFn)
			if err != nil {
				t.Fatal(err)
			}
			if got != filepath.Clean(tc.want) {
				t.Fatalf("cacheRoot = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestLauncherShellCheckMatchesCacheRoot runs the cache-root block of the
// launcher under every POSIX shell present and checks that it picks the same
// folder as CacheRoot, so self-update writes where the launchers read.
func TestLauncherShellCheckMatchesCacheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX launcher")
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "templates", "repo", ".agentflow", "bin", "agentflow"))
	if err != nil {
		t.Fatal(err)
	}
	block := launcherBlock(t, string(src))
	f := newCacheFixture(t)
	// The shipped block reads the account from the user database; here it is the fixture's.
	script := block + "\naf_account_home() { printf '%s\\n' \"$TEST_ACCOUNT\"; }\naf_cache_root\n"
	shells := 0
	for _, sh := range []string{"/bin/sh", "/bin/dash", "/bin/bash", "/usr/bin/dash"} {
		if _, err := os.Stat(sh); err != nil {
			continue
		}
		shells++
		for _, tc := range f.cases() {
			t.Run(sh+"/"+tc.name, func(t *testing.T) {
				cmd := exec.Command(sh, "-euc", script)
				cmd.Dir = f.outside
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TEST_ACCOUNT=" + f.account}
				for k, v := range tc.env {
					cmd.Env = append(cmd.Env, k+"="+v)
				}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("%s: %v %s", sh, err, stderr.String())
				}
				got := filepath.Clean(strings.TrimSuffix(string(out), "\n"))
				if got != tc.want {
					t.Fatalf("%s picked %s, CacheRoot picks %s (stderr %q)", sh, got, tc.want, stderr.String())
				}
				if tc.want == filepath.Join(f.account, ".cache", "agentflow") && len(tc.env) > 0 &&
					tc.env["HOME"] != f.account && !strings.Contains(stderr.String(), "agentflow: ignoring the cache") {
					t.Fatalf("%s gave no warning: %q", sh, stderr.String())
				}
			})
		}
	}
	if shells == 0 {
		t.Skip("no POSIX shell")
	}
}

func launcherBlock(t *testing.T, src string) string {
	t.Helper()
	const begin, end = "# >>> agentflow cache root", "# <<< agentflow cache root\n"
	i, j := strings.Index(src, begin), strings.Index(src, end)
	if i < 0 || j < i {
		t.Fatal("launcher has no cache-root block")
	}
	return src[i : j+len(end)]
}
