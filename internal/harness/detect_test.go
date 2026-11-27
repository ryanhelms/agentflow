package harness

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/harness/harnesstest"
)

// TestGuardRefusesRealPATH is the ADR's guard test: detection with the
// system PATH fails in a test binary, before any home is read.
func TestGuardRefusesRealPATH(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("PATH", "/usr/bin"+string(filepath.ListSeparator)+"/bin")
	_, err := New(Options{}).Detect(context.Background(), DetectOptions{Requester: RequesterPerson, Probe: true})
	if !errors.Is(err, ErrTestGuard) {
		t.Fatalf("err = %v, want ErrTestGuard", err)
	}
	if !strings.Contains(err.Error(), "/usr/bin") {
		t.Fatalf("err = %v, want it to name the entry", err)
	}
}

// fakeProber answers each probe with the record its function gives.
type fakeProber struct {
	mu    sync.Mutex
	calls []string
	fn    func(Found) ProbeRecord
}

func (p *fakeProber) Probe(_ context.Context, f Found, _ time.Duration) ProbeRecord {
	p.mu.Lock()
	p.calls = append(p.calls, f.ID)
	p.mu.Unlock()
	if p.fn == nil {
		return ProbeRecord{Status: ProbeOK, Code: CodeOK, ACP: &ACPInfo{ProtocolVersion: 1, AgentName: "fake\u202e agent"}, DurationMS: 5}
	}
	return p.fn(f)
}

func (p *fakeProber) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

type fakeSignIn struct {
	mu    sync.Mutex
	calls int
	v     SignInStatus
}

func (s *fakeSignIn) CheckSignIn(context.Context, string, Found) SignInStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.v
}

type env struct {
	home   string
	bin    string
	prober *fakeProber
	signIn *fakeSignIn
	store  *MemoryStore
	now    time.Time
}

func (e *env) detector() Detector {
	return New(Options{Home: e.home, Prober: e.prober, SignIn: e.signIn, Store: e.store, Now: func() time.Time { return e.now }})
}

func (e *env) detect(t *testing.T, o DetectOptions) Report {
	t.Helper()
	r, err := e.detector().Detect(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newEnv(t *testing.T, s harnesstest.Settings) *env {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are /bin/sh scripts")
	}
	bin := t.TempDir()
	home := harnesstest.PATH(t, bin)
	harnesstest.WriteSettings(t, home, s)
	return &env{home: home, bin: bin, prober: &fakeProber{}, signIn: &fakeSignIn{v: SignInStatus{Status: SignedIn, Code: CodeOK}},
		store: NewMemoryStore(), now: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)}
}

func byID(t *testing.T, r Report, id string) Harness {
	t.Helper()
	for _, h := range r.Harnesses {
		if h.ID == id {
			return h
		}
	}
	t.Fatalf("%s not in the report: %+v", id, r.Harnesses)
	return Harness{}
}

func demo(defs map[string]harnessdef.Definition) map[string]harnessdef.Definition { return defs }

