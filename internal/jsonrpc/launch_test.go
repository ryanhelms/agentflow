package jsonrpc_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

func launch(t *testing.T, s jsonrpc.LaunchSpec) jsonrpc.Process {
	t.Helper()
	p, err := jsonrpc.Launch(context.Background(), s)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Kill()
		<-p.Exited()
	})
	return p
}

func waitExited(t *testing.T, p jsonrpc.Process) {
	t.Helper()
	select {
	case <-p.Exited():
	case <-time.After(10 * time.Second):
		t.Fatal("child did not exit")
	}
}

// copyChild copies the test binary into a fresh root and returns both.
func copyChild(t *testing.T) (exe, root string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root = t.TempDir()
	exe = filepath.Join(root, "bin", "child")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(exe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return exe, root
}

func TestLaunchServesAndStopsGracefully(t *testing.T) {
	s := jsonrpctest.ChildSpec(t, "serve")
	p := launch(t, s)
	if p.PID() <= 0 {
		t.Fatalf("pid = %d", p.PID())
	}
	var got map[string]string
	if err := p.Conn().Call(context.Background(), "echo", map[string]string{"a": "b"}, &got); err != nil || got["a"] != "b" {
		t.Fatalf("echo = %v, %v", got, err)
	}
	start := time.Now()
	if err := p.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("Stop waited for the grace period although the child exits on EOF")
	}
	if p.ExitCode() != 0 {
		t.Fatalf("exit code = %d", p.ExitCode())
	}
	if err := p.Stop(); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}

