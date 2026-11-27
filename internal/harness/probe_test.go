package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/harness/harnesstest"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

// TestMain lets acptest.Install start this test binary as a fake agent.
func TestMain(m *testing.M) {
	jsonrpctest.RunChild()
	os.Exit(m.Run())
}

// secretParent is a parent environment full of things no harness may see.
func secretParent() []string {
	return append(os.Environ(),
		"GH_TOKEN=gh-secret", "GITHUB_TOKEN=gh-secret", "ANTHROPIC_API_KEY=sk-secret", "OPENAI_API_KEY=sk-secret",
		"AWS_SECRET_ACCESS_KEY=aws-secret", "CLAUDE_CODE_SESSION_ID=session", "AGENTFLOW_HOME=/somewhere",
		"SSH_AUTH_SOCK=/tmp/agent.sock", "HTTPS_PROXY=http://user:pass@proxy")
}

func identity(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fakeAgent installs a fake agent and returns its Found and log file.
func fakeAgent(t *testing.T, id string, s acptest.Script) (Found, string) {
	t.Helper()
	dir := t.TempDir()
	if s.Log == "" {
		s.Log = filepath.Join(t.TempDir(), "agent.log")
	}
	args := acptest.Install(t, dir, "fake-"+id, s)
	path := filepath.Join(dir, "fake-"+id)
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return Found{
		ID:         id,
		Definition: harnessdef.Definition{Name: id, Kind: harnessdef.KindNative, Command: "fake-" + id, Args: append(args, "--acp")},
		Sources:    []string{SourcePersonal},
		Path:       path,
		Target:     target,
		PathDir:    dir,
		Identity:   identity(id + path),
	}, s.Log
}

func newTestProber(t *testing.T) (*ACPProber, string) {
	t.Helper()
	home := t.TempDir()
	p := NewProber(home, "9.9.9", nil)
	p.Environ = secretParent
	p.StopGrace = 3 * time.Second // a race-enabled child sleeps 1 s at exit
	return p, home
}

func readAgentLog(t *testing.T, path string) []acptest.LogEntry {
	t.Helper()
	entries, err := acptest.ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestProbeOK(t *testing.T) {
	ok := acptest.OK()
	ok.AuthMethods = append(ok.AuthMethods,
		acp.AuthMethod{ID: "env", Name: "With a mode", Type: "terminal", Args: []string{"login", "--device"}, Env: map[string]string{"LOGIN_MODE": "device"}},
		acp.AuthMethod{ID: "evil", Name: "Evil", Type: "terminal", Env: map[string]string{"NODE_OPTIONS": "--require=x"}},
		acp.AuthMethod{ID: "evil2", Name: "Evil 2", Type: "terminal", Args: []string{"$(rm -rf ~)"}},
		acp.AuthMethod{ID: "future", Name: "Future", Type: "env_var"},
	)
	f, log := fakeAgent(t, "demo", acptest.Script{Initialize: &ok, Ask: "fs/read_text_file", Stderr: "secret stderr line\n"})
	p, home := newTestProber(t)
	rec := p.Probe(context.Background(), f, 10*time.Second)
	if rec.Status != ProbeOK || rec.Code != CodeOK || rec.ID != "demo" || rec.Identity != f.Identity || rec.ProbedAt.IsZero() {
		t.Fatalf("record %+v", rec)
	}
	if rec.ACP == nil || rec.ACP.ProtocolVersion != 1 || rec.ACP.AgentName != "fake-acp" || !rec.ACP.LoadSession || !rec.ACP.Image || !rec.ACP.MCPHTTP {
		t.Errorf("acp %+v", rec.ACP)
	}
	want := []SignIn{
		{Method: "oauth", Type: "agent", Name: "Log in", Description: "Sign in with your account"},
		{Method: "login", Type: "terminal", Name: "Log in from a terminal", Command: "fake-demo --acp login"},
		{Method: "env", Type: "terminal", Name: "With a mode", Command: "LOGIN_MODE=device fake-demo --acp login --device"},
		{Method: "evil", Type: "terminal", Name: "Evil", Unsafe: true},
		{Method: "evil2", Type: "terminal", Name: "Evil 2", Unsafe: true},
	}
	if runtime.GOOS == "windows" {
		t.Skip("the command names differ on Windows")
	}
	got := rec.SignIn
	for i := range got { // the Windows child-mode args are not in want
		got[i].Command = strings.ReplaceAll(got[i].Command, "  ", " ")
	}
	if !slices.Equal(got, want) {
		t.Errorf("sign in\n got %+v\nwant %+v", got, want)
	}

	entries := readAgentLog(t, log)
	var methods []string
	for _, e := range entries {
		if e.Method != "" {
			methods = append(methods, e.Method)
			want := `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false,"auth":{"terminal":true}},"clientInfo":{"name":"agentflow","title":"agentflow","version":"9.9.9"}}`
			if string(e.Params) != want {
				t.Errorf("params %s", e.Params)
			}
		}
		if e.Asked != "" && e.Code != jsonrpc.CodeMethodNotFound {
			t.Errorf("the agent's request got %d, want -32601", e.Code)
		}
	}
	if !slices.Equal(methods, []string{acp.MethodInitialize}) {
		t.Errorf("methods sent: %v", methods)
	}
	start := entries[0]
	if !slices.Equal(start.Args, []string{"--acp"}) {
		t.Errorf("args %v", start.Args)
	}
	for _, name := range start.Env {
		if !jsonrpc.EnvAllowed(name) && !slices.Contains(harnesstest.ShellVariables, name) {
			t.Errorf("the agent saw %s", name)
		}
	}
	data, _ := filepath.EvalSymlinks(filepath.Join(home, "harnesses", "demo", "data"))
	if got, _ := filepath.EvalSymlinks(start.Dir); got != data {
		t.Errorf("dir %s, want %s", start.Dir, data)
	}
	logged, err := os.ReadFile(filepath.Join(home, "harnesses", "demo", "probe.log"))
	if err != nil || !strings.Contains(string(logged), "secret stderr line") {
		t.Errorf("probe.log = %q, %v", logged, err)
	}
	b, _ := json.Marshal(rec)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), f.PathDir) {
		t.Errorf("record carries stderr or a path: %s", b)
	}
	if info, err := os.Stat(filepath.Join(home, "harnesses", "demo", "probe.log")); err == nil && runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("probe.log mode %v", info.Mode())
	}
}

