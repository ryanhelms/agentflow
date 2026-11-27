package harness

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// The sign-in check (lane 6.2; "Phase 6 sign-in check"). It is the only
// command phase 6 runs with the person's real home folder and credentials,
// so every rule is here, in one place:
//
//   - it runs only a status command from the agentflow overlay or the
//     person's own override (StatusFrom, StatusArgs), on a file the finder
//     already found: the harness itself ("self") or, for an adapter that is
//     itself found, its found base CLI ("base");
//   - only while sign_in_check is on; for an agent or the daemon only while
//     auto_probe is on too; a person's request always runs it;
//   - not when HOME, an XDG directory or a config_env directory is inside a
//     repository (unsafe_environment);
//   - in the empty directory <home>/harnesses/<id>/status, with only the
//     listed variables and the parent's real HOME, a 5 s timeout and the
//     process group killed;
//   - its stdout and stderr (at most 4 KiB each) are read into memory only
//     to match status_signed_out_line and then dropped: no byte is stored,
//     logged, published, returned or shown. Only the verdict, its code and
//     its time leave this file;
//   - a signed_out answer while the parent environment has a variable that
//     could sign the harness in (by name only; the value is never read)
//     becomes unknown/env_credential.

// SignInCheck runs sign-in status commands. The zero value is not usable;
// set Home.
type SignInCheck struct {
	// Home is the agentflow home: the check runs in
	// <Home>/harnesses/<id>/status with DataDir <Home>/harnesses/<id>.
	Home string
	// Environ is the parent environment; nil means os.Environ.
	Environ func() []string
	// Inside reports whether a directory is inside a repository; nil means
	// config.InsideRepository.
	Inside func(dir string) (bool, string)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// timeout replaces DefaultStatusTimeout in tests.
	timeout time.Duration

	once sync.Once
	slot chan struct{} // one check at a time per process
}

// NewSignInCheck returns the check for the agentflow home home.
func NewSignInCheck(home string) *SignInCheck {
	return &SignInCheck{Home: home}
}

