package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// The initialize probe (lane 6.2): a Proxy over jsonrpc.Conn with
// Request-Reply and a timeout ("Phase 6 harness launch profile"). A probe
// starts the found file through jsonrpc.Launch with HarnessEnv, sends the
// fixed initialize request, answers any request from the agent with
// -32601, and stops the process: close stdin, a 2 s grace, then the
// process group is killed.

// ACPProber is the production Prober.
type ACPProber struct {
	// Home is the agentflow home; a probe's data directory is
	// <Home>/harnesses/<id>/data and its stderr goes to
	// <Home>/harnesses/<id>/probe.log.
	Home string
	// Version is agentflow's version, sent as clientInfo.version.
	Version string
	// Store, when set, gives cached results inside the cooldown.
	Store Store
	// Environ is the parent environment; nil means os.Environ. Only what
	// HarnessEnv lets through reaches the harness.
	Environ func() []string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// MaxProbes and Cooldown take DefaultMaxProbes and DefaultProbeCooldown
	// when zero; StopGrace takes DefaultStopGrace.
	MaxProbes int
	Cooldown  time.Duration
	StopGrace time.Duration

	once     sync.Once
	slots    chan struct{}
	mu       sync.Mutex
	recent   map[string]ProbeRecord
	inflight map[string]*probeCall
}

type probeCall struct {
	done chan struct{}
	rec  ProbeRecord
}

var _ Prober = (*ACPProber)(nil)

// NewProber returns a Prober for the agentflow home home.
func NewProber(home, version string, store Store) *ACPProber {
	return &ACPProber{Home: home, Version: version, Store: store}
}

func (p *ACPProber) init() {
	p.once.Do(func() {
		n := p.MaxProbes
		if n <= 0 {
			n = DefaultMaxProbes
		}
		p.slots = make(chan struct{}, n)
		p.recent = map[string]ProbeRecord{}
		p.inflight = map[string]*probeCall{}
	})
}

func (p *ACPProber) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *ACPProber) cooldown() time.Duration {
	if p.Cooldown > 0 {
		return p.Cooldown
	}
	return DefaultProbeCooldown
}

// Probe runs one initialize probe of f. timeout is clamped to
// MinProbeTimeout..MaxProbeTimeout (zero means DefaultProbeTimeout). The
// same identity is not probed again within the cooldown: the earlier
// result is returned as it was (its ProbedAt says when). A second Probe of
// an identity that is being probed waits for the first. At most MaxProbes
// run at once in this process; a Probe that cannot get a slot before ctx
// ends returns status none.
//
// The record has ID, Identity, Status, Code, ACP, SignIn, ProbedAt and
// DurationMS; the version, the sign-in verdict and storing it are the
// Detector's.
func (p *ACPProber) Probe(ctx context.Context, f Found, timeout time.Duration) ProbeRecord {
	p.init()
	key := f.ID + "\x00" + f.Identity
	now := p.now()
	p.mu.Lock()
	if r, ok := p.recent[key]; ok && now.Sub(r.ProbedAt) < p.cooldown() {
		p.mu.Unlock()
		return r
	}
	if c, ok := p.inflight[key]; ok {
		p.mu.Unlock()
		select {
		case <-c.done:
			return c.rec
		case <-ctx.Done():
			return p.record(f, ProbeNone, CodeNotProbed, now)
		}
	}
	c := &probeCall{done: make(chan struct{})}
	p.inflight[key] = c
	p.mu.Unlock()

	rec := p.probeOnce(ctx, f, timeout, now)

	p.mu.Lock()
	if rec.Status != ProbeNone {
		p.recent[key] = rec
	}
	for k, r := range p.recent { // keep the map small
		if now.Sub(r.ProbedAt) >= p.cooldown() {
			delete(p.recent, k)
		}
	}
	delete(p.inflight, key)
	p.mu.Unlock()
	c.rec = rec
	close(c.done)
	return rec
}

func (p *ACPProber) record(f Found, status, code string, at time.Time) ProbeRecord {
	return ProbeRecord{ID: f.ID, Identity: f.Identity, Status: status, Code: code, ProbedAt: at.UTC()}
}

func (p *ACPProber) probeOnce(ctx context.Context, f Found, timeout time.Duration, now time.Time) ProbeRecord {
	if p.Store != nil {
		if r, ok, err := p.Store.Get(f.ID, f.Identity); err == nil && ok && r.Status != ProbeNone && now.Sub(r.ProbedAt) < p.cooldown() {
			return r
		}
	}
	if code, status := probeRefusal(f); code != "" {
		return p.record(f, status, code, now)
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return p.record(f, ProbeNone, CodeNotProbed, now)
	}
	start := p.now()
	rec := p.run(ctx, f, probeTimeout(timeout))
	rec.ProbedAt = start.UTC()
	rec.DurationMS = p.now().Sub(start).Milliseconds()
	return rec
}

