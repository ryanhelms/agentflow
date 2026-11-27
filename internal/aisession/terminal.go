package aisession

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
)

// terminal is one process agentflow started for terminal/create: no shell,
// its own process group, stdout and stderr together, kept up to the
// agent's limit and truncated from the start.
type terminal struct {
	id    string
	cmd   *exec.Cmd
	limit int
	line  string
	cwd   string

	mu        sync.Mutex
	out       []byte
	truncated bool
	done      chan struct{}
	exit      *acp.TerminalExitStatus
	released  bool
}

// terminals are a session's terminals ("Phase 7 terminals", lifetime).
type terminals struct {
	mu      sync.Mutex
	byID    map[string]*terminal
	started int
}

func newTerminals() *terminals { return &terminals{byID: map[string]*terminal{}} }

var errTooManyTerminals = errors.New("too many terminals")

// start starts a terminal. env is the terminal environment plus the
// agent's entries that passed the chain.
func (ts *terminals) start(c *Command, env []string, limit uint64) (*terminal, error) {
	ts.mu.Lock()
	open := 0
	for _, t := range ts.byID {
		select {
		case <-t.done:
		default:
			open++
		}
	}
	if open >= MaxTerminalsOpen || ts.started >= MaxTerminals {
		ts.mu.Unlock()
		return nil, errTooManyTerminals
	}
	ts.started++
	ts.mu.Unlock()

	lim := DefaultTerminalOut
	if limit > 0 {
		lim = int(min(limit, uint64(MaxTerminalOut)))
	}
	t := &terminal{id: "term_" + randomHex(8), limit: lim, line: c.Line, cwd: c.Cwd.Rel, done: make(chan struct{})}
	cmd := &exec.Cmd{
		Path: c.Resolved,
		Args: append([]string{c.Resolved}, c.Args...),
		Dir:  c.Cwd.Real,
		Env:  append([]string{}, env...),
	}
	if cmd.Dir == "" {
		cmd.Dir = c.Cwd.Abs
	}
	w := &termWriter{t: t}
	cmd.Stdout, cmd.Stderr = w, w
	setTerminalGroup(cmd)
	cmd.WaitDelay = 500 * time.Millisecond
	t.cmd = cmd
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		err := cmd.Wait()
		st := &acp.TerminalExitStatus{}
		if cmd.ProcessState != nil {
			if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				sig := ws.Signal().String()
				st.Signal = &sig
			} else if code := cmd.ProcessState.ExitCode(); code >= 0 && code <= 1<<31 {
				u := uint32(code) //nolint:gosec // G115: checked to be 0 to 2^31 above
				st.ExitCode = &u
			}
		} else if err != nil {
			sig := "unknown"
			st.Signal = &sig
		}
		t.mu.Lock()
		t.exit = st
		t.mu.Unlock()
		close(t.done)
	}()
	ts.mu.Lock()
	ts.byID[t.id] = t
	ts.mu.Unlock()
	return t, nil
}

func (ts *terminals) get(id string) (*terminal, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.byID[id]
	if !ok || t.released {
		return nil, false
	}
	return t, true
}

// owns reports whether id is a terminal agentflow started (released or not).
func (ts *terminals) owns(id string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	_, ok := ts.byID[id]
	return ok
}

func (ts *terminals) release(id string) bool {
	ts.mu.Lock()
	t, ok := ts.byID[id]
	if ok && !t.released {
		t.released = true
	}
	ts.mu.Unlock()
	if ok {
		t.kill()
	}
	return ok
}

// killAll kills every terminal's process group (session end).
func (ts *terminals) killAll() {
	ts.mu.Lock()
	list := make([]*terminal, 0, len(ts.byID))
	for _, t := range ts.byID {
		list = append(list, t)
	}
	ts.mu.Unlock()
	for _, t := range list {
		t.kill()
	}
	for _, t := range list {
		select {
		case <-t.done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (t *terminal) kill() {
	select {
	case <-t.done:
		return
	default:
	}
	killTerminalGroup(t.cmd)
}

// output returns what the agent gets: the bytes it would get from a pipe,
// truncated from the start at a character boundary.
func (t *terminal) output() acp.TerminalOutputResponse {
	t.mu.Lock()
	defer t.mu.Unlock()
	return acp.TerminalOutputResponse{Output: string(t.out), Truncated: t.truncated, ExitStatus: t.exit}
}

// shown is the output with terminal sequences removed, the last 64 KiB,
// for the transcript and the projection.
func (t *terminal) shown() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Sanitize(ClampStart(string(t.out), TranscriptTermOut), TranscriptTermOut)
}

type termWriter struct{ t *terminal }

func (w *termWriter) Write(p []byte) (int, error) {
	t := w.t
	t.mu.Lock()
	t.out = append(t.out, p...)
	if len(t.out) > t.limit {
		s := ClampStart(string(t.out), t.limit)
		t.out = []byte(s)
		t.truncated = true
	}
	t.mu.Unlock()
	return len(p), nil
}

// lookProgram resolves a terminal program: a bare name on the session
// PATH, a path with a separator against cwd. The result is absolute, a
// regular executable file.
func lookProgram(program, cwd string, path []string) (string, bool) {
	if program == "" || strings.ContainsRune(program, 0) {
		return "", false
	}
	check := func(p string) (string, bool) {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return "", false
		}
		if !isWindows() && info.Mode().Perm()&0o111 == 0 {
			return "", false
		}
		return filepath.Clean(p), true
	}
	if strings.ContainsAny(program, `/\`) {
		p := program
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		return check(p)
	}
	for _, dir := range path {
		names := []string{program}
		if isWindows() && filepath.Ext(program) == "" {
			names = []string{program + ".exe", program + ".com"}
		}
		for _, n := range names {
			if p, ok := check(filepath.Join(dir, n)); ok {
				return p, true
			}
		}
	}
	return "", false
}

// quoteLine is the POSIX-quoted program and arguments.
func quoteLine(program string, args []string) string {
	words := append([]string{program}, args...)
	for i, w := range words {
		words[i] = shQuote(w)
	}
	return strings.Join(words, " ")
}

func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		plain := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./-_", r)
		if !plain {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// waitExit waits for the terminal to exit or ctx to end.
func (t *terminal) waitExit(ctx context.Context) (acp.TerminalExitStatus, error) {
	select {
	case <-t.done:
		t.mu.Lock()
		defer t.mu.Unlock()
		return *t.exit, nil
	case <-ctx.Done():
		return acp.TerminalExitStatus{}, ctx.Err()
	}
}
