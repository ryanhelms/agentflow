package harness

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/harness/harnesstest"
)

// statusFound is a found harness whose sign-in check runs a StatusCLI fake.
func statusFound(t *testing.T, exit int, line string, d harnessdef.Definition) (Found, *harnesstest.StatusLog) {
	t.Helper()
	dir := t.TempDir()
	log := harnesstest.StatusCLI(t, dir, "fake-cli", exit, line)
	path := filepath.Join(dir, "fake-cli")
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.StatusFrom == "" {
		d.StatusFrom = harnessdef.StatusFromSelf
	}
	if d.Kind == "" {
		d.Kind = harnessdef.KindNative
	}
	return Found{ID: "demo", Definition: d, Path: path, Target: target, PathDir: dir, Identity: identity("demo" + path)}, log
}

func newTestCheck(t *testing.T, parent ...string) (*SignInCheck, string) {
	t.Helper()
	harnesstest.PATH(t)
	home := t.TempDir()
	c := NewSignInCheck(home)
	// The parent is this process's environment without any credential
	// variable (an agent session running the tests may have some), plus
	// the test's own.
	var env []string
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !harnessdef.CredentialSuffix(name) {
			env = append(env, kv)
		}
	}
	env = append(env, parent...)
	c.Environ = func() []string { return env }
	c.timeout = 3 * time.Second
	return c, home
}

func person() SignInRequest {
	return SignInRequest{Requester: RequesterPerson, Enabled: true, AutoProbe: true}
}

func calls(t *testing.T, l *harnesstest.StatusLog) []harnesstest.StatusCall {
	t.Helper()
	c, err := l.Calls()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSignInVerdicts(t *testing.T) {
	cases := []struct {
		name   string
		exit   int
		line   string
		def    harnessdef.Definition
		parent []string
		status string
		code   string
	}{
		{"signed in", 0, "", harnessdef.Definition{}, nil, SignedIn, CodeOK},
		{"signed out", 1, "", harnessdef.Definition{}, nil, SignedOut, CodeSignedOut},
		{"unmapped exit", 2, "", harnessdef.Definition{}, nil, SignInUnknown, CodeExitCode},
		{"own exit codes", 7, "", harnessdef.Definition{StatusSignedIn: []int{3}, StatusSignedOut: []int{7}}, nil, SignedOut, CodeSignedOut},
		{"signed-out line present", 1, "Not logged in", harnessdef.Definition{StatusSignedOutLine: "Not logged in"}, nil, SignedOut, CodeSignedOut},
		{"signed-out line missing", 1, "Error: network", harnessdef.Definition{StatusSignedOutLine: "Not logged in"}, nil, SignInUnknown, CodeExitCode},
		{"generic credential", 1, "", harnessdef.Definition{}, []string{"ANTHROPIC_API_KEY=sk-x"}, SignInUnknown, CodeEnvCredential},
		{"listed credential", 1, "", harnessdef.Definition{CredentialEnv: []string{"DEMO_LOGIN"}}, []string{"DEMO_LOGIN=1"}, SignInUnknown, CodeEnvCredential},
		{"empty credential", 1, "", harnessdef.Definition{}, []string{"OPENAI_API_KEY="}, SignedOut, CodeSignedOut},
		{"credential and signed in", 0, "", harnessdef.Definition{}, []string{"ANTHROPIC_API_KEY=sk-x"}, SignedIn, CodeOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			check, _ := newTestCheck(t, c.parent...)
			f, log := statusFound(t, c.exit, c.line, c.def)
			v := check.Check(context.Background(), f, person())
			if v.Status != c.status || v.Code != c.code || v.CheckedAt == nil {
				t.Fatalf("got %+v, want %s/%s", v, c.status, c.code)
			}
			if n := len(calls(t, log)); n != 1 {
				t.Errorf("ran %d times", n)
			}
		})
	}
}

func TestSignInSignedOutLineOnStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh fake")
	}
	check, _ := newTestCheck(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-cli")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'Not logged in\\n' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := Found{ID: "codex-acp", Definition: harnessdef.Definition{StatusFrom: "self", StatusSignedOutLine: "Not logged in"},
		Path: path, Target: path, PathDir: dir, Identity: identity("stderr")}
	f.Target, _ = filepath.EvalSymlinks(path)
	if v := check.Check(context.Background(), f, person()); v.Status != SignedOut {
		t.Errorf("got %+v", v)
	}
}