func probeTimeout(t time.Duration) time.Duration {
	switch {
	case t <= 0:
		return DefaultProbeTimeout
	case t < MinProbeTimeout:
		return MinProbeTimeout
	case t > MaxProbeTimeout:
		return MaxProbeTimeout
	}
	return t
}

// probeRefusal returns the code and status of a Found that must not be
// started, or "". The finder already refuses these; the probe checks again
// because it is the last step before a process starts.
func probeRefusal(f Found) (code, status string) {
	if f.Path == "" || f.Target == "" || f.PathDir == "" || !filepath.IsAbs(f.Path) || !filepath.IsAbs(f.Target) || !filepath.IsAbs(f.PathDir) {
		return CodeLaunch, ProbeFailed
	}
	if !cacheIDRule.MatchString(f.ID) || !cacheIdentityRule.MatchString(f.Identity) {
		return CodeLaunch, ProbeFailed
	}
	if probeShim(f.Path) || probeShim(f.Target) {
		return CodeShim, ProbeRefused
	}
	if testGuard(f.Path) != nil || testGuard(f.Target) != nil {
		return CodeTestGuard, ProbeRefused
	}
	if harnessdef.RefusedLauncher(f.Path) || harnessdef.RefusedLauncher(f.Target) || !probeArgsOK(f.Definition.Args) {
		return CodeLaunch, ProbeFailed
	}
	return "", ""
}

// probeShim reports a Windows command script, which cmd.exe would re-parse.
func probeShim(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".cmd" || ext == ".bat"
}

func probeArgsOK(args []string) bool {
	if len(args) > harnessdef.MaxArgs {
		return false
	}
	for _, a := range args {
		if !harnessdef.ValidArg(a) {
			return false
		}
	}
	return true
}

// probeRoot is the deepest directory that contains both a and b ("Phase 6
// harness launch profile", Root). Both are cleaned absolute paths.
func probeRoot(a, b string) string {
	a, b = filepath.Clean(a), filepath.Clean(b)
	vol := filepath.VolumeName(a)
	if !strings.EqualFold(vol, filepath.VolumeName(b)) {
		return ""
	}
	as := strings.Split(strings.TrimPrefix(a[len(vol):], string(filepath.Separator)), string(filepath.Separator))
	bs := strings.Split(strings.TrimPrefix(b[len(vol):], string(filepath.Separator)), string(filepath.Separator))
	n := 0
	for n < len(as) && n < len(bs) && as[n] == bs[n] && as[n] != "" {
		n++
	}
	return vol + string(filepath.Separator) + filepath.Join(as[:n]...)
}

