package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// Harness files under the agentflow home.
const (
	HarnessesDir = "harnesses"   // <home>/harnesses, 0700
	DataSubdir   = "data"        // <home>/harnesses/<id>/data: a probe's Dir and data directory
	StatusSubdir = "status"      // <home>/harnesses/<id>/status: the sign-in check's empty Dir
	ProbeLogFile = "probe.log"   // <home>/harnesses/<id>/probe.log: stderr of probes and version commands
	ProbeLogOld  = "probe.log.1" // the one rotated file kept
)

// HarnessDir is <home>/harnesses/<id>.
func HarnessDir(home, id string) string { return filepath.Join(home, HarnessesDir, id) }

// ProbeSpec is the probe launch profile ("Phase 6 harness launch profile")
// for a found harness started with args: Path as found, Root from
// LaunchRoot, Dir and DataDir <home>/harnesses/<id>/data (0700), Env
// HarnessEnv(os.Environ(), data, [PATH entry]), and stderr (at most 64 KiB)
// appended to probe.log, rotated at 1 MiB with one old file kept. The test
// guard is checked first. The caller closes the returned Closer once the
// child has ended. Lane 6.2's probe uses it for initialize; the version
// command uses it too.
func ProbeSpec(home string, f Found, args []string) (jsonrpc.LaunchSpec, io.Closer, error) {
	for _, p := range []string{f.Path, f.Target} {
		if err := testGuard(p); err != nil {
			return jsonrpc.LaunchSpec{}, nil, err
		}
	}
	if home == "" || !filepath.IsAbs(home) {
		return jsonrpc.LaunchSpec{}, nil, errors.New("harness: the agentflow home must be an absolute path")
	}
	dir := HarnessDir(home, f.ID)
	data := filepath.Join(dir, DataSubdir)
	if err := os.MkdirAll(data, 0o700); err != nil {
		return jsonrpc.LaunchSpec{}, nil, fmt.Errorf("harness: data directory: %w", err)
	}
	for _, d := range []string{filepath.Join(home, HarnessesDir), dir, data} {
		if err := os.Chmod(d, 0o700); err != nil { //nolint:gosec // G302: a directory needs its search bit; 0700 keeps it to the user
			return jsonrpc.LaunchSpec{}, nil, fmt.Errorf("harness: data directory: %w", err)
		}
	}
	env, err := jsonrpc.HarnessEnv(os.Environ(), data, []string{f.PathDir})
	if err != nil {
		return jsonrpc.LaunchSpec{}, nil, err
	}
	log := &rotatingLog{path: filepath.Join(dir, ProbeLogFile), old: filepath.Join(dir, ProbeLogOld), limit: DefaultProbeLogRotate}
	return jsonrpc.LaunchSpec{
		Path:        f.Path,
		Root:        LaunchRoot(f.PathDir, f.Target),
		Dir:         data,
		DataDir:     data,
		Args:        append([]string(nil), args...),
		Env:         env,
		Stderr:      log,
		StderrLimit: DefaultProbeStderr,
	}, log, nil
}

// rotatingLog appends to path and moves it to old once it passes limit.
type rotatingLog struct {
	mu    sync.Mutex
	path  string
	old   string
	limit int64
	f     *os.File
}

func (l *rotatingLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		if info, err := os.Stat(l.path); err == nil && info.Size() >= l.limit {
			_ = os.Rename(l.path, l.old)
		}
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return len(b), nil // stderr is best effort; never fail the child's copy
		}
		l.f = f
	}
	_, _ = l.f.Write(b)
	return len(b), nil
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

var versionRe = regexp.MustCompile(`v?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)`)

// maxVersionLen clamps a version read from a harness.
const maxVersionLen = 64

// ParseVersion returns the first v?X.Y.Z[-pre] in out, without the v, or "".
func ParseVersion(out []byte) string {
	m := versionRe.FindSubmatch(out)
	if m == nil {
		return ""
	}
	v := string(m[1])
	if len(v) > maxVersionLen {
		v = v[:maxVersionLen]
	}
	return v
}

// runVersion runs the definition's version command through ProbeSpec with
// DefaultVersionTimeout and the first DefaultVersionStdout bytes of stdout.
// It returns the version, or "" and VersionCodeNone. A definition with
// NoVersion, or a Windows shim, runs nothing.
func runVersion(ctx context.Context, home string, f found) (string, string, error) {
	if f.Definition.NoVersion || f.shim || f.Path == "" {
		return "", VersionCodeNone, nil
	}
	spec, closer, err := ProbeSpec(home, f.Found, versionArgs(f.Definition))
	if err != nil {
		if errors.Is(err, ErrTestGuard) {
			return "", "", err
		}
		return "", VersionCodeNone, nil
	}
	defer closer.Close()
	ctx, cancel := context.WithTimeout(ctx, DefaultVersionTimeout)
	defer cancel()
	res, err := jsonrpc.Run(ctx, spec, DefaultVersionStdout)
	if err != nil || res.TimedOut {
		return "", VersionCodeNone, nil
	}
	if v := ParseVersion(res.Stdout); v != "" {
		return v, "", nil
	}
	return "", VersionCodeNone, nil
}

// compareVersions compares two X.Y.Z[-pre] versions: -1, 0 or 1. A version
// with a pre-release is lower than the same version without one. ok is false
// when either does not parse.
func compareVersions(a, b string) (int, bool) {
	pa, prea, oka := splitVersion(a)
	pb, preb, okb := splitVersion(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range 3 {
		switch {
		case pa[i] < pb[i]:
			return -1, true
		case pa[i] > pb[i]:
			return 1, true
		}
	}
	switch {
	case prea == preb:
		return 0, true
	case prea == "":
		return 1, true
	case preb == "":
		return -1, true
	case prea < preb:
		return -1, true
	}
	return 1, true
}

func splitVersion(v string) ([3]int, string, bool) {
	var out [3]int
	core, pre, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, "", false
		}
		out[i] = n
	}
	return out, pre, true
}