func TestDetectFindsWithoutStartingAnything(t *testing.T) {
	e := newEnv(t, harnesstest.Settings{Overrides: demo(map[string]harnessdef.Definition{
		"demo": {Name: "Demo", Command: "demo-acp", Args: []string{"acp"}},
	})})
	log := harnesstest.StatusCLI(t, e.bin, "demo-acp", 0, "demo-acp 1.2.3")
	r := e.detect(t, DetectOptions{Requester: RequesterAgent})
	h := byID(t, r, "demo")
	if h.State != StateNative || h.Code != CodeNotProbed || h.Probe.Status != ProbeNone || h.Probe.Code != CodeNotProbed {
		t.Fatalf("demo = %+v", h)
	}
	if h.Fix == nil || h.Fix.Command != ProbeCommand {
		t.Fatalf("fix = %+v", h.Fix)
	}
	if !slices.Equal(h.Sources, []string{SourcePersonal}) || h.Command != "demo-acp" || h.Path == "" {
		t.Fatalf("demo = %+v", h)
	}
	if calls, _ := log.Calls(); len(calls) != 0 || e.prober.count() != 0 {
		t.Fatalf("detection started something: %d version runs, %d probes", len(calls), e.prober.count())
	}
	if r.AutoProbe || r.ProbeRefused != "" || r.Registry.Agents != 46 {
		t.Fatalf("report = %+v", r)
	}
	for _, h := range r.Harnesses {
		if h.State == StateNotInstalled {
			t.Fatalf("not_installed listed without All: %s", h.ID)
		}
	}
	all := e.detect(t, DetectOptions{Requester: RequesterAgent, All: true})
	if g := byID(t, all, "gemini"); g.State != StateNotInstalled || g.Code != CodeMissing || g.Fix == nil ||
		g.Fix.Command != "npm install -g @google/gemini-cli@0.71.1" {
		t.Fatalf("gemini = %+v %+v", g, g.Fix)
	}
	if last := all.Harnesses[len(all.Harnesses)-1]; last.State != StateNotInstalled {
		t.Fatalf("not_installed is not sorted last: %s", last.ID)
	}
	if q := byID(t, all, "fast-agent"); !q.Quarantined || q.QuarantineReason == "" || !slices.Contains(q.Notes, NoteQuarantined) ||
		!slices.Contains(q.Notes, NoteRegistryEnvIgnored) {
		t.Fatalf("fast-agent = %+v", q)
	}
}

func TestDetectAgentProbeRefusedWhenAutoProbeOff(t *testing.T) {
	e := newEnv(t, harnesstest.Settings{Overrides: demo(map[string]harnessdef.Definition{"demo": {Command: "demo-acp"}})})
	log := harnesstest.StatusCLI(t, e.bin, "demo-acp", 0, "demo-acp 1.2.3")
	r := e.detect(t, DetectOptions{Requester: RequesterAgent, Probe: true})
	h := byID(t, r, "demo")
	if r.ProbeRefused != CodeNeedsPerson || h.Probe.Status != ProbeRefused || h.Probe.Code != CodeNeedsPerson {
		t.Fatalf("report %q, demo %+v", r.ProbeRefused, h.Probe)
	}
	if h.Fix == nil || !strings.Contains(h.Fix.Text, "Automatic probes are off") {
		t.Fatalf("fix = %+v", h.Fix)
	}
	if calls, _ := log.Calls(); len(calls) != 0 || e.prober.count() != 0 {
		t.Fatal("a refused probe started something")
	}
	// The daemon asks for nothing while auto_probe is off.
	e.detect(t, DetectOptions{Requester: RequesterDaemon, Probe: false})
	if e.prober.count() != 0 {
		t.Fatal("the daemon probed")
	}
}

func TestDetectPersonProbeReadsVersionAndCaches(t *testing.T) {
	e := newEnv(t, harnesstest.Settings{Overrides: demo(map[string]harnessdef.Definition{"demo": {Command: "demo-acp", Args: []string{"acp"}}})})
	log := harnesstest.StatusCLI(t, e.bin, "demo-acp", 0, "demo-acp version v1.2.3-beta.1 (build 7)")
	r := e.detect(t, DetectOptions{Requester: RequesterPerson, Probe: true})
	h := byID(t, r, "demo")
	if h.State != StateNative || h.Code != "" || h.Probe.Status != ProbeOK || h.Version != "1.2.3-beta.1" || h.Fix != nil {
		t.Fatalf("demo = %+v (fix %+v)", h, h.Fix)
	}
	if h.ACP == nil || h.ACP.AgentName != "fake agent" {
		t.Fatalf("acp = %+v, want sanitized", h.ACP)
	}
	calls, _ := log.Calls()
	if len(calls) != 1 || !slices.Equal(calls[0].Args, []string{"--version"}) {
		t.Fatalf("version runs = %+v", calls)
	}
	wantDir, _ := filepath.EvalSymlinks(filepath.Join(e.home, HarnessesDir, "demo", DataSubdir))
	if calls[0].Dir != wantDir {
		t.Fatalf("version command ran in %s, want %s", calls[0].Dir, wantDir)
	}
	for _, n := range calls[0].Env {
		if n == "AGENTFLOW_HOME" || strings.HasSuffix(n, "_TOKEN") {
			t.Fatalf("%s reached the version command", n)
		}
	}
	// Inside the cooldown a second probe gets the cached result.
	e.now = e.now.Add(10 * time.Second)
	r = e.detect(t, DetectOptions{Requester: RequesterPerson, Probe: true})
	if h := byID(t, r, "demo"); h.Probe.Status != ProbeCached || h.Probe.Code != CodeOK || h.Version != "1.2.3-beta.1" {
		t.Fatalf("cached demo = %+v", h.Probe)
	}
	if e.prober.count() != 1 {
		t.Fatalf("probes = %d, want 1 inside the cooldown", e.prober.count())
	}
	// A read without a probe shows the cached record.
	if h := byID(t, e.detect(t, DetectOptions{Requester: RequesterAgent}), "demo"); h.Probe.Status != ProbeCached {
		t.Fatalf("read = %+v", h.Probe)
	}
	// The daemon does not probe an identity it already has.
	e.now = e.now.Add(time.Minute)
	e.detect(t, DetectOptions{Requester: RequesterDaemon, Probe: true})
	if e.prober.count() != 1 {
		t.Fatal("the daemon probed an unchanged identity")
	}
	// A changed binary is a new identity.
	harnesstest.StatusCLI(t, e.bin, "demo-acp", 0, "demo-acp 2.0.0 and more")
	r = e.detect(t, DetectOptions{Requester: RequesterPerson, Probe: true})
	if h := byID(t, r, "demo"); h.Version != "2.0.0" || h.Probe.Status != ProbeOK || e.prober.count() != 2 {
		t.Fatalf("new identity = %+v, probes %d", h, e.prober.count())
	}
}

