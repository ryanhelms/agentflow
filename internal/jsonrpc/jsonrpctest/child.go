package jsonrpctest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// ChildArg, as the first argument, turns the test binary into a child
// process: the second argument names the mode (see RunChild).
const ChildArg = "-jsonrpctest.child"

var (
	childMu    sync.Mutex
	childModes = map[string]func(args []string) int{
		"serve":   serve,
		"sleep":   func([]string) int { sleepForever(); return 0 },
		"flood":   flood,
		"garbage": garbage,
	}
)

// RegisterChild adds a child mode. fn gets the arguments after the mode and
// returns the exit code. Call it from TestMain before RunChild.
func RegisterChild(name string, fn func(args []string) int) {
	childMu.Lock()
	defer childMu.Unlock()
	childModes[name] = fn
}

// RunChild runs the child mode and exits when the test binary was started
// with ChildArg; otherwise it returns at once. Call it first in TestMain:
//
//	func TestMain(m *testing.M) {
//		jsonrpctest.RunChild()
//		os.Exit(m.Run())
//	}
//
// Built-in modes:
//
//   - serve [ignore-eof]: answers JSON-RPC on stdin/stdout (methods below),
//     exits when stdin closes unless ignore-eof is given;
//   - sleep: sleeps until killed;
//   - flood: writes stdout without a newline until killed;
//   - garbage: writes a line that is not JSON, then sleeps.
//
// serve methods: echo (returns its params), env (the sorted environment),
// pid, spawn (starts a "sleep" grandchild, returns its PID), hang (never
// answers), notify (sends the notification "note" with its params back),
// call {method, params} (calls the parent and returns its result), stderr
// {n} (writes n bytes to stderr), exit {code}.
func RunChild() {
	if len(os.Args) < 3 || os.Args[1] != ChildArg {
		return
	}
	childMu.Lock()
	fn := childModes[os.Args[2]]
	childMu.Unlock()
	if fn == nil {
		fmt.Fprintf(os.Stderr, "jsonrpctest: unknown child mode %q\n", os.Args[2])
		os.Exit(2)
	}
	os.Exit(fn(os.Args[3:]))
}

// ChildSpec returns a LaunchSpec that runs this test binary in mode: Path
// is the resolved test executable, Root its directory, and Env the
// ScrubbedEnv of this process with a fresh data directory. The test binary's
// TestMain must call RunChild.
func ChildSpec(tb testing.TB, mode string, args ...string) jsonrpc.LaunchSpec {
	tb.Helper()
	exe, err := os.Executable()
	if err != nil {
		tb.Fatal(err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		tb.Fatal(err)
	}
	data := tb.TempDir()
	env, err := jsonrpc.ScrubbedEnv(os.Environ(), data, map[string]string{
		jsonrpc.EnvPluginData: data,
	})
	if err != nil {
		tb.Fatal(err)
	}
	return jsonrpc.LaunchSpec{
		Path: exe,
		Root: filepath.Dir(exe),
		Args: append([]string{ChildArg, mode}, args...),
		Env:  env,
		// A race-enabled child sleeps 1 s at exit (GORACE atexit_sleep_ms),
		// and the scrubbed environment cannot change that.
		ShutdownGrace: 5 * time.Second,
	}
}

func serve(args []string) int {
	ignoreEOF := len(args) > 0 && args[0] == "ignore-eof"
	var c jsonrpc.Conn
	ready := make(chan struct{})
	h := jsonrpc.HandlerFunc(func(ctx context.Context, r *jsonrpc.Request) (any, error) {
		<-ready
		switch r.Method {
		case "echo":
			if len(r.Params) == 0 {
				return nil, nil
			}
			return r.Params, nil
		case "env":
			env := os.Environ()
			sort.Strings(env)
			return env, nil
		case "pid":
			return os.Getpid(), nil
		case "spawn":
			return spawnSleeper()
		case "hang":
			<-ctx.Done()
			return nil, ctx.Err()
		case "notify":
			return struct{}{}, c.Notify(ctx, "note", r.Params)
		case "call":
			var p struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(r.Params, &p); err != nil {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params"}
			}
			var params any
			if len(p.Params) > 0 {
				params = p.Params
			}
			var out json.RawMessage
			if err := c.Call(ctx, p.Method, params, &out); err != nil {
				return nil, err
			}
			return out, nil
		case "stderr":
			var p struct {
				N int `json:"n"`
			}
			_ = json.Unmarshal(r.Params, &p)
			_, _ = os.Stderr.WriteString(strings.Repeat("e", p.N))
			return struct{}{}, nil
		case "exit":
			var p struct {
				Code int `json:"code"`
			}
			_ = json.Unmarshal(r.Params, &p)
			os.Exit(p.Code)
		}
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
	})
	c = jsonrpc.NewConn(os.Stdin, os.Stdout, h, jsonrpc.Limits{})
	close(ready)
	<-c.Done()
	if ignoreEOF {
		sleepForever()
	}
	return 0
}

func spawnSleeper() (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := &exec.Cmd{Path: exe, Args: []string{exe, ChildArg, "sleep"}, Env: os.Environ()}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

func flood([]string) int {
	chunk := []byte(strings.Repeat("x", 64<<10))
	for {
		if _, err := os.Stdout.Write(chunk); err != nil {
			sleepForever()
		}
	}
}

func garbage([]string) int {
	_, _ = os.Stdout.WriteString("this is not json\n")
	sleepForever()
	return 0
}

// sleepForever blocks until the process is killed. (An empty select would
// trip the runtime's deadlock check when no other goroutine runs.)
func sleepForever() {
	for {
		time.Sleep(time.Hour)
	}
}