func TestSignInProfile(t *testing.T) {
	parent := []string{"GH_TOKEN=x", "GITHUB_TOKEN=x", "ANTHROPIC_API_KEY=x", "AWS_SECRET_ACCESS_KEY=x", "SSH_AUTH_SOCK=/x",
		"HTTPS_PROXY=http://p", "AGENTFLOW_HOME=/x", "CLAUDE_CODE_SESSION_ID=s", "NODE_OPTIONS=--require=x", "LC_TIME=C",
		"USER=me", "LOGNAME=me", "CODEX_HOME=", "DEMO_CONFIG=/tmp/x", "DEMO_LOGIN=1"}
	check, home := newTestCheck(t, parent...)
	d := harnessdef.Definition{StatusArgs: []string{"auth", "status"}, ConfigEnv: []string{"DEMO_CONFIG", "NODE_OPTIONS", "DEMO_LOGIN", "DEMO_API_KEY"},
		CredentialEnv: []string{"DEMO_LOGIN"}}
	f, log := statusFound(t, 0, "", d)
	statusDir := filepath.Join(home, "harnesses", "demo", "status")
	if err := os.MkdirAll(statusDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(statusDir, "left-over"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := check.Check(context.Background(), f, person()); v.Status != SignedIn {
		t.Fatalf("got %+v", v)
	}
	got := calls(t, log)
	if len(got) != 1 {
		t.Fatalf("calls %+v", got)
	}
	call := got[0]
	wantDir, _ := filepath.EvalSymlinks(statusDir)
	if call.Dir != wantDir {
		t.Errorf("dir %s, want %s", call.Dir, wantDir)
	}
	if !slices.Equal(call.Args, []string{"auth", "status"}) {
		t.Errorf("args %v", call.Args)
	}
	allowed := map[string]bool{"HOME": true, "USER": true, "LOGNAME": true, "LANG": true, "TZ": true, "TMPDIR": true, "PATH": true,
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true, "XDG_CACHE_HOME": true, "DEMO_CONFIG": true}
	for _, name := range call.Env {
		if !allowed[name] && !strings.HasPrefix(name, "LC_") {
			t.Errorf("the status command saw %s", name)
		}
	}
	for _, need := range []string{"HOME", "USER", "LC_TIME", "DEMO_CONFIG", "PATH"} {
		if !slices.Contains(call.Env, need) {
			t.Errorf("the status command did not get %s (%v)", need, call.Env)
		}
	}
	entries, _ := os.ReadDir(statusDir)
	if len(entries) != 0 {
		t.Errorf("status directory not emptied: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(home, "harnesses", "demo", "probe.log")); err == nil {
		t.Error("the sign-in check wrote probe.log")
	}
}

func TestSignInHomeIsTheParents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh fake")
	}
	check, _ := newTestCheck(t)
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "home")
	path := filepath.Join(dir, "fake-cli")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' \"$HOME\" > '"+out+"'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	target, _ := filepath.EvalSymlinks(path)
	f := Found{ID: "demo", Definition: harnessdef.Definition{StatusFrom: "self"}, Path: path, Target: target, PathDir: dir, Identity: identity("home")}
	check.Check(context.Background(), f, person())
	got, _ := os.ReadFile(out)
	if string(got) != os.Getenv("HOME") {
		t.Errorf("HOME = %q, want %q", got, os.Getenv("HOME"))
	}
}

