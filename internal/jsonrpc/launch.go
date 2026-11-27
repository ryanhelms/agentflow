package jsonrpc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// drainDelay is how long a process waits, after the child exits, for the
// connection to read the rest of the child's stdout. A grandchild that keeps
// stdout open cannot hold the Process open longer than this.
const drainDelay = 500 * time.Millisecond

// Launch starts a child that speaks JSON-RPC on stdin and stdout. It checks
// the LaunchSpec first (see its fields) and runs the resolved executable,
// never a PATH lookup. ctx bounds the child's whole life, as with
// exec.CommandContext: when it ends, the child (group) is killed.
//
// On Unix the child leads its own process group and Stop and Kill signal
// the group, so its own children end too.
func Launch(ctx context.Context, s LaunchSpec) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("jsonrpc: launch: %w", err)
	}
	exe, root, err := resolveExecutable(s.Path, s.Root)
	if err != nil {
		return nil, err
	}
	dir, err := resolveDir(s.Dir, root, s.Env, s.DataDir)
	if err != nil {
		return nil, err
	}
	if err := checkEnv(s.Env, s.AllowEnv); err != nil {
		return nil, err
	}
	limit := s.StderrLimit
	if limit <= 0 {
		limit = DefaultStderrLimit
	}
	grace := s.ShutdownGrace
	if grace <= 0 {
		grace = DefaultShutdownGrace
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("jsonrpc: launch: stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		closeAll(stdinR, stdinW)
		return nil, fmt.Errorf("jsonrpc: launch: stdout pipe: %w", err)
	}
	stderr := newStderrStream(s.Stderr, limit)
	cmd := &exec.Cmd{
		Path:   exe,
		Args:   append([]string{exe}, s.Args...),
		Dir:    dir,
		Env:    append([]string{}, s.Env...), // never nil: nil would inherit the parent's
		Stdin:  stdinR,
		Stdout: stdoutW,
		Stderr: stderr,
		// Bounds the stderr copy when a grandchild keeps stderr open.
		WaitDelay: drainDelay,
	}
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		closeAll(stdinR, stdinW, stdoutR, stdoutW)
		return nil, fmt.Errorf("jsonrpc: launch: start: %w", err)
	}
	closeAll(stdinR, stdoutW) // the child holds its own copies

	p := &process{
		cmd:    cmd,
		stdin:  stdinW,
		grace:  grace,
		stderr: stderr,
		exited: make(chan struct{}),
		code:   -1,
	}
	p.conn = NewConn(stdoutR, stdinW, s.Handler, s.Limits)
	go p.wait()
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Kill()
		case <-p.exited:
		}
	}()
	return p, nil
}

func closeAll(fs ...*os.File) {
	for _, f := range fs {
		_ = f.Close()
	}
}

