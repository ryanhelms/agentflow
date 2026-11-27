package config

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// homeFixture fakes the OS account's home and captures warnings. Every path
// is under t.TempDir(); the developer's own home is never read.
type homeFixture struct {
	account  string
	warnings *bytes.Buffer
}

func newHomeFixture(t *testing.T) *homeFixture {
	t.Helper()
	f := &homeFixture{account: realDir(t, t.TempDir()), warnings: &bytes.Buffer{}}
	oldWarnings := HomeWarnings
	restore := OverrideAccountHome(f.account)
	HomeWarnings = f.warnings
	resetHomeWarnings()
	t.Cleanup(func() {
		restore()
		HomeWarnings = oldWarnings
		resetHomeWarnings()
	})
	t.Setenv("AGENTFLOW_HOME", "")
	t.Setenv("DEVFLOW_HOME", "")
	t.Setenv("HOME", f.account)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return f
}

func resetHomeWarnings() {
	homeWarned.Clear()
	homeNotes.Lock()
	homeNotes.list = nil
	homeNotes.Unlock()
}

func realDir(t *testing.T, dir string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// newRepo returns a git repository with one commit, under t.TempDir().
func newRepo(t *testing.T) string {
	t.Helper()
	dir := realDir(t, t.TempDir())
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "-c", "user.email=dev@example.invalid", "-c", "user.name=Dev", "commit", "-q", "--allow-empty", "-m", "chore: init")
	return dir
}

// wantIgnored checks that HomeDir ignored the folder from source and used
// the account's own folder instead.
func (f *homeFixture) wantIgnored(t *testing.T, source string) {
	t.Helper()
	got, err := HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(f.account, ".agentflow"); got != want {
		t.Fatalf("HomeDir() = %s, want the account's folder %s", got, want)
	}
	w := f.warnings.String()
	if !strings.Contains(w, "ignoring the agentflow home") || !strings.Contains(w, "(from "+source+")") ||
		!strings.Contains(w, "AGENTFLOW_HOME") {
		t.Fatalf("warning does not name %s and the fix: %q", source, w)
	}
}

func (f *homeFixture) wantUsed(t *testing.T, want string) {
	t.Helper()
	got, err := HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("HomeDir() = %s, want %s", got, want)
	}
	if f.warnings.Len() != 0 {
		t.Fatalf("unexpected warning: %s", f.warnings)
	}
}

func TestHomeOutsideRepositoryIsUsed(t *testing.T) {
	f := newHomeFixture(t)
	home := filepath.Join(t.TempDir(), "scratch-home")
	t.Setenv("AGENTFLOW_HOME", home)
	f.wantUsed(t, home)
}