func TestDetectStates(t *testing.T) {
	e := newEnv(t, harnesstest.Settings{AutoProbe: true, Overrides: demo(map[string]harnessdef.Definition{
		"v2":      {Command: "v2-acp"},
		"garbage": {Command: "garbage-acp"},
		"auth":    {Command: "auth-acp"},
		"slow":    {Command: "slow-acp"},
		"old":     {Command: "old-acp", MinVersion: "2.0.0"},
		"adapter": {Name: "Demo adapter", Kind: harnessdef.KindAdapter, Command: "missing-acp", BaseName: "Demo CLI", BaseCommands: []string{"demo-cli"}},
	})})
	for _, n := range []string{"v2-acp", "garbage-acp", "auth-acp", "slow-acp", "old-acp", "demo-cli"} {
		harnesstest.StatusCLI(t, e.bin, n, 0, n+" 1.0.0")
	}
	e.prober.fn = func(f Found) ProbeRecord {
		switch f.ID {
		case "v2":
			return ProbeRecord{Status: ProbeFailed, Code: CodeProtocolVersion, ACP: &ACPInfo{ProtocolVersion: 2}}
		case "garbage":
			return ProbeRecord{Status: ProbeFailed, Code: CodeProtocol}
		case "auth":
			return ProbeRecord{Status: ProbeFailed, Code: CodeAuthRequired}
		case "slow":
			return ProbeRecord{Status: ProbeFailed, Code: CodeTimeout}
		}
		return ProbeRecord{Status: ProbeOK, Code: CodeOK}
	}
	r := e.detect(t, DetectOptions{Requester: RequesterAgent, Probe: true})
	for id, want := range map[string][2]string{
		"v2":      {string(StateIncompatible), CodeProtocolVersion},
		"garbage": {string(StateIncompatible), CodeProtocol},
		"auth":    {string(StateNeedsAuth), CodeAuthRequired},
		"slow":    {string(StateNative), CodeTimeout},
		"old":     {string(StateIncompatible), CodeVersionTooOld},
		"adapter": {string(StateAdapterAvailable), CodeBaseOnly},
	} {
		h := byID(t, r, id)
		if string(h.State) != want[0] || h.Code != want[1] || h.Fix == nil {
			t.Errorf("%s = %s/%s (fix %+v), want %s/%s", id, h.State, h.Code, h.Fix, want[0], want[1])
		}
	}
	if fix := byID(t, r, "v2").Fix; !strings.Contains(fix.Text, "ACP version 2") {
		t.Errorf("v2 fix = %+v", fix)
	}
	if fix := byID(t, r, "slow").Fix; fix.Command != TimeoutCommand {
		t.Errorf("slow fix = %+v", fix)
	}
	if fix := byID(t, r, "adapter").Fix; fix.Text != "Demo CLI is installed, but `missing-acp` is not on your PATH." {
		t.Errorf("adapter fix = %+v", fix)
	}
	if a := byID(t, r, "adapter"); a.Base == nil || a.Base.Command != "demo-cli" || a.Path != "" {
		t.Errorf("adapter = %+v", a)
	}
}