func (c *SignInCheck) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Check returns the sign-in verdict for f. It never returns or keeps any
// output of the command. A verdict whose command ran (or was started and
// timed out or failed to start) has CheckedAt; one that did not run has
// none.
func (c *SignInCheck) Check(ctx context.Context, f Found, r SignInRequest) SignInStatus {
	c.once.Do(func() { c.slot = make(chan struct{}, 1) })
	unknown := func(code string) SignInStatus { return SignInStatus{Status: SignInUnknown, Code: code} }
	d := f.Definition
	if d.StatusFrom == "" || f.Path == "" {
		return unknown(CodeNoCheck)
	}
	if !r.Enabled {
		return unknown(CodeOff)
	}
	switch r.Requester {
	case RequesterPerson:
	case RequesterAgent, RequesterDaemon:
		if !r.AutoProbe {
			return unknown(CodeNeedsPerson)
		}
	default:
		return unknown(CodeNeedsPerson)
	}
	if r.Requester != RequesterPerson && r.Previous != nil && r.Previous.CheckedAt != nil {
		if age := c.now().Sub(*r.Previous.CheckedAt); age >= 0 && age < DefaultStatusInterval {
			return *r.Previous
		}
	}

	exe, ok := signInTarget(f)
	if !ok {
		if d.StatusFrom == harnessdef.StatusFromBase {
			return unknown(CodeBaseMissing)
		}
		return unknown(CodeLaunch)
	}
	if probeShim(exe.path) || probeShim(exe.target) || harnessdef.RefusedLauncher(exe.path) || harnessdef.RefusedLauncher(exe.target) {
		return unknown(CodeLaunch)
	}
	if !probeArgsOK(d.StatusArgs) {
		return unknown(CodeLaunch)
	}
	if testGuard(exe.path) != nil || testGuard(exe.target) != nil {
		return unknown(CodeTestGuard)
	}

	environ := os.Environ
	if c.Environ != nil {
		environ = c.Environ
	}
	parent := environ()
	inside := config.InsideRepository
	if c.Inside != nil {
		inside = c.Inside
	}
	configEnv := signInConfigEnv(d)
	for _, dir := range signInDirectories(parent, configEnv) {
		if in, _ := inside(dir); in {
			return unknown(CodeUnsafeEnvironment)
		}
	}

	select {
	case c.slot <- struct{}{}:
		defer func() { <-c.slot }()
	case <-ctx.Done():
		return unknown(CodeTimeout)
	}
	checked := c.now().UTC()
	ran := func(status, code string) SignInStatus {
		return SignInStatus{Status: status, Code: code, CheckedAt: &checked}
	}

	data, err := harnessDir(c.Home, f.ID, "")
	if err != nil {
		return ran(SignInUnknown, CodeLaunch)
	}
	dir := filepath.Join(data, "status")
	if err := os.RemoveAll(dir); err != nil {
		return ran(SignInUnknown, CodeLaunch)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return ran(SignInUnknown, CodeLaunch)
	}
	env, allow := signInEnv(parent, exe.dir, configEnv)
	timeout := c.timeout
	if timeout <= 0 {
		timeout = DefaultStatusTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stderr := &signInBuffer{limit: DefaultStatusOutput}
	// The test guard runs again right before the start.
	if testGuard(exe.path) != nil || testGuard(exe.target) != nil {
		return unknown(CodeTestGuard)
	}
	res, err := jsonrpc.Run(ctx, jsonrpc.LaunchSpec{
		Path:        exe.path,
		Root:        probeRoot(exe.dir, exe.target),
		Dir:         dir,
		DataDir:     data,
		Args:        append([]string{}, d.StatusArgs...),
		Env:         env,
		AllowEnv:    allow,
		Stderr:      stderr,
		StderrLimit: DefaultStatusOutput,
	}, DefaultStatusOutput)
	defer signInWipe(res.Stdout)
	defer stderr.wipe()
	if err != nil {
		return ran(SignInUnknown, CodeLaunch) // the error may name a path: never kept
	}
	if res.TimedOut {
		return ran(SignInUnknown, CodeTimeout)
	}
	signedIn, signedOut := d.StatusSignedIn, d.StatusSignedOut
	if len(signedIn) == 0 {
		signedIn = []int{0}
	}
	if len(signedOut) == 0 {
		signedOut = []int{1}
	}
	switch {
	case slices.Contains(signedIn, res.ExitCode):
		return ran(SignedIn, CodeOK)
	case slices.Contains(signedOut, res.ExitCode):
		if d.StatusSignedOutLine != "" && !signInHasLine(res.Stdout, d.StatusSignedOutLine) && !signInHasLine(stderr.bytes(), d.StatusSignedOutLine) {
			return ran(SignInUnknown, CodeExitCode)
		}
		if signInCredentialSet(parent, d.CredentialEnv) {
			return ran(SignInUnknown, CodeEnvCredential)
		}
		return ran(SignedOut, CodeSignedOut)
	}
	return ran(SignInUnknown, CodeExitCode)
}

// signInExe is the file a status command starts.
type signInExe struct {
	path   string // as found on PATH
	target string // links resolved
	dir    string // the PATH entry it was found in
}

// signInTarget picks the status command's file: the harness itself, or
// for an adapter that is itself found, its found base CLI. A base CLI must
// be an absolute path the finder found.
func signInTarget(f Found) (signInExe, bool) {
	switch f.Definition.StatusFrom {
	case harnessdef.StatusFromSelf:
		if !filepath.IsAbs(f.Path) || !filepath.IsAbs(f.Target) || !filepath.IsAbs(f.PathDir) {
			return signInExe{}, false
		}
		return signInExe{path: f.Path, target: f.Target, dir: f.PathDir}, true
	case harnessdef.StatusFromBase:
		// The finder's base file: found on PATH by the same checks as the
		// harness itself, with its resolved target and PATH entry.
		b := f.BaseFound
		if f.Definition.Kind != harnessdef.KindAdapter || b == nil ||
			!filepath.IsAbs(b.Path) || !filepath.IsAbs(b.Target) || !filepath.IsAbs(b.PathDir) {
			return signInExe{}, false
		}
		return signInExe{path: filepath.Clean(b.Path), target: b.Target, dir: b.PathDir}, true
	}
	return signInExe{}, false
}

var signInEnvNameRule = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

// signInConfigEnv is the definition's config_env, checked again here: a
// valid name, not a launch-safety name, not a credential name.
func signInConfigEnv(d harnessdef.Definition) []string {
	var out []string
	for _, n := range d.ConfigEnv {
		if !signInEnvNameRule.MatchString(n) || harnessdef.LaunchSafetyName(n) || harnessdef.CredentialSuffix(n) ||
			slices.Contains(d.CredentialEnv, n) || signInFixedName(n) || slices.Contains(out, n) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// signInFixed are the names the status profile passes from the parent.
var (
	signInUnix    = []string{"HOME", "USER", "LOGNAME", "LANG", "TZ", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"}
	signInWindows = []string{"USERPROFILE", "APPDATA", "LOCALAPPDATA", "HOMEDRIVE", "HOMEPATH",
		"SystemRoot", "SystemDrive", "COMSPEC", "PATHEXT", "WINDIR", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE"}
	// signInDirs are the directory variables checked against repositories.
	signInDirs = []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"}
)

func signInFixedName(n string) bool {
	return n == "PATH" || slices.Contains(signInUnix, n) || slices.ContainsFunc(signInWindows, func(w string) bool { return strings.EqualFold(w, n) }) || strings.HasPrefix(n, "LC_")
}

// signInDirectories lists the set directory variables: the fixed ones and
// the config_env names.
func signInDirectories(parent, configEnv []string) []string {
	var out []string
	for _, kv := range parent {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		if signInNameIn(name, signInDirs) || slices.Contains(configEnv, name) {
			out = append(out, value)
		}
	}
	return out
}

func signInNameIn(name string, list []string) bool {
	for _, n := range list {
		if n == name || (windowsOS() && strings.EqualFold(n, name)) {
			return true
		}
	}
	return false
}

// signInEnv builds the status command's environment from the parent: only
// the listed names, the config_env names and PATH (the found directory,
// then the fixed system PATH). allow lists the names jsonrpc does not allow
// on its own.
func signInEnv(parent []string, foundDir string, configEnv []string) (env, allow []string) {
	windows := windowsOS()
	seen := map[string]bool{}
	for _, kv := range parent {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" || strings.ContainsRune(value, 0) {
			continue
		}
		pass := signInNameIn(name, signInUnix) || strings.HasPrefix(name, "LC_") || slices.Contains(configEnv, name)
		if windows {
			pass = pass || signInNameIn(name, signInWindows)
		}
		if !pass || seen[strings.ToUpper(name)] {
			continue
		}
		seen[strings.ToUpper(name)] = true
		env = append(env, name+"="+value)
		if !jsonrpc.EnvAllowed(name) {
			allow = append(allow, name)
		}
	}
	env = append(env, "PATH="+foundDir+string(filepath.ListSeparator)+signInSystemPath(parent))
	slices.Sort(env)
	return env, allow
}

// signInSystemPath is the fixed system PATH jsonrpc.ScrubbedEnv uses.
func signInSystemPath(parent []string) string {
	if runtime.GOOS != "windows" {
		return "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	root := `C:\Windows`
	for _, kv := range parent {
		if name, value, ok := strings.Cut(kv, "="); ok && strings.EqualFold(name, "SystemRoot") && value != "" {
			root = value
		}
	}
	return root + `\System32;` + root
}

// signInCredentialSet reports whether the parent has a non-empty variable
// that could sign a harness in: one of names, or any name with a generic
// credential suffix. Only names are compared; values are never read beyond
// being empty or not.
func signInCredentialSet(parent, names []string) bool {
	for _, kv := range parent {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		if slices.Contains(names, name) || harnessdef.CredentialSuffix(name) {
			return true
		}
	}
	return false
}

// signInHasLine reports whether out has line as one whole trimmed line.
func signInHasLine(out []byte, line string) bool {
	want := []byte(strings.TrimSpace(line))
	for _, l := range bytes.Split(out, []byte("\n")) {
		if bytes.Equal(bytes.TrimSpace(l), want) {
			return true
		}
	}
	return false
}

func signInWipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// signInBuffer keeps the first limit bytes of the command's stderr in
// memory, for the signed-out line only.
type signInBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (s *signInBuffer) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if room := s.limit - len(s.buf); room > 0 {
		if len(b) < room {
			room = len(b)
		}
		s.buf = append(s.buf, b[:room]...)
	}
	return len(b), nil
}

func (s *signInBuffer) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf
}

func (s *signInBuffer) wipe() {
	s.mu.Lock()
	defer s.mu.Unlock()
	signInWipe(s.buf)
	s.buf = nil
}