// harnessDir is <home>/harnesses/<id>, created 0700.
func harnessDir(home, id, sub string) (string, error) {
	if home == "" || !filepath.IsAbs(home) || !cacheIDRule.MatchString(id) {
		return "", errors.New("harness: invalid home or id")
	}
	dir := filepath.Join(home, "harnesses", id)
	if sub != "" {
		dir = filepath.Join(dir, sub)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if !windowsOS() {
		for d := dir; d != filepath.Join(home, "harnesses") && strings.HasPrefix(d, filepath.Join(home, "harnesses")); d = filepath.Dir(d) {
			_ = os.Chmod(d, 0o700)
		}
		_ = os.Chmod(filepath.Join(home, "harnesses"), 0o700)
	}
	return dir, nil
}

func (p *ACPProber) run(ctx context.Context, f Found, timeout time.Duration) ProbeRecord {
	rec := ProbeRecord{ID: f.ID, Identity: f.Identity}
	fail := func(code string) ProbeRecord {
		rec.Status, rec.Code = ProbeFailed, code
		return rec
	}
	data, err := harnessDir(p.Home, f.ID, "data")
	if err != nil {
		return fail(CodeLaunch)
	}
	environ := os.Environ
	if p.Environ != nil {
		environ = p.Environ
	}
	env, err := jsonrpc.HarnessEnv(environ(), data, []string{f.PathDir})
	if err != nil {
		return fail(CodeLaunch)
	}
	root := probeRoot(f.PathDir, f.Target)
	if root == "" {
		return fail(CodeLaunch)
	}
	log := openProbeLog(p.Home, f.ID)
	if log != nil {
		defer log.Close()
		fmt.Fprintf(log, "--- probe %s\n", p.now().UTC().Format(time.RFC3339))
	}
	var stderr io.Writer
	if log != nil {
		stderr = log
	}
	grace := p.StopGrace
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// The test guard runs again right before the start.
	if testGuard(f.Path) != nil || testGuard(f.Target) != nil {
		rec.Status, rec.Code = ProbeRefused, CodeTestGuard
		return rec
	}
	proc, err := jsonrpc.Launch(ctx, jsonrpc.LaunchSpec{
		Path:          f.Path,
		Root:          root,
		Dir:           data,
		Args:          append([]string{}, f.Definition.Args...),
		Env:           env,
		Stderr:        stderr,
		StderrLimit:   DefaultProbeStderr,
		Limits:        jsonrpc.Limits{MaxFrame: jsonrpc.DefaultMaxFrame, CallTimeout: timeout},
		ShutdownGrace: grace,
		Handler:       nil, // every request from the agent: -32601
	})
	if err != nil {
		return fail(CodeLaunch) // the error may name a path: never kept
	}
	pid := proc.PID()
	defer func() {
		_ = proc.Stop()
		probeKillGroup(pid)
	}()

	resp, err := acp.Initialize(ctx, proc.Conn(), p.request())
	if err == nil {
		if resp.ProtocolVersion != acp.ProtocolVersion {
			rec.Status, rec.Code = ProbeFailed, CodeProtocolVersion
			rec.ACP = &ACPInfo{ProtocolVersion: resp.ProtocolVersion}
			return rec
		}
		rec.Status, rec.Code = ProbeOK, CodeOK
		rec.ACP = acpInfo(resp)
		rec.SignIn = signInGuidance(resp.AuthMethods, filepath.Base(f.Path), f.Definition.Args)
		return rec
	}
	var rerr *jsonrpc.Error
	switch {
	case errors.As(err, &rerr) && rerr.Code == acp.CodeAuthRequired:
		// Read by ACP meaning: the agent needs a sign-in (state row 6).
		rec.Status, rec.Code = ProbeFailed, CodeAuthRequired
		at := p.now().UTC()
		rec.AuthRequiredAt = &at
		return rec
	case errors.As(err, &rerr):
		return fail(CodeProtocol)
	case errors.Is(err, acp.ErrProtocol), errors.Is(proc.Conn().Err(), jsonrpc.ErrProtocol), errors.Is(proc.Conn().Err(), jsonrpc.ErrFrameTooLarge):
		return fail(CodeProtocol)
	case ctx.Err() != nil:
		return fail(CodeTimeout)
	}
	return fail(CodeExited)
}

// request is the probe's fixed initialize request.
func (p *ACPProber) request() acp.InitializeRequest {
	v := p.Version
	if v == "" {
		v = "dev"
	}
	return acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersion,
		ClientCapabilities: acp.ClientCapabilities{
			FS:       acp.FileSystemCapabilities{ReadTextFile: false, WriteTextFile: false},
			Terminal: false,
			Auth:     acp.AuthCapabilities{Terminal: true},
		},
		ClientInfo: &acp.Implementation{Name: "agentflow", Title: "agentflow", Version: v},
	}
}