func TestDetectSignInCheck(t *testing.T) {
	defs := demo(map[string]harnessdef.Definition{"demo": {
		Kind: harnessdef.KindAdapter, Command: "demo-acp", BaseName: "Demo CLI", BaseCommands: []string{"demo-cli"},
		StatusFrom: harnessdef.StatusFromBase, StatusArgs: []string{"--status=out"},
	}})
	e := newEnv(t, harnesstest.Settings{SignInCheck: true, Overrides: defs})
	harnesstest.StatusCLI(t, e.bin, "demo-acp", 0, "demo-acp 1.0.0")
	harnesstest.StatusCLI(t, e.bin, "demo-cli", 1, "")
	e.signIn.v = SignInStatus{Status: SignedOut, Code: CodeSignedOut}

	// An agent with auto_probe off: no probe, no sign-in check.
	r := e.detect(t, DetectOptions{Requester: RequesterAgent, Probe: true})
	if h := byID(t, r, "demo"); h.Auth.Code != CodeNeedsPerson || e.signIn.calls != 0 {
		t.Fatalf("agent: auth %+v, checks %d", h.Auth, e.signIn.calls)
	}
	// A person's probe runs it.
	r = e.detect(t, DetectOptions{Requester: RequesterPerson, Probe: true})
	h := byID(t, r, "demo")
	if e.signIn.calls != 1 || h.State != StateNeedsAuth || h.Code != CodeSignedOut || h.Auth.CheckedAt == nil {
		t.Fatalf("person: %+v, checks %d", h, e.signIn.calls)
	}
	if h.Fix == nil || !strings.HasPrefix(h.Fix.Text, "Sign in to") {
		t.Fatalf("fix = %+v", h.Fix)
	}
	// With sign_in_check off nobody runs it.
	harnesstest.WriteSettings(t, e.home, harnesstest.Settings{AutoProbe: true, Overrides: defs})
	e.now = e.now.Add(time.Hour)
	harnesstest.StatusCLI(t, e.bin, "demo-acp", 0, "demo-acp 1.0.1") // new identity
	r = e.detect(t, DetectOptions{Requester: RequesterPerson, Probe: true})
	if h := byID(t, r, "demo"); h.Auth.Code != CodeOff || e.signIn.calls != 1 {
		t.Fatalf("off: auth %+v, checks %d", h.Auth, e.signIn.calls)
	}
}

func TestDetectSkipsUnsafePATHEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are /bin/sh scripts")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	repoBin := filepath.Join(repo, "bin")
	nodeBin := filepath.Join(t.TempDir(), "node_modules", ".bin")
	open := filepath.Join(t.TempDir(), "open")
	for _, d := range []string{repoBin, nodeBin, open} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	good := t.TempDir()
	home := harnesstest.PATH(t, repoBin, nodeBin, open, good)
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	harnesstest.WriteSettings(t, home, harnesstest.Settings{Overrides: demo(map[string]harnessdef.Definition{
		"in-repo": {Command: "repo-acp"}, "in-node": {Command: "node-acp"}, "in-open": {Command: "open-acp"},
		"writable": {Command: "writable-acp"},
	})})
	for dir, name := range map[string]string{repoBin: "repo-acp", nodeBin: "node-acp", open: "open-acp", good: "writable-acp"} {
		harnesstest.StatusCLI(t, dir, name, 0, name+" 1.0.0")
	}
	if err := os.Chmod(filepath.Join(good, "writable-acp"), 0o777); err != nil {
		t.Fatal(err)
	}
	d := New(Options{Home: home, WorkDirs: func() []string { return []string{repo} }})
	r, err := d.Detect(context.Background(), DetectOptions{Requester: RequesterPerson, All: true})
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, s := range r.SkippedPath {
		reasons[filepath.Base(s.Dir)] = s.Reason
	}
	for name, want := range map[string]string{"bin": SkipInsideRepository, ".bin": SkipNodeModules, "open": SkipUnsafePermissions, "writable-acp": SkipUnsafePermissions} {
		if reasons[name] != want {
			t.Errorf("%s: reason %q, want %q (all %+v)", name, reasons[name], want, r.SkippedPath)
		}
	}
	for _, id := range []string{"in-repo", "in-node", "in-open", "writable"} {
		if h := byID(t, r, id); h.State != StateNotInstalled {
			t.Errorf("%s = %s, want not_installed", id, h.State)
		}
	}
}