func TestLaunchChildCallsParent(t *testing.T) {
	s := jsonrpctest.ChildSpec(t, "serve")
	s.Handler = jsonrpc.HandlerFunc(func(_ context.Context, r *jsonrpc.Request) (any, error) {
		return "parent answered " + r.Method, nil
	})
	p := launch(t, s)
	var got string
	err := p.Conn().Call(context.Background(), "call", map[string]any{"method": "subscribe"}, &got)
	if err != nil || got != "parent answered subscribe" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestLaunchChildNotifiesParent(t *testing.T) {
	notes := make(chan string, 1)
	s := jsonrpctest.ChildSpec(t, "serve")
	s.Handler = jsonrpc.HandlerFunc(func(_ context.Context, r *jsonrpc.Request) (any, error) {
		if r.Notification() {
			notes <- r.Method + " " + string(r.Params)
		}
		return nil, nil
	})
	p := launch(t, s)
	if err := p.Conn().Call(context.Background(), "notify", []int{1}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-notes:
		if n != "note [1]" {
			t.Fatalf("notification = %q", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notification")
	}
}

func TestLaunchEnvironmentIsScrubbed(t *testing.T) {
	for _, name := range secrets {
		t.Setenv(name, "leaked-"+name)
	}
	s := jsonrpctest.ChildSpec(t, "serve")
	data := ""
	for _, kv := range s.Env {
		if v, ok := strings.CutPrefix(kv, jsonrpc.EnvPluginData+"="); ok {
			data = v
		}
	}
	p := launch(t, s)
	var env []string
	if err := p.Conn().Call(context.Background(), "env", nil, &env); err != nil {
		t.Fatal(err)
	}
	m := envMap(t, env)
	for _, name := range secrets {
		if _, ok := m[name]; ok {
			t.Errorf("%s reached the child", name)
		}
	}
	for _, kv := range env {
		if strings.Contains(kv, "leaked-") {
			t.Errorf("parent value reached the child: %s", kv)
		}
		name, _, _ := strings.Cut(kv, "=")
		if !jsonrpc.EnvAllowed(name) {
			t.Errorf("child has %s, which EnvAllowed refuses", name)
		}
	}
	if runtime.GOOS != "windows" {
		if m["PATH"] != "/usr/bin:/bin:/usr/sbin:/sbin" {
			t.Errorf("PATH = %q", m["PATH"])
		}
		for _, name := range []string{"HOME", "TMPDIR"} {
			if !strings.HasPrefix(m[name], data+string(filepath.Separator)) {
				t.Errorf("%s = %q, want under %s", name, m[name], data)
			}
		}
	}
}

func TestLaunchEmptyEnvDoesNotInherit(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "leaked")
	s := jsonrpctest.ChildSpec(t, "serve")
	s.Env = nil
	p := launch(t, s)
	var env []string
	if err := p.Conn().Call(context.Background(), "env", nil, &env); err != nil {
		t.Fatal(err)
	}
	if len(env) != 0 {
		t.Fatalf("nil Env gave the child %v", env)
	}
}

func TestLaunchRefuses(t *testing.T) {
	exe, root := copyChild(t)
	outside := t.TempDir()
	notExec := filepath.Join(root, "bin", "script")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(root, "bin", "escape")
	outsideExe, _ := copyChild(t)
	if err := os.Symlink(outsideExe, escape); err != nil {
		t.Fatal(err)
	}
	escapeDir := filepath.Join(root, "linked")
	if err := os.Symlink(filepath.Dir(outsideExe), escapeDir); err != nil {
		t.Fatal(err)
	}
	good := jsonrpctest.ChildSpec(t, "serve")

	cases := map[string]func(s *jsonrpc.LaunchSpec){
		"relative path":      func(s *jsonrpc.LaunchSpec) { s.Path = "child" },
		"bare name for PATH": func(s *jsonrpc.LaunchSpec) { s.Path = "sh" },
		"relative root":      func(s *jsonrpc.LaunchSpec) { s.Root = "plugins" },
		"outside the root":   func(s *jsonrpc.LaunchSpec) { s.Path = outsideExe },
		"system program":     func(s *jsonrpc.LaunchSpec) { s.Path = "/bin/sh" },
		"dot-dot escape":     func(s *jsonrpc.LaunchSpec) { s.Path = filepath.Join(root, "..", filepath.Base(outside), "x") },
		"dot-dot to outside exe": func(s *jsonrpc.LaunchSpec) {
			s.Path = filepath.Join(root, "bin", "..", "..", relTo(t, filepath.Dir(root), outsideExe))
		},
		"symlink to outside":      func(s *jsonrpc.LaunchSpec) { s.Path = escape },
		"symlinked directory":     func(s *jsonrpc.LaunchSpec) { s.Path = filepath.Join(escapeDir, "child") },
		"directory":               func(s *jsonrpc.LaunchSpec) { s.Path = filepath.Join(root, "bin") },
		"missing file":            func(s *jsonrpc.LaunchSpec) { s.Path = filepath.Join(root, "bin", "nope") },
		"not executable":          func(s *jsonrpc.LaunchSpec) { s.Path = notExec },
		"token in env":            func(s *jsonrpc.LaunchSpec) { s.Env = append(s.Env, "GITHUB_TOKEN=x") },
		"daemon variable in env":  func(s *jsonrpc.LaunchSpec) { s.Env = append(s.Env, "AGENTFLOW_HOME=/x") },
		"env entry without value": func(s *jsonrpc.LaunchSpec) { s.Env = append(s.Env, "ghp_rawtoken") },
		"dir outside":             func(s *jsonrpc.LaunchSpec) { s.Dir = outside },
		"relative dir":            func(s *jsonrpc.LaunchSpec) { s.Dir = "bin" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && name == "not executable" {
				t.Skip("no executable bit on Windows")
			}
			s := good
			s.Env = append([]string(nil), good.Env...)
			s.Path, s.Root = exe, root
			mutate(&s)
			p, err := jsonrpc.Launch(context.Background(), s)
			if err == nil {
				_ = p.Kill()
				t.Fatal("launched")
			}
			if strings.Contains(err.Error(), "ghp_") {
				t.Fatalf("error echoes an env entry: %v", err)
			}
		})
	}

	t.Run("control: copied child inside the root starts", func(t *testing.T) {
		s := good
		s.Path, s.Root = exe, root
		s.Dir = filepath.Join(root, "bin")
		p := launch(t, s)
		if err := p.Conn().Call(context.Background(), "pid", nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("control: symlink inside the root to a file inside it starts", func(t *testing.T) {
		link := filepath.Join(root, "link")
		if err := os.Symlink(exe, link); err != nil {
			t.Fatal(err)
		}
		s := good
		s.Path, s.Root = link, root
		p := launch(t, s)
		if err := p.Conn().Call(context.Background(), "pid", nil, nil); err != nil {
			t.Fatal(err)
		}
	})
}

func relTo(t *testing.T, base, target string) string {
	t.Helper()
	rel, err := filepath.Rel(base, target)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestLaunchCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := jsonrpc.Launch(ctx, jsonrpctest.ChildSpec(t, "serve")); err == nil {
		t.Fatal("launched with a canceled context")
	}
}

func TestLaunchContextEndKillsChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := jsonrpc.Launch(ctx, jsonrpctest.ChildSpec(t, "serve", "ignore-eof"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	waitExited(t, p)
	if p.ExitCode() != -1 {
		t.Fatalf("exit code = %d, want -1 (killed)", p.ExitCode())
	}
}

func TestStopKillsChildThatIgnoresEOF(t *testing.T) {
	s := jsonrpctest.ChildSpec(t, "serve", "ignore-eof")
	s.ShutdownGrace = 200 * time.Millisecond
	p := launch(t, s)
	start := time.Now()
	err := p.Stop()
	if err == nil {
		t.Fatal("Stop of a killed child returned no exit error")
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("Stop returned after %v, before the grace period", d)
	}
	if p.ExitCode() != -1 {
		t.Fatalf("exit code = %d, want -1", p.ExitCode())
	}
}

func TestChildThatNeverAnswers(t *testing.T) {
	s := jsonrpctest.ChildSpec(t, "serve")
	s.Limits.CallTimeout = 100 * time.Millisecond
	p := launch(t, s)
	err := p.Conn().Call(context.Background(), "hang", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call = %v, want deadline exceeded", err)
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	waitExited(t, p)
}

func TestChildFloodingStdoutClosesConnection(t *testing.T) {
	s := jsonrpctest.ChildSpec(t, "flood")
	s.Limits.MaxFrame = 4096
	p := launch(t, s)
	select {
	case <-p.Conn().Done():
	case <-time.After(10 * time.Second):
		t.Fatal("flood did not close the connection")
	}
	if err := p.Conn().Err(); !errors.Is(err, jsonrpc.ErrFrameTooLarge) {
		t.Fatalf("Err = %v", err)
	}
	_ = p.Kill()
	waitExited(t, p)
}

func TestChildSendingGarbageClosesConnection(t *testing.T) {
	p := launch(t, jsonrpctest.ChildSpec(t, "garbage"))
	select {
	case <-p.Conn().Done():
	case <-time.After(10 * time.Second):
		t.Fatal("garbage did not close the connection")
	}
	if err := p.Conn().Err(); !errors.Is(err, jsonrpc.ErrProtocol) {
		t.Fatalf("Err = %v", err)
	}
}

// syncBuffer is a bytes.Buffer safe for the launcher's goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Len()
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func stderrChild(t *testing.T, limit int) (jsonrpc.Process, *syncBuffer) {
	t.Helper()
	var stderr syncBuffer
	s := jsonrpctest.ChildSpec(t, "serve")
	s.Stderr = &stderr
	s.StderrLimit = limit
	return launch(t, s), &stderr
}

func writeStderr(t *testing.T, p jsonrpc.Process, text string, n int) {
	t.Helper()
	if err := p.Conn().Call(context.Background(), "stderr", map[string]any{"text": text, "n": n}, nil); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStderrArrivesWhileChildRuns(t *testing.T) {
	p, stderr := stderrChild(t, 0)
	writeStderr(t, p, "first line\npartial", 0)
	waitFor(t, "the first line", func() bool { return stderr.String() == "first line\n" })
	select {
	case <-p.Exited():
		t.Fatal("child exited")
	default:
	}
	// A partial line waits for its newline, or for the exit.
	writeStderr(t, p, " done\nlast without newline", 0)
	waitFor(t, "the second line", func() bool { return stderr.String() == "first line\npartial done\n" })
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); got != "first line\npartial done\nlast without newline\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestStderrLinesAreCapped(t *testing.T) {
	p, stderr := stderrChild(t, 0)
	writeStderr(t, p, strings.Repeat("a", 10000)+"\nnext\n", 0)
	want := strings.Repeat("a", 4096) + " [line truncated]\nnext\n"
	waitFor(t, "both lines", func() bool { return stderr.String() == want })
}

func TestStderrBudgetIsExhausted(t *testing.T) {
	p, stderr := stderrChild(t, 1000)
	line := strings.Repeat("b", 99) + "\n"
	for range 30 {
		writeStderr(t, p, line, 0)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat(line, 10) + "[stderr truncated]\n"
	if got := stderr.String(); got != want {
		t.Fatalf("stderr has %d bytes, %d markers; want 10 lines and one marker",
			len(got), strings.Count(got, "truncated"))
	}
	tail := jsonrpc.StderrTail(p)
	if len(tail) != 1000 || !bytes.HasSuffix(tail, []byte(line)) {
		t.Fatalf("tail = %d bytes", len(tail))
	}
}

func TestStderrWithoutNewlines(t *testing.T) {
	p, stderr := stderrChild(t, 8000)
	writeStderr(t, p, "", 300<<10)
	want := strings.Repeat("e", 4096) + " [line truncated]\n"
	waitFor(t, "the cut line", func() bool { return stderr.String() == want })
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); got != want {
		t.Fatalf("stderr grew to %d bytes after the cut", len(got))
	}
	if tail := jsonrpc.StderrTail(p); len(tail) != 8000 {
		t.Fatalf("tail = %d bytes, want 8000", len(tail))
	}
}

func TestChildExitIsReported(t *testing.T) {
	p := launch(t, jsonrpctest.ChildSpec(t, "serve"))
	_ = p.Conn().Call(context.Background(), "exit", map[string]int{"code": 3}, nil)
	waitExited(t, p)
	if p.ExitCode() != 3 {
		t.Fatalf("exit code = %d", p.ExitCode())
	}
	select {
	case <-p.Conn().Done():
	default:
		t.Fatal("connection still open after the child exited")
	}
	if err := p.Conn().Call(context.Background(), "echo", nil, nil); !errors.Is(err, jsonrpc.ErrClosed) {
		t.Fatalf("call after exit = %v", err)
	}
}