// openProbeLog opens <home>/harnesses/<id>/probe.log for appending, mode
// 0600, after rotating it at DefaultProbeLogRotate (one old file,
// probe.log.1). It returns nil when the log cannot be opened: stderr is
// then dropped, never sent anywhere else.
func openProbeLog(home, id string) *os.File {
	dir, err := harnessDir(home, id, "")
	if err != nil {
		return nil
	}
	path := filepath.Join(dir, "probe.log")
	if info, err := os.Stat(path); err == nil && info.Size() >= DefaultProbeLogRotate {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	if !windowsOS() {
		_ = f.Chmod(0o600)
	}
	return f
}

// acpInfo is the sanitized initialize answer.
func acpInfo(r acp.InitializeResponse) *ACPInfo {
	info := &ACPInfo{
		ProtocolVersion: r.ProtocolVersion,
		LoadSession:     r.AgentCapabilities.LoadSession,
		Image:           r.AgentCapabilities.PromptCapabilities.Image,
		Audio:           r.AgentCapabilities.PromptCapabilities.Audio,
		EmbeddedContext: r.AgentCapabilities.PromptCapabilities.EmbeddedContext,
		MCPHTTP:         r.AgentCapabilities.MCPCapabilities.HTTP,
		MCPSSE:          r.AgentCapabilities.MCPCapabilities.SSE,
		Logout:          r.AgentCapabilities.Auth.Logout != nil,
	}
	if r.AgentInfo != nil {
		info.AgentName = acpText(r.AgentInfo.Name, MaxNameLen)
		info.AgentTitle = acpText(r.AgentInfo.Title, MaxNameLen)
		info.AgentVersion = acpText(r.AgentInfo.Version, MaxNameLen)
	}
	return info
}

// Sign-in method types (SignIn.Type).
const (
	signInAgent    = "agent"
	signInTerminal = "terminal"
)

// signInGuidance turns the advertised auth methods into sign-in guidance
// ("Phase 6 fix text", Sign-in guidance): sanitized text, at most
// MaxSignInMethods, and for a terminal method the command to run in the
// person's own terminal, omitted (Unsafe) when any part fails the rules.
// command is the found command's file name, never its path.
func signInGuidance(methods []acp.AuthMethod, command string, defArgs []string) []SignIn {
	var out []SignIn
	for _, m := range methods {
		if len(out) == MaxSignInMethods {
			break
		}
		s := SignIn{Method: acpText(m.ID, MaxIDLen), Name: acpText(m.Name, MaxNameLen)}
		if s.Method == "" || s.Name == "" {
			continue
		}
		switch m.Type {
		case "", signInAgent:
			s.Type = signInAgent
			s.Description = acpText(m.Description, MaxDescriptionLen)
		case signInTerminal:
			s.Type = signInTerminal
			s.Description = acpText(m.Description, MaxDescriptionLen)
			if cmd, ok := signInCommand(command, defArgs, m.Args, m.Env); ok {
				s.Command = cmd
			} else {
				s.Unsafe = true
			}
		default:
			continue // a method type agentflow does not know
		}
		out = append(out, s)
	}
	return out
}

// signInCommand builds "NAME=value command args... methodArgs..." for a
// terminal method, or reports it unsafe.
func signInCommand(command string, defArgs, args []string, env map[string]string) (string, bool) {
	name := strings.TrimSuffix(command, ".exe")
	if !harnessdef.ValidCommandName(name) || harnessdef.RefusedLauncher(name) {
		return "", false
	}
	if len(args) > MaxSignInArgs || !probeArgsOK(defArgs) {
		return "", false
	}
	for _, a := range args {
		if !harnessdef.ValidArg(a) {
			return "", false
		}
	}
	var prefix []string
	for k, v := range env {
		if !signInEnvName(k) || harnessdef.LaunchSafetyName(k) || len(v) > MaxSignInEnvValue || !harnessdef.ValidArg(v) {
			return "", false
		}
		prefix = append(prefix, k+"="+shellWord(v))
	}
	sort.Strings(prefix)
	words := append([]string{}, prefix...)
	words = append(words, name)
	for _, a := range defArgs {
		words = append(words, shellWord(a))
	}
	for _, a := range args {
		words = append(words, shellWord(a))
	}
	return strings.Join(words, " "), true
}

func signInEnvName(s string) bool {
	if s == "" || len(s) > harnessdef.MaxEnvNameLen {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// signInCommandSafe re-checks a stored command: one line of shell-safe
// words, so a cache edited by hand cannot put anything else on the page.
func signInCommandSafe(cmd string) bool {
	if cmd == "" || len(cmd) > 2048 {
		return false
	}
	for _, r := range cmd {
		if !(r == ' ' || r == '\'' || r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._=:/@+,-", r))) {
			return false
		}
	}
	return true
}

// shellWord quotes s for a POSIX shell when it has anything beyond
// [A-Za-z0-9._/:=-]. s already passed the arg rule, so it has no quote.
func shellWord(s string) string {
	for _, r := range s {
		if !(r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._/:=-", r))) {
			return "'" + s + "'"
		}
	}
	return s
}

// acpText is the Content Filter for agent-supplied text: valid UTF-8, the
// first line only, control and bidirectional characters removed, spaces
// trimmed, at most max bytes (cut on a character boundary).
func acpText(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	if i := strings.IndexAny(s, "\r\n  \u0085"); i >= 0 {
		s = s[:i]
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || acpBidi(r) {
			continue
		}
		b.WriteRune(r)
	}
	s = strings.TrimSpace(b.String())
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut])
}

func acpBidi(r rune) bool {
	switch {
	case r == 0x061C, r == 0x200E, r == 0x200F:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

func windowsOS() bool { return runtime.GOOS == "windows" }