func TestSignInWhoMayRun(t *testing.T) {
	check, _ := newTestCheck(t)
	f, log := statusFound(t, 0, "", harnessdef.Definition{})
	cases := []struct {
		r    SignInRequest
		code string
	}{
		{SignInRequest{Requester: RequesterPerson, Enabled: false, AutoProbe: true}, CodeOff},
		{SignInRequest{Requester: RequesterAgent, Enabled: false, AutoProbe: true}, CodeOff},
		{SignInRequest{Requester: RequesterDaemon, Enabled: true, AutoProbe: false}, CodeNeedsPerson},
		{SignInRequest{Requester: RequesterAgent, Enabled: true, AutoProbe: false}, CodeNeedsPerson},
		{SignInRequest{Requester: "", Enabled: true, AutoProbe: true}, CodeNeedsPerson},
		{SignInRequest{Requester: "mcp", Enabled: true, AutoProbe: true}, CodeNeedsPerson},
	}
	for _, c := range cases {
		if v := check.Check(context.Background(), f, c.r); v.Status != SignInUnknown || v.Code != c.code || v.CheckedAt != nil {
			t.Errorf("%+v: got %+v, want unknown/%s", c.r, v, c.code)
		}
	}
	if n := len(calls(t, log)); n != 0 {
		t.Fatalf("ran %d times while not allowed", n)
	}
	// A person's probe with auto_probe off still runs it.
	if v := check.Check(context.Background(), f, SignInRequest{Requester: RequesterPerson, Enabled: true}); v.Status != SignedIn {
		t.Errorf("person: %+v", v)
	}
	// The daemon and agents run it when both toggles are on.
	if v := check.Check(context.Background(), f, SignInRequest{Requester: RequesterDaemon, Enabled: true, AutoProbe: true}); v.Status != SignedIn {
		t.Errorf("daemon: %+v", v)
	}
	if n := len(calls(t, log)); n != 2 {
		t.Errorf("ran %d times", n)
	}
}

func TestSignInInterval(t *testing.T) {
	check, _ := newTestCheck(t)
	f, log := statusFound(t, 1, "", harnessdef.Definition{})
	recent := time.Now().Add(-time.Minute)
	prev := &SignInStatus{Status: SignedIn, Code: CodeOK, CheckedAt: &recent}
	if v := check.Check(context.Background(), f, SignInRequest{Requester: RequesterAgent, Enabled: true, AutoProbe: true, Previous: prev}); v.Status != SignedIn {
		t.Errorf("agent within 5 minutes: %+v", v)
	}
	if n := len(calls(t, log)); n != 0 {
		t.Fatalf("ran %d times inside the interval", n)
	}
	if v := check.Check(context.Background(), f, SignInRequest{Requester: RequesterPerson, Enabled: true, Previous: prev}); v.Status != SignedOut {
		t.Errorf("person: %+v", v)
	}
	old := time.Now().Add(-6 * time.Minute)
	prev.CheckedAt = &old
	if v := check.Check(context.Background(), f, SignInRequest{Requester: RequesterDaemon, Enabled: true, AutoProbe: true, Previous: prev}); v.Status != SignedOut {
		t.Errorf("daemon after 6 minutes: %+v", v)
	}
	if n := len(calls(t, log)); n != 2 {
		t.Errorf("ran %d times", n)
	}
}

func TestSignInBase(t *testing.T) {
	check, _ := newTestCheck(t)
	adapter, adapterLog := statusFound(t, 0, "", harnessdef.Definition{})
	baseDir := t.TempDir()
	baseLog := harnesstest.StatusCLI(t, baseDir, "base-cli", 1, "")
	basePath := filepath.Join(baseDir, "base-cli")

	f := adapter
	f.Definition = harnessdef.Definition{Kind: harnessdef.KindAdapter, StatusFrom: harnessdef.StatusFromBase, StatusArgs: []string{"login", "status"}}
	baseTarget, err := filepath.EvalSymlinks(basePath)
	if err != nil {
		t.Fatal(err)
	}
	f.BaseFound = &Found{Path: basePath, Target: baseTarget, PathDir: baseDir}
	if v := check.Check(context.Background(), f, person()); v.Status != SignedOut {
		t.Fatalf("base: %+v", v)
	}
	if got := calls(t, baseLog); len(got) != 1 || !slices.Equal(got[0].Args, []string{"login", "status"}) {
		t.Errorf("base calls %+v", got)
	}
	if n := len(calls(t, adapterLog)); n != 0 {
		t.Errorf("the adapter itself ran %d times", n)
	}

	missing := f
	missing.BaseFound = nil
	if v := check.Check(context.Background(), missing, person()); v.Code != CodeBaseMissing {
		t.Errorf("no base: %+v", v)
	}
	shown := f
	shown.BaseFound = &Found{Path: "~/bin/base-cli"}
	if v := check.Check(context.Background(), shown, person()); v.Code != CodeBaseMissing {
		t.Errorf("display path: %+v", v)
	}
	native := f
	native.Definition.Kind = harnessdef.KindNative
	if v := check.Check(context.Background(), native, person()); v.Code != CodeBaseMissing {
		t.Errorf("native with base: %+v", v)
	}
	notFound := f
	notFound.Path = "" // adapter_available: its base CLI is never started
	if v := check.Check(context.Background(), notFound, person()); v.Code != CodeNoCheck {
		t.Errorf("adapter not found: %+v", v)
	}
	if n := len(calls(t, baseLog)); n != 1 {
		t.Errorf("base ran %d times", n)
	}
}