func TestDetectUnsafeHomeIgnoresPersonalLayer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes")
	}
	e := newEnv(t, harnesstest.Settings{AutoProbe: true, Overrides: demo(map[string]harnessdef.Definition{"demo": {Command: "demo-acp"}})})
	harnesstest.StatusCLI(t, e.bin, "demo-acp", 0, "demo-acp 1.0.0")
	if err := os.Chmod(e.home, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.home, 0o700) })
	r := e.detect(t, DetectOptions{Requester: RequesterAgent, Probe: true, All: true})
	if r.AutoProbe || r.ProbeRefused != CodeNeedsPerson {
		t.Fatalf("an unsafe home kept its toggles: %+v", r)
	}
	found := false
	for _, p := range r.Problems {
		found = found || p.Code == ProblemHomeUnsafe
	}
	if !found {
		t.Fatalf("problems = %+v", r.Problems)
	}
	for _, h := range r.Harnesses {
		if h.ID == "demo" {
			t.Fatal("a personal override from an unsafe home was used")
		}
	}
}

func TestDetectFilterAndMarkAuthRequired(t *testing.T) {
	e := newEnv(t, harnesstest.Settings{Overrides: demo(map[string]harnessdef.Definition{
		"one": {Command: "one-acp"}, "two": {Command: "two-acp"},
	})})
	for _, n := range []string{"one-acp", "two-acp"} {
		harnesstest.StatusCLI(t, e.bin, n, 0, n+" 1.0.0")
	}
	r := e.detect(t, DetectOptions{Requester: RequesterPerson, Probe: true, IDs: []string{"one"}})
	if len(r.Harnesses) != 1 || r.Harnesses[0].ID != "one" {
		t.Fatalf("harnesses = %+v", r.Harnesses)
	}
	// A session records auth_required after the good probe.
	rec := e.store.records
	var id, identity string
	for _, v := range rec {
		id, identity = v.ID, v.Identity
	}
	if err := e.store.MarkAuthRequired(id, identity, e.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if h := byID(t, e.detect(t, DetectOptions{Requester: RequesterAgent}), "one"); h.State != StateNeedsAuth || h.Code != CodeAuthRequired {
		t.Fatalf("one = %s/%s", h.State, h.Code)
	}
}

func TestDetectSignInOffWithoutAProbe(t *testing.T) {
	defs := map[string]harnessdef.Definition{
		"found":   {Command: "found-acp", StatusFrom: harnessdef.StatusFromSelf, StatusArgs: []string{"status"}},
		"missing": {Command: "missing-acp", StatusFrom: harnessdef.StatusFromSelf},
		"plain":   {Command: "plain-acp"},
	}
	e := newEnv(t, harnesstest.Settings{Overrides: defs})
	e.signIn = nil
	harnesstest.StatusCLI(t, e.bin, "found-acp", 0, "found-acp 1.0.0")
	harnesstest.StatusCLI(t, e.bin, "plain-acp", 0, "plain-acp 1.0.0")
	d := New(Options{Home: e.home})
	r, err := d.Detect(context.Background(), DetectOptions{Requester: RequesterAgent, All: true})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"found": CodeOff, "missing": CodeOff, "plain": CodeNoCheck} {
		if h := byID(t, r, id); h.Auth.Status != SignInUnknown || h.Auth.Code != want {
			t.Errorf("%s auth = %+v, want unknown/%s", id, h.Auth, want)
		}
	}
}
