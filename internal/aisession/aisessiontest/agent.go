package aisessiontest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

// ChildMode is the jsonrpctest child mode of the fake session agent.
const ChildMode = "aisession-agent"

// SessionToken and CwdToken in a Call's params become the ACP session ID
// and the cwd session/new gave.
const (
	SessionToken = "$SESSION"
	CwdToken     = "$CWD"
)

// Script is what the fake agent does. It answers initialize with
// ProtocolVersion (default 1), session/new with "fake-session" (or
// NewSessionError), set_mode with {}, and plays Steps after session/prompt,
// then answers Stop (default end_turn).
type Script struct {
	ProtocolVersion int    `json:"protocol_version,omitempty"`
	NewSessionError int    `json:"new_session_error,omitempty"`
	Stop            string `json:"stop,omitempty"`
	Steps           []Step `json:"steps"`
}

// Step is one thing the fake does during the turn.
type Step struct {
	// Update is sent as session/update {"update": Update}.
	Update json.RawMessage `json:"update,omitempty"`
	// Call calls a client method and logs its answer.
	Call *Call `json:"call,omitempty"`
	// Sleep waits; it ends early when session/cancel arrives.
	Sleep string `json:"sleep,omitempty"`
	// Crash exits at once.
	Crash bool `json:"crash,omitempty"`
	// WaitCancel waits until session/cancel arrives (or 30 s).
	WaitCancel bool `json:"wait_cancel,omitempty"`
}

// Call is one client request.
type Call struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	// Async does not wait for the answer before the next step.
	Async bool `json:"async,omitempty"`
}

// LogEntry is one line of the fake's log.
type LogEntry struct {
	Env    []string        `json:"env,omitempty"` // names only, at start
	EnvSet map[string]bool `json:"env_set,omitempty"`
	Dir    string          `json:"dir,omitempty"`
	// Received: a method the client sent, with its params.
	Received string          `json:"received,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
	// Called: a client method the fake called, and the answer.
	Called    string          `json:"called,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	ErrorCode int             `json:"error_code,omitempty"`
	ErrorData string          `json:"error_data,omitempty"`
}

// Installed is a fake agent on disk.
type Installed struct {
	Dir  string // the PATH directory holding it
	Name string
	Log  string
}