func TestSignInRefusals(t *testing.T) {
	check, _ := newTestCheck(t)
	f, log := statusFound(t, 0, "", harnessdef.Definition{})
	none := f
	none.Definition.StatusFrom = ""
	if v := check.Check(context.Background(), none, person()); v.Code != CodeNoCheck {
		t.Errorf("no status command: %+v", v)
	}
	bad := f
	bad.Definition.StatusArgs = []string{"-p", "write a poem"}
	if v := check.Check(context.Background(), bad, person()); v.Code != CodeLaunch {
		t.Errorf("unsafe args: %+v", v)
	}
	if n := len(calls(t, log)); n != 0 {
		t.Fatalf("ran %d times", n)
	}
	if runtime.GOOS != "windows" {
		real := f
		real.Path, real.Target, real.PathDir = "/usr/bin/true", "/usr/bin/true", "/usr/bin"
		if t2, err := filepath.EvalSymlinks(real.Path); err == nil {
			real.Target = t2
		}
		if v := check.Check(context.Background(), real, person()); v.Code != CodeTestGuard {
			t.Errorf("real binary: %+v", v)
		}
	}
}

func TestSignInUnsafeEnvironment(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	repo := t.TempDir()
	if out, err := exec.Command(git, "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	inRepo := filepath.Join(repo, "config")
	if err := os.MkdirAll(inRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"XDG_CONFIG_HOME", "DEMO_CONFIG"} {
		check, _ := newTestCheck(t, name+"="+inRepo)
		f, log := statusFound(t, 0, "", harnessdef.Definition{ConfigEnv: []string{"DEMO_CONFIG"}})
		if v := check.Check(context.Background(), f, person()); v.Status != SignInUnknown || v.Code != CodeUnsafeEnvironment {
			t.Errorf("%s inside a repository: %+v", name, v)
		}
		if n := len(calls(t, log)); n != 0 {
			t.Errorf("%s: ran %d times", name, n)
		}
	}
}

func TestSignInTimeoutKills(t *testing.T) {
	check, _ := newTestCheck(t)
	check.timeout = 500 * time.Millisecond
	f, log := statusFound(t, -1, "", harnessdef.Definition{})
	start := time.Now()
	v := check.Check(context.Background(), f, person())
	if v.Status != SignInUnknown || v.Code != CodeTimeout {
		t.Fatalf("got %+v", v)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("took %v", took)
	}
	if n := len(calls(t, log)); n != 1 {
		t.Errorf("ran %d times", n)
	}
}

func TestSignInDefaultTimeout(t *testing.T) {
	if (&SignInCheck{}).timeout != 0 || DefaultStatusTimeout != 5*time.Second {
		t.Fatal("the status timeout is not 5 s")
	}
}

func TestSignInVerdictCarriesNoOutput(t *testing.T) {
	check, _ := newTestCheck(t)
	f, _ := statusFound(t, 0, "token=sk-very-secret user@example.com", harnessdef.Definition{})
	v := check.Check(context.Background(), f, person())
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "example.com") {
		t.Errorf("verdict carries output: %s", b)
	}
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	for k := range fields {
		if k != "status" && k != "code" && k != "checked_at" {
			t.Errorf("verdict field %q", k)
		}
	}
}