func TestProbeEnvironmentIsScrubbed(t *testing.T) {
	f, log := fakeAgent(t, "scrub", acptest.Script{})
	p, _ := newTestProber(t)
	if rec := p.Probe(context.Background(), f, 10*time.Second); rec.Status != ProbeOK {
		t.Fatalf("record %+v", rec)
	}
	entries := readAgentLog(t, log)
	for _, secret := range []string{"GH_TOKEN", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY",
		"CLAUDE_CODE_SESSION_ID", "AGENTFLOW_HOME", "SSH_AUTH_SOCK", "HTTPS_PROXY"} {
		if slices.Contains(entries[0].Env, secret) {
			t.Errorf("%s reached the agent", secret)
		}
	}
}

func TestProbeOutcomes(t *testing.T) {
	v2 := acptest.OK()
	v2.ProtocolVersion = 2
	cases := []struct {
		name   string
		script acptest.Script
		status string
		code   string
	}{
		{"timeout", acptest.Script{Delay: time.Hour}, ProbeFailed, CodeTimeout},
		{"exited", acptest.Script{ExitBefore: true}, ProbeFailed, CodeExited},
		{"garbage", acptest.Script{Garbage: true}, ProbeFailed, CodeProtocol},
		{"v2", acptest.Script{Initialize: &v2}, ProbeFailed, CodeProtocolVersion},
		{"auth", acptest.Script{InitializeError: &jsonrpc.Error{Code: acp.CodeAuthRequired, Message: "Authentication required"}}, ProbeFailed, CodeAuthRequired},
		{"error", acptest.Script{InitializeError: &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "boom"}}, ProbeFailed, CodeProtocol},
		// -32002 is "resource not found" in ACP, not jsonrpc's "rate limited".
		{"not found", acptest.Script{InitializeError: &jsonrpc.Error{Code: acp.CodeResourceNotFound, Message: "nope"}}, ProbeFailed, CodeProtocol},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := fakeAgent(t, "case", c.script)
			p, _ := newTestProber(t)
			start := time.Now()
			rec := p.Probe(context.Background(), f, time.Second)
			if rec.Status != c.status || rec.Code != c.code {
				t.Fatalf("got %s/%s, want %s/%s", rec.Status, rec.Code, c.status, c.code)
			}
			if took := time.Since(start); took > 8*time.Second {
				t.Errorf("probe took %v", took)
			}
			switch c.name {
			case "v2":
				if rec.ACP == nil || rec.ACP.ProtocolVersion != 2 {
					t.Errorf("acp %+v", rec.ACP)
				}
			case "auth":
				if rec.AuthRequiredAt == nil {
					t.Error("auth_required_at not set")
				}
			}
		})
	}
}

func TestProbeKillsTheProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are Unix only")
	}
	for _, s := range []acptest.Script{{Spawn: true}, {Spawn: true, Delay: time.Hour}} {
		f, log := fakeAgent(t, "group", s)
		p, _ := newTestProber(t)
		p.Probe(context.Background(), f, time.Second)
		var pid int
		for _, e := range readAgentLog(t, log) {
			if e.Spawned != 0 {
				pid = e.Spawned
			}
		}
		if pid == 0 {
			t.Fatal("the fake did not spawn")
		}
		deadline := time.Now().Add(5 * time.Second)
		for processAlive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("grandchild %d still runs", pid)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestProbeCooldown(t *testing.T) {
	f, log := fakeAgent(t, "cool", acptest.Script{})
	p, _ := newTestProber(t)
	first := p.Probe(context.Background(), f, 10*time.Second)
	second := p.Probe(context.Background(), f, 10*time.Second)
	if first.Status != ProbeOK || !second.ProbedAt.Equal(first.ProbedAt) {
		t.Fatalf("first %+v second %+v", first, second)
	}
	starts := 0
	for _, e := range readAgentLog(t, log) {
		if e.Dir != "" {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("started %d times", starts)
	}

	// A record in the store inside the cooldown is used as well.
	g, glog := fakeAgent(t, "stored", acptest.Script{})
	store := NewFileStore(t.TempDir())
	cached := ProbeRecord{ID: g.ID, Identity: g.Identity, Status: ProbeOK, Code: CodeOK, ProbedAt: time.Now().UTC()}
	if err := store.Put(cached); err != nil {
		t.Fatal(err)
	}
	p.Store = store
	if rec := p.Probe(context.Background(), g, 10*time.Second); rec.Status != ProbeOK {
		t.Fatalf("record %+v", rec)
	}
	if entries := readAgentLog(t, glog); len(entries) != 0 {
		t.Errorf("the stored identity was started: %+v", entries)
	}
}

func TestProbeConcurrencyLimit(t *testing.T) {
	p, _ := newTestProber(t)
	var founds []Found
	for _, id := range []string{"one", "two", "three"} {
		f, _ := fakeAgent(t, id, acptest.Script{Delay: time.Hour})
		founds = append(founds, f)
	}
	start := time.Now()
	var wg sync.WaitGroup
	for _, f := range founds {
		wg.Add(1)
		go func(f Found) {
			defer wg.Done()
			if rec := p.Probe(context.Background(), f, time.Second); rec.Code != CodeTimeout {
				t.Errorf("%s: %+v", f.ID, rec)
			}
		}(f)
	}
	wg.Wait()
	if took := time.Since(start); took < 2*time.Second {
		t.Errorf("three 1 s probes took %v: more than two ran at once", took)
	}
}

func TestProbeRefusals(t *testing.T) {
	p, home := newTestProber(t)
	f, log := fakeAgent(t, "refuse", acptest.Script{})

	outside := f
	outside.Path, outside.Target, outside.PathDir = "/bin/sh", "/bin/sh", "/bin"
	if runtime.GOOS == "windows" {
		outside.Path, outside.Target, outside.PathDir = `C:\Windows\System32\where.exe`, `C:\Windows\System32\where.exe`, `C:\Windows\System32`
	}
	outside.Identity = identity("outside")
	if rec := p.Probe(context.Background(), outside, time.Second); rec.Status != ProbeRefused || rec.Code != CodeTestGuard {
		t.Errorf("outside the temporary directory: %+v", rec)
	}
	shim := f
	shim.Path = strings.TrimSuffix(f.Path, ".exe") + ".cmd"
	shim.Identity = identity("shim")
	if rec := p.Probe(context.Background(), shim, time.Second); rec.Status != ProbeRefused || rec.Code != CodeShim {
		t.Errorf("shim: %+v", rec)
	}
	bad := f
	bad.Definition.Args = []string{"--acp", "$(id)"}
	bad.Identity = identity("bad")
	if rec := p.Probe(context.Background(), bad, time.Second); rec.Status != ProbeFailed || rec.Code != CodeLaunch {
		t.Errorf("bad args: %+v", rec)
	}
	if entries := readAgentLog(t, log); len(entries) != 0 {
		t.Errorf("a refused probe started the agent: %+v", entries)
	}
	if _, err := os.Stat(filepath.Join(home, "harnesses", "refuse", "probe.log")); err == nil {
		t.Error("a refused probe wrote probe.log")
	}
}

func TestProbeGuardRefusesARealBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh")
	}
	p, _ := newTestProber(t)
	f := Found{ID: "sh", Definition: harnessdef.Definition{Command: "true"}, Path: "/usr/bin/true", Target: "/usr/bin/true", PathDir: "/usr/bin", Identity: identity("true")}
	if real, err := filepath.EvalSymlinks(f.Path); err == nil {
		f.Target = real
	}
	if rec := p.Probe(context.Background(), f, time.Second); rec.Status != ProbeRefused || rec.Code != CodeTestGuard {
		t.Errorf("record %+v", rec)
	}
}

