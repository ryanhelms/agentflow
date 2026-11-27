// Package acptest is a fake ACP agent for tests ("Phase 6 ACP initialize
// slice" in docs/knowledge/engineering/decisions/event-driven-platform.md).
//
// Install puts an executable in a test's temporary directory that answers
// initialize as a Script says. It is the test binary itself, started in the
// "acptest-agent" child mode of internal/jsonrpc/jsonrpctest, so a test
// binary that uses Install calls jsonrpctest.RunChild first in TestMain:
//
//	func TestMain(m *testing.M) {
//		jsonrpctest.RunChild()
//		os.Exit(m.Run())
//	}
//
// cmd/fake-acp is a standalone fake for the phase demo, built by hand.
// Lane 7.1 adds session scripts.
package acptest

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
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
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

// ChildMode is the jsonrpctest child mode the installed fake runs in.
const ChildMode = "acptest-agent"

// Script says how the fake agent answers initialize.
type Script struct {
	// Initialize is the answer; nil means OK().
	Initialize *acp.InitializeResponse
	// InitializeError, when set, is sent instead of an answer.
	InitializeError *jsonrpc.Error
	// Delay waits before answering (a large one makes a hanging agent).
	Delay time.Duration
	// ExitBefore exits (code 3) when initialize arrives, without answering.
	ExitBefore bool
	// Garbage writes a line that is not JSON-RPC instead of answering, then
	// waits until it is killed.
	Garbage bool
	// Version is printed as "<name> <Version>" when the fake is started
	// with --version; empty means "1.2.3".
	Version string

	// Lane 6.2 additions, for the probe's tests.

	// Log, when set, is a file the fake appends one JSON line to per thing
	// it saw (LogEntry): its arguments, environment names and working
	// directory at start, every message from the client, and the error code
	// of its Ask call.
	Log string
	// Ask, when set, is a method the fake calls on the client after
	// initialize arrives and before it answers.
	Ask string
	// Stderr is written to stderr when initialize arrives.
	Stderr string
	// Spawn starts a grandchild that sleeps until killed and logs its PID
	// (LogEntry.Spawned), to show the probe kills the process group.
	Spawn bool
}

