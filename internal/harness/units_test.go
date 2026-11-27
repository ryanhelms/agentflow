package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/harness/acpregistry"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

func TestParseAndCompareVersions(t *testing.T) {
	for in, want := range map[string]string{
		"gemini 0.71.1\n":           "0.71.1",
		"codex-cli v2.12.1-alpha.3": "2.12.1-alpha.3",
		"no version here":           "",
		"1.2 then 3.4.5":            "3.4.5",
	} {
		if got := ParseVersion([]byte(in)); got != want {
			t.Errorf("ParseVersion(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.2.3", "1.2.3", 0}, {"1.2.3", "1.10.0", -1}, {"2.0.0", "1.9.9", 1}, {"1.0.0-rc.1", "1.0.0", -1}, {"1.0.0", "1.0.0-rc.1", 1}} {
		if got, ok := compareVersions(c.a, c.b); !ok || got != c.want {
			t.Errorf("compare(%s, %s) = %d, %v", c.a, c.b, got, ok)
		}
	}
	if _, ok := compareVersions("1.2", "1.2.3"); ok {
		t.Error("1.2 parsed")
	}
}

func TestSignInGuidance(t *testing.T) {
	methods := []AuthMethod{
		{ID: "oauth", Name: "Log in\nwith Google", Type: ""},
		{ID: "login", Name: "Terminal", Type: "terminal", Args: []string{"login"}, Env: map[string]string{"MODE": "device", "A_FLAG": "1"}},
		{ID: "evil-arg", Type: "terminal", Args: []string{"$(id)"}},
		{ID: "evil-env", Type: "terminal", Env: map[string]string{"NODE_OPTIONS": "--require=x"}},
		{ID: "evil-value", Type: "terminal", Env: map[string]string{"MODE": "a;rm -rf ~"}},
		{ID: "lower-env", Type: "terminal", Env: map[string]string{"mode": "x"}},
		{ID: "long-value", Type: "terminal", Env: map[string]string{"MODE": strings.Repeat("a", 129)}},
		{ID: "many-args", Type: "terminal", Args: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}},
		{ID: "", Name: "no id"},
		{ID: "odd", Type: "browser"},
	}
	got := SignInGuidance("gemini", []string{"--acp"}, methods)
	if len(got) != 8 {
		t.Fatalf("got %d methods: %+v", len(got), got)
	}
	if got[0].Type != MethodAgent || got[0].Name != "Log in with Google" || got[0].Command != "" {
		t.Errorf("agent = %+v", got[0])
	}
	if got[1].Command != "A_FLAG=1 MODE=device gemini --acp login" || got[1].Unsafe {
		t.Errorf("terminal = %+v", got[1])
	}
	for _, s := range got[2:] {
		if !s.Unsafe || s.Command != "" {
			t.Errorf("%s: %+v should be unsafe", s.Method, s)
		}
	}
	many := make([]AuthMethod, 12)
	for i := range many {
		many[i] = AuthMethod{ID: "m" + strings.Repeat("x", i)}
	}
	if n := len(SignInGuidance("x", nil, many)); n != MaxSignInMethods {
		t.Errorf("methods kept = %d", n)
	}
}

func TestResolveTable(t *testing.T) {
	good := &ProbeRecord{Status: ProbeOK, Code: CodeOK, ProbedAt: time.Unix(100, 0)}
	later := time.Unix(200, 0)
	cases := []struct {
		name string
		in   ResolveInput
		st   State
		code string
	}{
		{"base only", ResolveInput{Kind: harnessdef.KindAdapter, BaseFound: true}, StateAdapterAvailable, CodeBaseOnly},
		{"native base is no help", ResolveInput{Kind: harnessdef.KindNative, BaseFound: true}, StateNotInstalled, CodeMissing},
		{"missing", ResolveInput{}, StateNotInstalled, CodeMissing},
		{"protocol version beats sign-in", ResolveInput{Found: true, Record: &ProbeRecord{Status: ProbeFailed, Code: CodeProtocolVersion}, Auth: SignInStatus{Status: SignedOut}}, StateIncompatible, CodeProtocolVersion},
		{"too old", ResolveInput{Found: true, Version: "1.0.0", MinVersion: "1.2.0", Record: good}, StateIncompatible, CodeVersionTooOld},
		{"signed out", ResolveInput{Found: true, Record: good, Auth: SignInStatus{Status: SignedOut}}, StateNeedsAuth, CodeSignedOut},
		{"initialize -32000", ResolveInput{Found: true, ProbeCode: CodeAuthRequired, Record: &ProbeRecord{Status: ProbeFailed, Code: CodeAuthRequired, ProbedAt: later, AuthRequiredAt: &later}}, StateNeedsAuth, CodeAuthRequired},
		{"session auth", ResolveInput{Found: true, Record: &ProbeRecord{Status: ProbeOK, Code: CodeOK, ProbedAt: time.Unix(100, 0), AuthRequiredAt: &later}}, StateNeedsAuth, CodeAuthRequired},
		{"auth before a good probe", ResolveInput{Found: true, ProbeCode: CodeOK, Record: &ProbeRecord{Status: ProbeOK, ProbedAt: later, AuthRequiredAt: &time.Time{}}}, StateNative, ""},
		{"adapter ok", ResolveInput{Kind: harnessdef.KindAdapter, Found: true, ProbeCode: CodeOK, Record: good}, StateAdapterInstalled, ""},
		{"version unknown", ResolveInput{Found: true, MinVersion: "1.0.0", ProbeCode: CodeOK}, StateNative, CodeVersionUnknown},
		{"not probed", ResolveInput{Found: true, ProbeCode: CodeNotProbed}, StateNative, CodeNotProbed},
	}
	for _, c := range cases {
		if st, code := Resolve(c.in); st != c.st || code != c.code {
			t.Errorf("%s: %s/%s, want %s/%s", c.name, st, code, c.st, c.code)
		}
	}
}