func TestProbeLogRotates(t *testing.T) {
	f, _ := fakeAgent(t, "rotate", acptest.Script{Stderr: "line\n"})
	p, home := newTestProber(t)
	dir := filepath.Join(home, "harnesses", "rotate")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.log"), make([]byte, DefaultProbeLogRotate), 0o600); err != nil {
		t.Fatal(err)
	}
	p.Probe(context.Background(), f, 10*time.Second)
	old, err := os.Stat(filepath.Join(dir, "probe.log.1"))
	if err != nil || old.Size() != DefaultProbeLogRotate {
		t.Fatalf("probe.log.1: %v %v", old, err)
	}
	cur, err := os.Stat(filepath.Join(dir, "probe.log"))
	if err != nil || cur.Size() >= DefaultProbeLogRotate {
		t.Errorf("probe.log: %v %v", cur, err)
	}
}

func TestACPText(t *testing.T) {
	cases := map[string]string{
		"plain":                 "plain",
		"two\nlines":            "two",
		"  spaced  ":            "spaced",
		"bidi\u202Eevil\u2066x": "bidievilx",
		"ctl\x07bell\x1b[31m":   "ctlbell[31m",
		"bad\xffutf8":           "badutf8",
		strings.Repeat("é", 40): strings.Repeat("é", 32),
		"line\u2028sep":         "line",
	}
	for in, want := range cases {
		if got := acpText(in, 64); got != want {
			t.Errorf("acpText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSignInGuidanceLimits(t *testing.T) {
	var methods []acp.AuthMethod
	for i := 0; i < 12; i++ {
		methods = append(methods, acp.AuthMethod{ID: "m" + string(rune('a'+i)), Name: "Method\nwith a second line", Description: strings.Repeat("d", 400)})
	}
	got := signInGuidance(methods, "agent", nil)
	if len(got) != MaxSignInMethods {
		t.Fatalf("%d methods", len(got))
	}
	if got[0].Name != "Method" || len(got[0].Description) != MaxDescriptionLen {
		t.Errorf("%+v", got[0])
	}
	unsafe := []acp.AuthMethod{
		{ID: "a", Name: "a", Type: "terminal", Args: make([]string, 9)},
		{ID: "b", Name: "b", Type: "terminal", Env: map[string]string{"lower": "x"}},
		{ID: "c", Name: "c", Type: "terminal", Env: map[string]string{"MODE": strings.Repeat("x", 129)}},
		{ID: "d", Name: "d", Type: "terminal", Env: map[string]string{"LD_PRELOAD": "x"}},
		{ID: "e", Name: "e", Type: "terminal", Env: map[string]string{"https_proxy": "x"}},
		{ID: "f", Name: "f", Type: "terminal", Env: map[string]string{"MODE": "a b"}},
	}
	for _, s := range signInGuidance(unsafe, "agent", nil) {
		if !s.Unsafe || s.Command != "" {
			t.Errorf("%+v is not unsafe", s)
		}
	}
	if s := signInGuidance([]acp.AuthMethod{{ID: "q", Name: "q", Type: "terminal", Args: []string{"a@b,c"}}}, "agent", nil); s[0].Command != "agent 'a@b,c'" {
		t.Errorf("quoting: %+v", s)
	}
}

func TestProbeRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix paths")
	}
	cases := [][3]string{
		{"/opt/homebrew/bin", "/opt/homebrew/lib/node_modules/x/cli.js", "/opt/homebrew"},
		{"/usr/local/bin", "/usr/local/bin/tool", "/usr/local/bin"},
		{"/a/b", "/c/d", "/"},
	}
	for _, c := range cases {
		if got := probeRoot(c[0], c[1]); got != c[2] {
			t.Errorf("probeRoot(%s, %s) = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestHarnessTestPATHStillWorks(t *testing.T) {
	// A probe test may also set its PATH through harnesstest; the fake is
	// found from its own temporary directory.
	f, _ := fakeAgent(t, "pathed", acptest.Script{})
	harnesstest.PATH(t, f.PathDir)
	p, _ := newTestProber(t)
	if rec := p.Probe(context.Background(), f, 10*time.Second); rec.Status != ProbeOK {
		t.Errorf("record %+v", rec)
	}
}