// LogEntry is one line of Script.Log.
type LogEntry struct {
	Args    []string        `json:"args,omitempty"`
	Env     []string        `json:"env,omitempty"` // names only, sorted
	Dir     string          `json:"dir,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Notify  bool            `json:"notify,omitempty"`
	Asked   string          `json:"asked,omitempty"`
	Code    int             `json:"code,omitempty"`
	Spawned int             `json:"spawned,omitempty"`
}

// ReadLog reads a Script.Log file. A missing file is no entries.
func ReadLog(path string) ([]LogEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []LogEntry
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for s.Scan() {
		var e LogEntry
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, s.Err()
}

// OK is a valid ACP v1 initialize answer with one agent and one terminal
// sign-in method.
func OK() acp.InitializeResponse {
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersion,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession:        true,
			PromptCapabilities: acp.PromptCapabilities{Image: true, EmbeddedContext: true},
			MCPCapabilities:    acp.MCPCapabilities{HTTP: true},
		},
		AuthMethods: []acp.AuthMethod{
			{ID: "oauth", Name: "Log in", Description: "Sign in with your account"},
			{ID: "login", Name: "Log in from a terminal", Type: "terminal", Args: []string{"login"}},
		},
		AgentInfo: &acp.Implementation{Name: "fake-acp", Title: "Fake ACP", Version: "1.2.3"},
	}
}

// Install writes an executable name in dir that runs the fake agent with s,
// and returns the arguments the definition must pass (none on Unix). On
// Unix it is a /bin/sh wrapper that starts this test binary in ChildMode
// with s encoded in the wrapper. On Windows it is a copy of the test binary,
// name.exe, with s in name.exe.acptest.json beside it, and the arguments
// select the child mode. dir should be inside tb.TempDir().
func Install(tb testing.TB, dir, name string, s Script) (args []string) {
	tb.Helper()
	exe, err := os.Executable()
	if err != nil {
		tb.Fatal(err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		tb.Fatal(err)
	}
	enc, err := encode(installed{Name: name, Script: s})
	if err != nil {
		tb.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		target := filepath.Join(dir, name+".exe")
		if err := copyFile(exe, target); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(target+sidecarSuffix, []byte(enc), 0o600); err != nil {
			tb.Fatal(err)
		}
		return []string{jsonrpctest.ChildArg, ChildMode}
	}
	body := fmt.Sprintf("#!/bin/sh\nexec %s %s %s %s \"$@\"\n", shQuote(exe), shQuote(jsonrpctest.ChildArg), shQuote(ChildMode), shQuote(encPrefix+enc))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil { //nolint:gosec // a fake harness must be executable; 0755 is what the PATH checks accept
		tb.Fatal(err)
	}
	return nil
}

const (
	encPrefix     = "acptest:"
	sidecarSuffix = ".acptest.json"
)

type installed struct {
	Name   string `json:"name"`
	Script Script `json:"script"`
}

func encode(in installed) (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func init() {
	jsonrpctest.RegisterChild(ChildMode, child)
}

// child is the ChildMode entry point: args are the encoded script (Unix)
// followed by the definition's arguments.
func child(args []string) int {
	var in installed
	rest := args
	if len(args) > 0 && strings.HasPrefix(args[0], encPrefix) {
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(args[0], encPrefix))
		if err != nil || json.Unmarshal(b, &in) != nil {
			fmt.Fprintln(os.Stderr, "acptest: bad script")
			return 2
		}
		rest = args[1:]
	} else {
		exe, err := os.Executable()
		if err != nil {
			return 2
		}
		b, err := os.ReadFile(exe + sidecarSuffix)
		if err != nil || json.Unmarshal(b, &in) != nil {
			fmt.Fprintln(os.Stderr, "acptest: no script")
			return 2
		}
	}
	return Run(in.Name, in.Script, rest, os.Stdin, os.Stdout, os.Stderr)
}

// Run is the fake agent: with --version in args it prints the version and
// exits; otherwise it answers JSON-RPC on in and out as s says until in
// closes. cmd/fake-acp uses it too.
func Run(name string, s Script, args []string, in io.Reader, out io.WriteCloser, errOut io.Writer) int {
	for _, a := range args {
		if a == "--version" {
			v := s.Version
			if v == "" {
				v = "1.2.3"
			}
			fmt.Fprintf(out, "%s %s\n", name, v)
			return 0
		}
	}
	lg := &logger{path: s.Log}
	wd, _ := os.Getwd()
	lg.write(LogEntry{Args: append([]string{}, args...), Env: envNames(), Dir: wd})

	var conn jsonrpc.Conn
	ready := make(chan struct{})
	var outMu sync.Mutex
	h := jsonrpc.HandlerFunc(func(ctx context.Context, r *jsonrpc.Request) (any, error) {
		<-ready
		lg.write(LogEntry{Method: r.Method, Params: r.Params, Notify: r.Notification()})
		if r.Method != acp.MethodInitialize {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
		}
		if s.Stderr != "" {
			_, _ = io.WriteString(errOut, s.Stderr)
		}
		if s.Spawn {
			if pid, err := spawnSleeper(); err == nil {
				lg.write(LogEntry{Spawned: pid})
			}
		}
		if s.Ask != "" {
			err := conn.Call(ctx, s.Ask, map[string]any{}, nil)
			code := 0
			var rerr *jsonrpc.Error
			if errors.As(err, &rerr) {
				code = rerr.Code
			}
			lg.write(LogEntry{Asked: s.Ask, Code: code})
		}
		if s.Delay > 0 {
			select {
			case <-time.After(s.Delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		switch {
		case s.ExitBefore:
			os.Exit(3)
		case s.Garbage:
			outMu.Lock()
			_, _ = io.WriteString(out, "this is not json-rpc\n")
			outMu.Unlock()
			<-ctx.Done()
			return nil, ctx.Err()
		case s.InitializeError != nil:
			return nil, s.InitializeError
		case s.Initialize != nil:
			return s.Initialize, nil
		}
		ok := OK()
		return ok, nil
	})
	conn = jsonrpc.NewConn(in, out, h, jsonrpc.Limits{})
	close(ready)
	<-conn.Done()
	return 0
}

type logger struct {
	mu   sync.Mutex
	path string
}

func (l *logger) write(e LogEntry) {
	if l.path == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
}

func envNames() []string {
	var names []string
	for _, kv := range os.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok && name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o755) //nolint:gosec // the fake agent must be executable
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// spawnSleeper starts this binary in the jsonrpctest "sleep" mode, in the
// fake's own process group, and returns its PID.
func spawnSleeper() (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := &exec.Cmd{Path: exe, Args: []string{exe, jsonrpctest.ChildArg, "sleep"}, Env: os.Environ()}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}
