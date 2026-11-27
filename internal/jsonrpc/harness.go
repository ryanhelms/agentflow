package jsonrpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// HarnessEnv is ScrubbedEnv(parent, data, nil) with PATH set to pathDirs
// followed by the fixed system PATH. pathDirs must be absolute.
//
// It is the phase 6 probe environment ("Phase 6 harness launch profile"):
// no token, key, AGENTFLOW_* variable or session ID of the parent; HOME and
// TMPDIR under data. pathDirs come first so a "#!/usr/bin/env node" script
// finds the node installed next to it. An entry that is empty, relative or
// contains the list separator or a NUL byte fails. The result is sorted.
func HarnessEnv(parent []string, data string, pathDirs []string) ([]string, error) {
	return harnessEnv(parent, data, pathDirs, runtime.GOOS == "windows")
}

func harnessEnv(parent []string, data string, pathDirs []string, windows bool) ([]string, error) {
	sep := ":"
	if windows {
		sep = ";"
	}
	dirs := make([]string, 0, len(pathDirs))
	for _, d := range pathDirs {
		if d == "" || strings.ContainsRune(d, 0) || strings.Contains(d, sep) || !absFor(d, windows) {
			return nil, errors.New("jsonrpc: a harness PATH directory must be an absolute path without the list separator")
		}
		dirs = append(dirs, cleanFor(d, windows))
	}
	env, err := scrubbedEnv(parent, data, nil, windows)
	if err != nil {
		return nil, err
	}
	for i, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if name == EnvPath {
			env[i] = EnvPath + "=" + strings.Join(append(dirs, value), sep)
		}
	}
	sort.Strings(env)
	return env, nil
}

// absFor is filepath.IsAbs for the given system, so the Windows rules can be
// tested anywhere.
func absFor(p string, windows bool) bool {
	if !windows {
		return strings.HasPrefix(p, "/")
	}
	if strings.HasPrefix(p, `\\`) {
		return len(p) > 2
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		(p[0] >= 'A' && p[0] <= 'Z' || p[0] >= 'a' && p[0] <= 'z')
}

func cleanFor(p string, windows bool) string {
	if windows == (runtime.GOOS == "windows") {
		return filepath.Clean(p)
	}
	return p
}

// RunResult is a finished child that does not speak JSON-RPC.
type RunResult struct {
	Stdout   []byte // at most maxStdout bytes, the first ones
	ExitCode int    // -1 when killed
	TimedOut bool
}

// Run starts s like Launch (same path, root, directory and environment
// checks, same process group) without a connection, waits for it to exit
// or ctx to end, and kills the group when ctx ends.
//
// Details:
//
//   - stdin is the null device;
//   - the first maxStdout bytes of stdout are kept (none when maxStdout is
//     0 or less) and the rest is read and dropped, so a flooding child
//     cannot block;
//   - stderr goes to s.Stderr exactly as with Launch (line by line, at most
//     StderrLimit bytes; nil discards it);
//   - when ctx ends first, the group is killed, ExitCode is -1 and TimedOut
//     is true (for a deadline or a cancel alike); the error is nil;
//   - after the child exits on its own the group is killed too, so nothing
//     it started outlives it, and its pipes are read for at most a short
//     drain delay.
//
// s.Handler, s.Limits and s.ShutdownGrace are not used. The error is
// non-nil only when the spec is refused or the child cannot start.
func Run(ctx context.Context, s LaunchSpec, maxStdout int) (RunResult, error) {
	if err := ctx.Err(); err != nil {
		return RunResult{}, fmt.Errorf("jsonrpc: run: %w", err)
	}
	exe, root, err := resolveExecutable(s.Path, s.Root)
	if err != nil {
		return RunResult{}, err
	}
	dir, err := resolveDir(s.Dir, root, s.Env, s.DataDir)
	if err != nil {
		return RunResult{}, err
	}
	if err := checkEnv(s.Env, s.AllowEnv); err != nil {
		return RunResult{}, err
	}
	limit := s.StderrLimit
	if limit <= 0 {
		limit = DefaultStderrLimit
	}

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("jsonrpc: run: stdout pipe: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		closeAll(stdoutR, stdoutW)
		return RunResult{}, fmt.Errorf("jsonrpc: run: stderr pipe: %w", err)
	}
	cmd := &exec.Cmd{
		Path:   exe,
		Args:   append([]string{exe}, s.Args...),
		Dir:    dir,
		Env:    append([]string{}, s.Env...), // never nil: nil would inherit the parent's
		Stdin:  nil,                          // the null device
		Stdout: stdoutW,                      // *os.File: Wait does not wait for a copy
		Stderr: stderrW,
	}
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		closeAll(stdoutR, stdoutW, stderrR, stderrW)
		return RunResult{}, fmt.Errorf("jsonrpc: run: start: %w", err)
	}
	closeAll(stdoutW, stderrW) // the child holds its own copies

	out := &headBuffer{limit: maxStdout}
	stderr := newStderrStream(s.Stderr, limit)
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); _, _ = io.Copy(out, stdoutR) }()
	go func() { defer readers.Done(); _, _ = io.Copy(stderr, stderrR) }()

	// The exit status is read from ProcessState; Wait's error adds nothing.
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()

	res := RunResult{ExitCode: -1}
	select {
	case <-waited:
		if cmd.ProcessState != nil {
			res.ExitCode = cmd.ProcessState.ExitCode()
		}
	case <-ctx.Done():
		res.TimedOut = true
		_ = killProcess(cmd.Process)
		<-waited
	}
	// Whatever the child started goes with it.
	_ = killProcess(cmd.Process)

	drained := make(chan struct{})
	go func() { readers.Wait(); close(drained) }()
	timer := time.NewTimer(drainDelay)
	select {
	case <-drained:
	case <-timer.C:
		// Something outside the group still holds a pipe open. Closing the
		// read ends unblocks the copies on Unix; where it may not (Windows),
		// wait one more drain delay at most and leave them behind.
		closeAll(stdoutR, stderrR)
		timer.Reset(drainDelay)
		select {
		case <-drained:
		case <-timer.C:
		}
	}
	timer.Stop()
	closeAll(stdoutR, stderrR)
	stderr.finish()
	res.Stdout = out.Bytes()
	return res, nil
}

// headBuffer keeps the first limit bytes written to it and drops the rest.
type headBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (h *headBuffer) Write(b []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if room := h.limit - len(h.buf); room > 0 {
		if len(b) < room {
			room = len(b)
		}
		h.buf = append(h.buf, b[:room]...)
	}
	return len(b), nil
}

func (h *headBuffer) Bytes() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]byte(nil), h.buf...)
}