func TestFixInstallStrategies(t *testing.T) {
	cat, _ := acpregistry.Load()
	get := func(id string) *acpregistry.Entry { e, _ := cat.Get(id); return &e }
	cases := []struct {
		id, platform, command, text string
	}{
		{"codex-acp", "darwin-aarch64", "npm install -g @agentclientprotocol/codex-acp@2.12.1", "Codex is installed, but its ACP adapter is not."},
		{"fast-agent", "darwin-aarch64", "uv tool install fast-agent-acp==0.10.66", "fast-agent is not installed."},
		{"minion-code", "darwin-aarch64", "uv tool install minion-code==0.1.44", "is not installed."},
		{"goose", "darwin-aarch64", "", "SHA-256"},
	}
	for _, c := range cases {
		h := Harness{ID: c.id, Name: get(c.id).Name, State: StateNotInstalled, Code: CodeMissing}
		if c.id == "codex-acp" {
			h.State, h.Code, h.Base = StateAdapterAvailable, CodeBaseOnly, &Base{Name: "Codex", Command: "codex"}
		}
		f := FixFor(FixInput{Harness: h, Entry: get(c.id), Platform: c.platform})
		if f == nil || f.Command != c.command || !strings.Contains(f.Text, c.text) {
			t.Errorf("%s: %+v", c.id, f)
		}
		if f != nil && f.URL != "" && !acpregistry.HTTPS(f.URL) {
			t.Errorf("%s: url %q", c.id, f.URL)
		}
	}
	if q := quoteArg("pkg[extra]==1", false); q != "'pkg[extra]==1'" {
		t.Errorf("posix quote = %s", q)
	}
	if q := quoteArg("it's", true); q != "'it''s'" {
		t.Errorf("powershell quote = %s", q)
	}
	needs := FixFor(FixInput{Harness: Harness{ID: "claude-acp", Name: "Claude Agent", State: StateNeedsAuth, Code: CodeSignedOut}})
	if needs.Command != "claude auth login" {
		t.Errorf("needs_auth fix = %+v", needs)
	}
}

func TestProbeSpecProfile(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	bin := filepath.Join(dir, "demo")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(bin)
	t.Setenv("GITHUB_TOKEN", "secret")
	spec, closer, err := ProbeSpec(home, Found{ID: "demo", Path: real, Target: real, PathDir: filepath.Dir(real)}, []string{"--version"})
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	data := filepath.Join(home, HarnessesDir, "demo", DataSubdir)
	if spec.Dir != data || spec.DataDir != data || spec.Root != filepath.Dir(real) {
		t.Fatalf("spec = %+v", spec)
	}
	for _, kv := range spec.Env {
		if strings.HasPrefix(kv, "GITHUB_TOKEN=") {
			t.Fatal("a token reached the profile")
		}
		if strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "PATH="+filepath.Dir(real)+string(filepath.ListSeparator)) {
			t.Fatalf("PATH = %s", kv)
		}
	}
	if info, err := os.Stat(data); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("data dir: %v %v", info, err)
	}
	if _, _, err := ProbeSpec(home, Found{ID: "x", Path: "/usr/bin/true", Target: "/usr/bin/true", PathDir: "/usr/bin"}, nil); err == nil {
		t.Fatal("ProbeSpec allowed a real binary in a test")
	}
}

func TestLaunchRoot(t *testing.T) {
	base := t.TempDir()
	baseReal, _ := filepath.EvalSymlinks(base)
	bin := filepath.Join(base, "bin")
	lib := filepath.Join(base, "lib", "node_modules", "x")
	for _, d := range []string{bin, lib} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := LaunchRoot(bin, filepath.Join(baseReal, "lib", "node_modules", "x", "cli.js")); got != baseReal {
		t.Fatalf("LaunchRoot = %s, want %s", got, baseReal)
	}
	if got := LaunchRoot(bin, filepath.Join(baseReal, "bin", "agent")); got != filepath.Join(baseReal, "bin") {
		t.Fatalf("LaunchRoot same dir = %s", got)
	}
}

func TestRotatingLog(t *testing.T) {
	dir := t.TempDir()
	l := &rotatingLog{path: filepath.Join(dir, ProbeLogFile), old: filepath.Join(dir, ProbeLogOld), limit: 10}
	_, _ = l.Write([]byte("0123456789ab"))
	_ = l.Close()
	_, _ = l.Write([]byte("new"))
	_ = l.Close()
	if b, _ := os.ReadFile(filepath.Join(dir, ProbeLogOld)); string(b) != "0123456789ab" {
		t.Fatalf("old = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ProbeLogFile)); string(b) != "new" {
		t.Fatalf("log = %q", b)
	}
}
