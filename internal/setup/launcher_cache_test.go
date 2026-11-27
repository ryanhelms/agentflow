package setup

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The scripts that find or install the agentflow binary all carry the same
// cache-root check (issue #129). These tests keep the copies identical and run
// the real scripts against a repository-set HOME and XDG_CACHE_HOME.

var cacheRootScripts = []string{
	"templates/repo/.agentflow/bin/agentflow",
	"adapters/claude-code/bin/agentflow",
	".agentflow/bin/agentflow",
	"adapters/claude-code/bin/launch",
	"templates/repo/.githooks/commit-msg",
	"templates/repo/.githooks/pre-push",
	"install.sh",
}

var cacheRootPowerShell = []string{
	"templates/repo/.agentflow/bin/agentflow.ps1",
	"install.ps1",
}

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// cacheBlock returns the fenced cache-root block of src.
func cacheBlock(t *testing.T, name, src string) string {
	t.Helper()
	const begin, end = "# >>> agentflow cache root", "# <<< agentflow cache root"
	i, j := strings.Index(src, begin), strings.Index(src, end)
	if i < 0 || j < i || strings.Count(src, begin) != 1 {
		t.Fatalf("%s must have exactly one cache-root block", name)
	}
	return src[i : j+len(end)]
}

// sameBlock is cacheBlock with each line's leading indentation removed.
func sameBlock(t *testing.T, name, src string) string {
	t.Helper()
	lines := strings.Split(cacheBlock(t, name, src), "\n")
	for k, l := range lines {
		lines[k] = strings.TrimLeft(l, " ")
	}
	return strings.Join(lines, "\n")
}

func TestCacheRootBlockIsTheSameEverywhere(t *testing.T) {
	for _, set := range [][]string{cacheRootScripts, cacheRootPowerShell} {
		want := sameBlock(t, set[0], repoFile(t, set[0]))
		for _, rel := range set[1:] {
			if got := sameBlock(t, rel, repoFile(t, rel)); got != want {
				t.Errorf("the cache-root block in %s differs from %s; copy it from there", rel, set[0])
			}
		}
	}
	// Nothing else reads the cache variables directly.
	for _, rel := range append(append([]string{}, cacheRootScripts...), cacheRootPowerShell...) {
		src := repoFile(t, rel)
		rest := strings.Replace(src, cacheBlock(t, rel, src), "", 1)
		for _, v := range []string{"XDG_CACHE_HOME", "$HOME", "LOCALAPPDATA"} {
			for _, l := range strings.Split(rest, "\n") {
				if strings.Contains(l, v) && !strings.HasPrefix(strings.TrimSpace(l), "#") &&
					!strings.Contains(l, "candidate") {
					t.Errorf("%s reads %s outside the cache-root block: %s", rel, v, strings.TrimSpace(l))
				}
			}
		}
	}
}

// launcherEnv runs scripts with a PATH whose `id` cannot name the account, so
// the account's own cache is unknown and the developer's real home is never
// used, a `gh` that only records that it ran, and git variables pointing away.
type launcherEnv struct {
	dir, repo, outside, marker, ghLog string
	path                              string
}

func newLauncherEnv(t *testing.T) *launcherEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX launchers")
	}
	e := &launcherEnv{dir: realTemp(t)}
	e.repo = filepath.Join(e.dir, "repo")
	e.outside = filepath.Join(e.dir, "outside")
	e.marker = filepath.Join(e.dir, "planted-ran")
	e.ghLog = filepath.Join(e.dir, "gh-ran")
	bin := filepath.Join(e.dir, "bin")
	for _, d := range []string{e.repo, e.outside, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, e.repo, "init", "-q")
	writeExec(t, filepath.Join(bin, "id"), "#!/bin/sh\nexit 1\n")
	writeExec(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho \"$*\" >> '"+e.ghLog+"'\nexit 1\n")
	// A git that lies: the check must not ask git anything.
	writeExec(t, filepath.Join(bin, "git"), "#!/bin/sh\ncase \"$1\" in rev-parse) echo /; exit 0;; esac\nexec "+gitPath(t)+" \"$@\"\n")
	e.path = bin + ":/usr/bin:/bin"
	return e
}