// Install writes the fake agent name into a fresh temporary directory and
// returns it; put Installed.Dir on PATH (Home's dirs).
func Install(tb testing.TB, name string, s Script) Installed {
	tb.Helper()
	skipWindows(tb)
	dir := tb.TempDir()
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	data := tb.TempDir()
	script := filepath.Join(data, name+".json")
	log := filepath.Join(data, name+".log")
	b, err := json.Marshal(s)
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(script, b, 0o600); err != nil {
		tb.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		tb.Fatal(err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		tb.Fatal(err)
	}
	body := fmt.Sprintf("#!/bin/sh\nexec %s %s %s %s %s \"$@\"\n", shq(exe), shq(jsonrpctest.ChildArg), shq(ChildMode), shq(script), shq(log))
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.Chmod(p, 0o755); err != nil { //nolint:gosec // a fake harness must be executable
		tb.Fatal(err)
	}
	return Installed{Dir: dir, Name: name, Log: log}
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ReadLog reads the fake's log.
func ReadLog(tb testing.TB, path string) []LogEntry {
	tb.Helper()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	var out []LogEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var e LogEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// Calls returns the logged answers to client calls of method, in order.
func Calls(entries []LogEntry, method string) []LogEntry {
	var out []LogEntry
	for _, e := range entries {
		if e.Called == method {
			out = append(out, e)
		}
	}
	return out
}

func init() { jsonrpctest.RegisterChild(ChildMode, agent) }

type logger struct {
	mu   sync.Mutex
	path string
}

func (l *logger) write(e LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, _ = f.Write(append(b, '\n'))
}

// credential-like names whose presence the log records (never values).
var watched = []string{"GH_TOKEN", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY", "SSH_AUTH_SOCK", "CLAUDE_CODE_SESSION_ID", "AGENTFLOW_HOME", "FAKE_AGENT_KEY"}

func agent(args []string) int {
	if len(args) < 2 {
		return 2
	}
	for _, a := range args[2:] {
		if a == "--version" {
			fmt.Println("fake-agent 1.0.0")
			return 0
		}
	}
	b, err := os.ReadFile(args[0])
	if err != nil {
		return 2
	}
	var s Script
	if json.Unmarshal(b, &s) != nil {
		return 2
	}
	lg := &logger{path: args[1]}
	var names []string
	set := map[string]bool{}
	for _, kv := range os.Environ() {
		n, _, _ := strings.Cut(kv, "=")
		names = append(names, n)
	}
	sort.Strings(names)
	for _, w := range watched {
		_, set[w] = os.LookupEnv(w)
	}
	wd, _ := os.Getwd()
	lg.write(LogEntry{Env: names, EnvSet: set, Dir: wd})

	var (
		conn      jsonrpc.Conn
		ready     = make(chan struct{})
		cwd       string
		cancelled = make(chan struct{})
		once      sync.Once
	)
	const sessionID = "fake-session"
	h := jsonrpc.HandlerFunc(func(ctx context.Context, r *jsonrpc.Request) (any, error) {
		<-ready
		lg.write(LogEntry{Received: r.Method, Params: r.Params})
		switch r.Method {
		case acp.MethodInitialize:
			v := s.ProtocolVersion
			if v == 0 {
				v = 1
			}
			return map[string]any{"protocolVersion": v, "agentCapabilities": map[string]any{}, "authMethods": []any{}}, nil
		case acp.MethodSessionNew:
			if s.NewSessionError != 0 {
				return nil, &jsonrpc.Error{Code: s.NewSessionError, Message: "fake"}
			}
			var p acp.NewSessionRequest
			_ = json.Unmarshal(r.Params, &p)
			cwd = p.Cwd
			return map[string]any{"sessionId": sessionID, "modes": map[string]any{"currentModeId": "ask", "availableModes": []any{map[string]any{"id": "ask", "name": "Ask"}, map[string]any{"id": "plan", "name": "Plan"}}}}, nil
		case acp.MethodSessionSetMode:
			return map[string]any{}, nil
		case acp.MethodSessionCancel:
			once.Do(func() { close(cancelled) })
			return nil, nil
		case acp.MethodSessionPrompt:
			stop := s.Stop
			if stop == "" {
				stop = "end_turn"
			}
			var wg sync.WaitGroup
			for _, st := range s.Steps {
				select {
				case <-cancelled:
					wg.Wait()
					return map[string]any{"stopReason": "cancelled"}, nil
				default:
				}
				switch {
				case st.Crash:
					os.Exit(3)
				case st.Sleep != "":
					d, _ := time.ParseDuration(st.Sleep)
					select {
					case <-time.After(d):
					case <-cancelled:
					}
				case st.WaitCancel:
					select {
					case <-time.After(30 * time.Second):
					case <-cancelled:
					}
				case st.Update != nil:
					_ = conn.Notify(ctx, acp.MethodSessionUpdate, map[string]any{"sessionId": sessionID, "update": st.Update})
				case st.Call != nil:
					call := *st.Call
					params := strings.NewReplacer(SessionToken, sessionID, CwdToken, cwd).Replace(string(call.Params))
					do := func() {
						var res json.RawMessage
						err := conn.Call(context.Background(), call.Method, json.RawMessage(params), &res)
						e := LogEntry{Called: call.Method, Result: res}
						var rerr *jsonrpc.Error
						if errors.As(err, &rerr) {
							e.ErrorCode = rerr.Code
							var d jsonrpc.ErrorData
							_ = json.Unmarshal(rerr.Data, &d)
							e.ErrorData = d.Code
						} else if err != nil {
							e.ErrorCode = -1
							e.ErrorData = err.Error()
						}
						lg.write(e)
					}
					if call.Async {
						wg.Add(1)
						go func() { defer wg.Done(); do() }()
					} else {
						do()
					}
				}
			}
			wg.Wait()
			select {
			case <-cancelled:
				return map[string]any{"stopReason": "cancelled"}, nil
			default:
			}
			return map[string]any{"stopReason": stop}, nil
		}
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
	})
	conn = jsonrpc.NewConn(os.Stdin, os.Stdout, h, jsonrpc.Limits{MaxFrame: 8 << 20, MaxInFlight: 16, CallTimeout: 10 * time.Minute})
	close(ready)
	<-conn.Done()
	return 0
}

// Update builders.

// Message is an agent_message_chunk update.
func Message(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
	return b
}

// ToolCall is a tool_call update.
func ToolCall(id, kind, status string, locations ...string) json.RawMessage {
	var locs []map[string]string
	for _, l := range locations {
		locs = append(locs, map[string]string{"path": l})
	}
	b, _ := json.Marshal(map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": "tool " + id, "kind": kind, "status": status, "locations": locs})
	return b
}

// Params marshals v for a Call.
func Params(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