// resolveExecutable returns the executable and Root with every symbolic
// link resolved, refusing anything outside Root.
func resolveExecutable(path, root string) (exe, rootReal string, err error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", "", errors.New("jsonrpc: launch: the executable path must be absolute")
	}
	if root == "" || !filepath.IsAbs(root) {
		return "", "", errors.New("jsonrpc: launch: the root must be an absolute path")
	}
	rootReal, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("jsonrpc: launch: resolve root: %w", err)
	}
	if info, err := os.Stat(rootReal); err != nil || !info.IsDir() {
		return "", "", errors.New("jsonrpc: launch: the root is not a directory")
	}
	// The unresolved path must already name something inside Root, and so
	// must the file every link leads to.
	if !within(filepath.Clean(path), filepath.Clean(root)) && !within(filepath.Clean(path), rootReal) {
		return "", "", errors.New("jsonrpc: launch: the executable is outside the root")
	}
	exe, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", fmt.Errorf("jsonrpc: launch: resolve executable: %w", err)
	}
	if !within(exe, rootReal) {
		return "", "", errors.New("jsonrpc: launch: the executable resolves outside the root")
	}
	info, err := os.Stat(exe)
	if err != nil {
		return "", "", fmt.Errorf("jsonrpc: launch: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", "", errors.New("jsonrpc: launch: the executable is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", "", errors.New("jsonrpc: launch: the file is not executable")
	}
	return exe, rootReal, nil
}

// resolveDir returns the working directory: Root by default, otherwise a
// directory inside Root or inside the child's data directory. With
// dataDir set (LaunchSpec.DataDir) that is the only data directory: it must
// be an absolute, existing directory, and nothing is derived from env.
// Without it the data directory is the child's AGENTFLOW_PLUGIN_DATA, else
// the parent of its HOME.
func resolveDir(dir, root string, env []string, dataDir string) (string, error) {
	var data string
	if dataDir != "" {
		if !filepath.IsAbs(dataDir) {
			return "", errors.New("jsonrpc: launch: the data directory must be absolute")
		}
		real, err := filepath.EvalSymlinks(dataDir)
		if err != nil {
			return "", fmt.Errorf("jsonrpc: launch: resolve data directory: %w", err)
		}
		if info, err := os.Stat(real); err != nil || !info.IsDir() {
			return "", errors.New("jsonrpc: launch: the data directory is not a directory")
		}
		data = real
	}
	if dir == "" {
		return root, nil
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("jsonrpc: launch: the working directory must be absolute")
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("jsonrpc: launch: resolve working directory: %w", err)
	}
	if info, err := os.Stat(real); err != nil || !info.IsDir() {
		return "", errors.New("jsonrpc: launch: the working directory is not a directory")
	}
	if within(real, root) {
		return real, nil
	}
	if data == "" {
		if d := envDataDir(env); d != "" {
			if dReal, err := filepath.EvalSymlinks(d); err == nil {
				data = dReal
			}
		}
	}
	if data != "" && within(real, data) {
		return real, nil
	}
	return "", errors.New("jsonrpc: launch: the working directory is outside the root and the data directory")
}

func envDataDir(env []string) string {
	var home string
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		switch {
		case strings.EqualFold(name, EnvPluginData):
			return value
		case strings.EqualFold(name, EnvHome) && filepath.IsAbs(value):
			home = filepath.Dir(value)
		}
	}
	return home
}

// within reports whether path is base or below it. Both must be clean.
func within(path, base string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

type process struct {
	cmd    *exec.Cmd
	conn   Conn
	stdin  *os.File
	grace  time.Duration
	stderr *stderrStream

	exited  chan struct{}
	code    int
	waitErr error

	stopOnce sync.Once
	stopErr  error
}

func (p *process) Conn() Conn { return p.conn }

func (p *process) PID() int { return p.cmd.Process.Pid }

func (p *process) Exited() <-chan struct{} { return p.exited }

func (p *process) ExitCode() int {
	select {
	case <-p.exited:
		return p.code
	default:
		return -1
	}
}

func (p *process) Stop() error {
	p.stopOnce.Do(func() {
		_ = p.stdin.Close()
		timer := time.NewTimer(p.grace)
		defer timer.Stop()
		select {
		case <-p.exited:
		case <-timer.C:
			_ = p.Kill()
			<-p.exited
		}
		p.stopErr = p.waitErr
	})
	return p.stopErr
}

func (p *process) Kill() error {
	select {
	case <-p.exited:
		return nil
	default:
	}
	err := killProcess(p.cmd.Process)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// wait reaps the child, lets the connection read what it left in the pipe,
// ends the stderr stream and then closes exited.
func (p *process) wait() {
	err := p.cmd.Wait()
	code := -1
	if p.cmd.ProcessState != nil {
		code = p.cmd.ProcessState.ExitCode()
	}
	timer := time.NewTimer(drainDelay)
	select {
	case <-p.conn.Done():
	case <-timer.C:
	}
	timer.Stop()
	_ = p.conn.Close()
	p.stderr.finish()
	p.code, p.waitErr = code, err
	close(p.exited)
}

// StderrTail returns the newest bytes (at most the spec's StderrLimit) the
// child wrote to stderr, unsplit and untruncated by line, for an exit or
// crash report. It returns nil for a Process that Launch did not start.
func StderrTail(p Process) []byte {
	if lp, ok := p.(*process); ok {
		return lp.stderr.tail.Bytes()
	}
	return nil
}

// tailBuffer keeps the newest limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(b)
	if n >= t.limit {
		t.buf = append(t.buf[:0], b[n-t.limit:]...)
		return n, nil
	}
	if over := len(t.buf) + n - t.limit; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	t.buf = append(t.buf, b...)
	return n, nil
}

func (t *tailBuffer) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}