func TestHomeInsideRepositoryIsIgnored(t *testing.T) {
	repo := newRepo(t)
	worktree := filepath.Join(realDir(t, t.TempDir()), "linked")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "feature", worktree)
	if err := os.MkdirAll(filepath.Join(repo, "sub", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := realDir(t, t.TempDir())
	if err := os.Symlink(filepath.Join(repo, "sub"), filepath.Join(outside, "into-repo")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"primary checkout":             filepath.Join(repo, ".agentflow-home"),
		"checkout root":                repo,
		"git directory":                filepath.Join(repo, ".git", "agentflow"),
		"linked worktree":              filepath.Join(worktree, "home"),
		"linked worktree's git dir":    filepath.Join(repo, ".git", "worktrees", "linked"),
		"missing folder in repository": filepath.Join(repo, "a", "b", "c"),
		"link into repository":         filepath.Join(outside, "into-repo", "home"),
		"link then ..":                 filepath.Join(outside, "into-repo") + "/deep/../../home",
		"relative":                     "sub/home",
	}
	for name, home := range cases {
		t.Run(name, func(t *testing.T) {
			f := newHomeFixture(t)
			t.Chdir(repo)
			t.Setenv("AGENTFLOW_HOME", home)
			f.wantIgnored(t, "AGENTFLOW_HOME")
		})
	}
}

// ".." is applied after links: a lexical path inside the repository that
// physically leaves it is not inside it.
func TestHomeDotDotThroughLinkLeavingRepository(t *testing.T) {
	f := newHomeFixture(t)
	repo := newRepo(t)
	elsewhere := realDir(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(elsewhere, "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "deep"), filepath.Join(repo, "out")); err != nil {
		t.Fatal(err)
	}
	home := repo + "/out/../home"
	t.Setenv("AGENTFLOW_HOME", home)
	f.wantUsed(t, home)
}

func TestHomeEveryRepositoryIsChecked(t *testing.T) {
	// The console daemon and the MCP server serve several repositories from
	// one process: a home inside any of them is ignored, whatever the
	// current directory.
	f := newHomeFixture(t)
	served := newRepo(t)
	t.Chdir(newRepo(t))
	t.Setenv("AGENTFLOW_HOME", filepath.Join(served, "home"))
	f.wantIgnored(t, "AGENTFLOW_HOME")
}

func TestLegacyDevflowHomeInsideRepositoryIsIgnored(t *testing.T) {
	f := newHomeFixture(t)
	t.Setenv("DEVFLOW_HOME", filepath.Join(newRepo(t), "home"))
	f.wantIgnored(t, "DEVFLOW_HOME")
}

func TestRepositorySetHOMEIsIgnored(t *testing.T) {
	f := newHomeFixture(t)
	repo := newRepo(t)
	if err := os.Mkdir(filepath.Join(repo, ".agentflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".agentflow", "config.yml"), []byte("autonomy: {local: auto, remote: auto}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", repo)
	f.wantIgnored(t, "HOME")
	u, err := LoadUser()
	if err != nil {
		t.Fatal(err)
	}
	if u.Autonomy.Remote != Confirm || u.Autonomy.Local != Confirm {
		t.Errorf("the repository supplied the personal autonomy: %+v", u.Autonomy)
	}
}

func TestIgnoredHomeFallsBackToLegacyAccountFolder(t *testing.T) {
	f := newHomeFixture(t)
	legacy := filepath.Join(f.account, ".devflow")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_HOME", filepath.Join(newRepo(t), "home"))
	if got, err := HomeDir(); err != nil || got != legacy {
		t.Fatalf("HomeDir() = %s, %v, want %s", got, err, legacy)
	}
}

func TestHomeWarningIsPrintedOnce(t *testing.T) {
	f := newHomeFixture(t)
	t.Setenv("AGENTFLOW_HOME", filepath.Join(newRepo(t), "home"))
	for range 3 {
		if _, err := HomeDir(); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(f.warnings.String(), "ignoring"); n != 1 {
		t.Errorf("warned %d times: %s", n, f.warnings)
	}
}

func TestHomeInsideRepositoryWithoutAccountHomeIsRefused(t *testing.T) {
	newHomeFixture(t)
	accountHome = func() (string, error) { return "", errors.New("no account") } // restored by the fixture
	t.Setenv("AGENTFLOW_HOME", filepath.Join(newRepo(t), "home"))
	_, err := HomeDir()
	var refused *HomeInRepositoryError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "AGENTFLOW_HOME") {
		t.Fatalf("HomeDir() error = %v, want a refusal naming AGENTFLOW_HOME", err)
	}
	if _, err := Home(); err == nil {
		t.Fatal("Home() created a folder inside the repository")
	}
}

func TestAccountHomeThatIsARepository(t *testing.T) {
	// Dotfiles: the OS account's home is itself a git repository.
	f := newHomeFixture(t)
	gitRun(t, f.account, "init", "-q")
	f.wantUsed(t, filepath.Join(f.account, ".agentflow"))

	custom := filepath.Join(f.account, "agentflow-custom")
	t.Setenv("AGENTFLOW_HOME", custom)
	f.wantUsed(t, custom)

	// A repository cloned under that home is still a repository.
	clone := filepath.Join(f.account, "code", "evil")
	if err := os.MkdirAll(clone, 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, clone, "init", "-q")
	t.Setenv("AGENTFLOW_HOME", filepath.Join(clone, "home"))
	f.wantIgnored(t, "AGENTFLOW_HOME")
}

func TestHOMEThatIsNotTheAccountHomeGetsNoDotfilesException(t *testing.T) {
	// $HOME can be set by the repository; only the account's home from
	// os/user counts as a dotfiles repository.
	f := newHomeFixture(t)
	repo := newRepo(t)
	t.Setenv("HOME", repo)
	t.Setenv("AGENTFLOW_HOME", filepath.Join(repo, "custom"))
	f.wantIgnored(t, "AGENTFLOW_HOME")
}

func TestAccountFolderLinkedIntoDotfilesCheckout(t *testing.T) {
	f := newHomeFixture(t)
	dotfiles := newRepo(t)
	target := filepath.Join(dotfiles, "agentflow")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(f.account, ".agentflow")); err != nil {
		t.Fatal(err)
	}
	f.wantUsed(t, filepath.Join(f.account, ".agentflow"))
	t.Setenv("AGENTFLOW_HOME", filepath.Join(f.account, ".agentflow"))
	f.wantUsed(t, filepath.Join(f.account, ".agentflow"))
}

func TestPersonalConfigLinkedIntoDotfilesCheckout(t *testing.T) {
	f := newHomeFixture(t)
	dotfiles := newRepo(t)
	target := filepath.Join(dotfiles, "agentflow.yml")
	if err := os.WriteFile(target, []byte("autonomy: {local: auto}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, "config.yml")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_HOME", home)
	f.wantUsed(t, home)
	u, err := LoadUser()
	if err != nil {
		t.Fatal(err)
	}
	if u.Autonomy.Local != Auto {
		t.Errorf("linked config.yml not read: %+v", u.Autonomy)
	}
}

// A folder whose location cannot be checked is not used.
func TestHomeThatCannotBeResolvedIsIgnored(t *testing.T) {
	f := newHomeFixture(t)
	dir := realDir(t, t.TempDir())
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_HOME", filepath.Join(loop, "home"))
	got, err := HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(f.account, ".agentflow"); got != want {
		t.Fatalf("HomeDir() = %s, want %s", got, want)
	}
	if !strings.Contains(f.warnings.String(), "cannot be checked") {
		t.Errorf("warning = %q", f.warnings)
	}
}

func TestHomeNoticesKeepTheWarning(t *testing.T) {
	newHomeFixture(t)
	t.Setenv("AGENTFLOW_HOME", filepath.Join(newRepo(t), "home"))
	if _, err := HomeDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := HomeDir(); err != nil {
		t.Fatal(err)
	}
	notes := HomeNotices()
	if len(notes) != 1 || !strings.Contains(notes[0], "AGENTFLOW_HOME") {
		t.Fatalf("HomeNotices() = %q", notes)
	}
}

func TestAccountHomeIsNotTakenFromHOME(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := lookupAccountHome()
	if err != nil {
		t.Skipf("this account has no passwd entry: %v", err)
	}
	if got == os.Getenv("HOME") {
		t.Fatalf("lookupAccountHome() = %s, the value of $HOME", got)
	}
}

// In a test binary, HomeDir never falls back to the developer's real account
// folder: a test that has not called OverrideAccountHome gets an error.
func TestTestBinaryNeverFallsBackToRealAccountHome(t *testing.T) {
	if accountOverridden {
		t.Fatal("the account home is overridden outside a fixture")
	}
	t.Setenv("DEVFLOW_HOME", "")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("AGENTFLOW_HOME", filepath.Join(newRepo(t), "home"))
	got, err := HomeDir()
	if err == nil || !strings.Contains(err.Error(), "OverrideAccountHome") {
		t.Fatalf("HomeDir() = %q, %v; want an error, not the real account folder", got, err)
	}
	if _, err := Home(); err == nil {
		t.Fatal("Home() succeeded")
	}
}

func TestInsideRepository(t *testing.T) {
	f := newHomeFixture(t)
	repo := newRepo(t)
	worktree := filepath.Join(realDir(t, t.TempDir()), "linked")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "feature", worktree)
	outside := realDir(t, t.TempDir())
	if err := os.Symlink(repo, filepath.Join(outside, "link")); err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(outside, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.account, "init", "-q")
	cases := map[string]bool{
		filepath.Join(outside, "codex"):          false,
		filepath.Join(repo, ".codex"):            true,
		filepath.Join(repo, ".git", "x"):         true,
		filepath.Join(worktree, "xdg", "config"): true,
		filepath.Join(outside, "link", "cfg"):    true,
		outside + "/link/../cfg":                 false,
		filepath.Join(loop, "x"):                 true,
		filepath.Join(f.account, ".config"):      false,
		filepath.Join(f.account, "code", "x"):    false,
	}
	for dir, want := range cases {
		inside, why := InsideRepository(dir)
		if inside != want {
			t.Errorf("InsideRepository(%s) = %v (%s), want %v", dir, inside, why, want)
		}
		if inside == (why == "") {
			t.Errorf("InsideRepository(%s): inside %v with why %q", dir, inside, why)
		}
	}
}