func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func gitPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	return p
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// plant puts a binary at cache/agentflow/current/agentflow that records that it ran.
func (e *launcherEnv) plant(t *testing.T, cache string) {
	t.Helper()
	writeExec(t, filepath.Join(cache, "agentflow", "1.0.0", "agentflow"), "#!/bin/sh\necho \"$*\" > '"+e.marker+"'\n")
	if err := os.Symlink(filepath.Join(cache, "agentflow", "1.0.0"), filepath.Join(cache, "agentflow", "current")); err != nil {
		t.Fatal(err)
	}
}

func (e *launcherEnv) run(t *testing.T, env map[string]string, stdin string, shell string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(shell, args...)
	cmd.Dir = e.repo
	cmd.Env = []string{"PATH=" + e.path, "TMPDIR=" + e.dir, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_DIR=" + e.outside, "GIT_WORK_TREE=" + e.outside}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (e *launcherEnv) ran() bool {
	_, err := os.Stat(e.marker)
	return err == nil
}

func shells(t *testing.T) []string {
	var out []string
	for _, sh := range []string{"/bin/sh", "/bin/dash", "/usr/bin/dash", "/bin/bash"} {
		if _, err := os.Stat(sh); err == nil {
			out = append(out, sh)
		}
	}
	if len(out) == 0 {
		t.Skip("no POSIX shell")
	}
	return out
}

func TestLaunchersNeverRunABinaryFromARepository(t *testing.T) {
	scripts := map[string][]string{
		"project launcher": {repoPath("templates/repo/.agentflow/bin/agentflow"), "--version"},
		"plugin launcher":  {repoPath("adapters/claude-code/bin/agentflow"), "--version"},
		"plugin hooks":     {repoPath("adapters/claude-code/bin/launch"), "hook", "session-start"},
	}
	for _, sh := range shells(t) {
		for name, argv := range scripts {
			for _, tc := range []struct {
				name  string
				setup func(e *launcherEnv) map[string]string
			}{
				{"repository-set XDG_CACHE_HOME", func(e *launcherEnv) map[string]string {
					e.plant(t, filepath.Join(e.repo, ".cache"))
					return map[string]string{"XDG_CACHE_HOME": filepath.Join(e.repo, ".cache"), "HOME": e.outside}
				}},
				{"repository-set HOME", func(e *launcherEnv) map[string]string {
					e.plant(t, filepath.Join(e.repo, ".cache"))
					return map[string]string{"HOME": e.repo}
				}},
				{"link outside into the repository", func(e *launcherEnv) map[string]string {
					e.plant(t, filepath.Join(e.repo, "c"))
					link := filepath.Join(e.outside, "cache")
					if err := os.Symlink(filepath.Join(e.repo, "c"), link); err != nil {
						t.Fatal(err)
					}
					return map[string]string{"XDG_CACHE_HOME": link, "HOME": e.outside}
				}},
				{"inside the git directory", func(e *launcherEnv) map[string]string {
					e.plant(t, filepath.Join(e.repo, ".git", "c"))
					return map[string]string{"XDG_CACHE_HOME": filepath.Join(e.repo, ".git", "c"), "HOME": e.outside}
				}},
				{"bare repository", func(e *launcherEnv) map[string]string {
					bare := filepath.Join(e.dir, "bare.git")
					if err := os.Mkdir(bare, 0o755); err != nil {
						t.Fatal(err)
					}
					runGit(t, bare, "init", "-q", "--bare")
					e.plant(t, bare)
					return map[string]string{"XDG_CACHE_HOME": bare, "HOME": e.outside}
				}},
				{"current links into a repository", func(e *launcherEnv) map[string]string {
					writeExec(t, filepath.Join(e.repo, "v", "agentflow"), "#!/bin/sh\necho \"$*\" > '"+e.marker+"'\n")
					if err := os.MkdirAll(filepath.Join(e.outside, "c", "agentflow"), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(e.repo, "v"), filepath.Join(e.outside, "c", "agentflow", "current")); err != nil {
						t.Fatal(err)
					}
					return map[string]string{"XDG_CACHE_HOME": filepath.Join(e.outside, "c"), "HOME": e.outside}
				}},
			} {
				t.Run(sh+"/"+name+"/"+tc.name, func(t *testing.T) {
					e := newLauncherEnv(t)
					env := tc.setup(e)
					out, _ := e.run(t, env, "", sh, argv...)
					if e.ran() {
						t.Fatalf("ran the binary from the repository: %s", out)
					}
					if !strings.Contains(out, "agentflow:") {
						t.Fatalf("no message on stderr: %q", out)
					}
				})
			}
		}
	}
}

func TestLaunchersStillUseACacheOutsideRepositories(t *testing.T) {
	for _, sh := range shells(t) {
		for _, argv := range [][]string{
			{repoPath("templates/repo/.agentflow/bin/agentflow"), "--version"},
			{repoPath("adapters/claude-code/bin/launch"), "hook", "session-start"},
		} {
			t.Run(sh+"/"+filepath.Base(argv[0]), func(t *testing.T) {
				e := newLauncherEnv(t)
				e.plant(t, filepath.Join(e.outside, ".cache"))
				out, err := e.run(t, map[string]string{"HOME": e.outside}, "", sh, argv...)
				if err != nil || !e.ran() {
					t.Fatalf("did not run the installed binary: %v %s", err, out)
				}
				if out != "" {
					t.Fatalf("unexpected output: %q", out)
				}
			})
		}
	}
}

func TestGitHooksNeverRunABinaryFromARepository(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	msg := filepath.Join(t.TempDir(), "MSG")
	if err := os.WriteFile(msg, []byte("feat(PLAT-1): add a thing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		hook, stdin string
		args        []string
	}{
		{"commit-msg", "", []string{msg}},
		{"pre-push", "refs/heads/x 1 refs/heads/x 2\n", []string{"origin", "url"}},
	} {
		for _, home := range []string{"XDG_CACHE_HOME", "HOME"} {
			t.Run(tc.hook+"/"+home, func(t *testing.T) {
				e := newLauncherEnv(t)
				e.plant(t, filepath.Join(e.repo, ".cache"))
				env := map[string]string{"HOME": e.repo}
				if home == "XDG_CACHE_HOME" {
					env = map[string]string{"XDG_CACHE_HOME": filepath.Join(e.repo, ".cache"), "HOME": e.outside}
				}
				// The hooks run git themselves; give them the repository.
				env["GIT_DIR"], env["GIT_WORK_TREE"] = filepath.Join(e.repo, ".git"), e.repo
				// A real git, and no agentflow anywhere on PATH.
				realGit := filepath.Join(e.dir, "bin", "git")
				_ = os.Remove(realGit)
				if err := os.Symlink(gitPath(t), realGit); err != nil {
					t.Fatal(err)
				}
				out, err := e.run(t, env, tc.stdin, bash, append([]string{repoPath("templates/repo/.githooks/" + tc.hook)}, tc.args...)...)
				if e.ran() {
					t.Fatalf("ran the binary from the repository: %s", out)
				}
				if err != nil {
					t.Fatalf("built-in fallback failed: %v %s", err, out)
				}
				if !strings.Contains(out, "agentflow: not using the cache") {
					t.Fatalf("no message: %q", out)
				}
			})
		}
	}
}

func repoPath(rel string) string {
	p, _ := filepath.Abs(filepath.Join("..", "..", filepath.FromSlash(rel)))
	return p
}
