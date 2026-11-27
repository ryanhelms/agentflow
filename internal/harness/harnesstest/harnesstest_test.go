package harnesstest_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/harness/harnesstest"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// fatalTB records a Fatalf instead of failing the real test.
type fatalTB struct {
	testing.TB
	mu    sync.Mutex
	fatal string
}

func (f *fatalTB) Fatalf(format string, args ...any) {
	f.mu.Lock()
	f.fatal = format
	f.mu.Unlock()
}

func (f *fatalTB) Helper() {}

func TestPATHSetsOnlyTemporaryDirectories(t *testing.T) {
	bin := t.TempDir()
	home := harnesstest.PATH(t, bin)
	entries := filepath.SplitList(os.Getenv("PATH"))
	if len(entries) == 0 || entries[0] != bin {
		t.Fatalf("PATH = %q, want %s first", os.Getenv("PATH"), bin)
	}
	tmp, _ := filepath.EvalSymlinks(os.TempDir())
	for _, e := range entries {
		real, err := filepath.EvalSymlinks(e)
		if err != nil || !strings.HasPrefix(real, tmp) {
			t.Errorf("PATH entry %s (%s) is outside %s", e, real, tmp)
		}
	}
	if os.Getenv("AGENTFLOW_HOME") != home || home == "" {
		t.Fatalf("AGENTFLOW_HOME = %q, returned %q", os.Getenv("AGENTFLOW_HOME"), home)
	}
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
		v, _ := filepath.EvalSymlinks(filepath.Dir(os.Getenv(name)))
		if !strings.HasPrefix(v, tmp) {
			t.Errorf("%s = %q is not temporary", name, os.Getenv(name))
		}
	}
	if got, err := config.HomeDir(); err != nil || got != home {
		t.Fatalf("config.HomeDir() = %q, %v; want the harnesstest home %s", got, err, home)
	}
	if os.Getenv("HOME") == home {
		t.Error("HOME and AGENTFLOW_HOME are the same directory")
	}
}

func TestPATHKeepsGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this machine")
	}
	harnesstest.PATH(t)
	out, err := exec.Command("git", "--version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "git version") {
		t.Fatalf("git --version = %q, %v", out, err)
	}
}

func TestPATHRefusesRealDirectories(t *testing.T) {
	for _, dir := range []string{"/usr/bin", "bin", ""} {
		f := &fatalTB{TB: t}
		harnesstest.PATH(f, dir)
		if f.fatal == "" {
			t.Errorf("PATH(%q) did not fail", dir)
		}
	}
}

func harnessSpec(t *testing.T, dir, name string) jsonrpc.LaunchSpec {
	t.Helper()
	data := t.TempDir()
	env, err := jsonrpc.HarnessEnv(os.Environ(), data, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	return jsonrpc.LaunchSpec{Path: filepath.Join(dir, name), Root: dir, Env: env}
}

func TestBinPrintsItsVersion(t *testing.T) {
	dir := harnesstest.Bin(t, "gemini", "0.21.3")
	harnesstest.PATH(t, dir)
	s := harnessSpec(t, dir, "gemini")
	s.Args = []string{"--version"}
	res, err := jsonrpc.Run(context.Background(), s, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || string(res.Stdout) != "gemini 0.21.3\n" {
		t.Fatalf("result = %+v (%q)", res, res.Stdout)
	}
	info, err := os.Stat(filepath.Join(dir, "gemini"))
	if err != nil || info.Mode().Perm()&0o022 != 0 {
		t.Fatalf("mode = %v, %v: must not be writable by group or others", info.Mode(), err)
	}
}

func TestStatusCLIRecordsNamesNotValues(t *testing.T) {
	dir := t.TempDir()
	log := harnesstest.StatusCLI(t, dir, "claude", 1, "Not logged in")
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret-value")
	s := harnessSpec(t, dir, "claude")
	work := filepath.Join(filepath.Dir(envValue(s.Env, "HOME")), "status")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	s.Dir = work
	s.Args = []string{"auth", "status"}
	res, err := jsonrpc.Run(context.Background(), s, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 1 || string(res.Stdout) != "Not logged in\n" {
		t.Fatalf("result = %+v (%q)", res, res.Stdout)
	}
	calls, err := log.Calls()
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls = %+v, %v", calls, err)
	}
	c := calls[0]
	wantDir, _ := filepath.EvalSymlinks(work)
	if c.Dir != wantDir || !slices.Equal(c.Args, []string{"auth", "status"}) {
		t.Fatalf("call = %+v, want dir %s", c, wantDir)
	}
	if !slices.Contains(c.Env, "HOME") || !slices.Contains(c.Env, "PATH") {
		t.Fatalf("env names = %v", c.Env)
	}
	for _, n := range append([]string{"ANTHROPIC_API_KEY"}, harnesstest.ShellVariables...) {
		if slices.Contains(c.Env, n) {
			t.Errorf("env names include %s", n)
		}
	}
	for _, n := range c.Env {
		if !jsonrpc.EnvAllowed(n) {
			t.Errorf("%s reached the fake but EnvAllowed refuses it", n)
		}
	}
}

func TestStatusCLIHangsUntilKilled(t *testing.T) {
	dir := t.TempDir()
	log := harnesstest.StatusCLI(t, dir, "codex", -1, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := jsonrpc.Run(ctx, harnessSpec(t, dir, "codex"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.ExitCode != -1 {
		t.Fatalf("result = %+v", res)
	}
	if calls, err := log.Calls(); err != nil || len(calls) != 1 {
		t.Fatalf("calls = %+v, %v", calls, err)
	}
}

func envValue(env []string, name string) string {
	for _, kv := range env {
		if n, v, _ := strings.Cut(kv, "="); n == name {
			return v
		}
	}
	return ""
}

func TestPATHTurnsBothTogglesOff(t *testing.T) {
	harnesstest.PATH(t)
	u, err := config.LoadUser()
	if err != nil {
		t.Fatal(err)
	}
	if u.AI.Harnesses.AutoProbeOn() || u.AI.Harnesses.SignInCheckOn() {
		t.Fatalf("toggles = %v %v, want both off", u.AI.Harnesses.AutoProbeOn(), u.AI.Harnesses.SignInCheckOn())
	}
}

func TestWriteSettingsKeepsBothToggles(t *testing.T) {
	home := harnesstest.PATH(t)
	harnesstest.WriteSettings(t, home, harnesstest.Settings{AutoProbe: true, ProbeTimeout: 2 * time.Second,
		Overrides: map[string]harnessdef.Definition{"mine": {Command: "mine"}}})
	b, _ := os.ReadFile(filepath.Join(home, "config.yml"))
	if !strings.Contains(string(b), "sign_in_check: false") || !strings.Contains(string(b), "auto_probe: true") {
		t.Fatalf("config.yml = %s", b)
	}
	u, err := config.LoadUser()
	if err != nil {
		t.Fatal(err)
	}
	h := u.AI.Harnesses
	if !h.AutoProbeOn() || h.SignInCheckOn() || h.EffectiveProbeTimeout() != 2*time.Second || h.Overrides["mine"].Command != "mine" {
		t.Fatalf("harnesses = %+v", h)
	}
}
