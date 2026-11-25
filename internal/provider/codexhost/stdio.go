package codexhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"
)

const (
	maxFrameBytes  = 8 << 20
	maxOutputBytes = 32 << 20
	shutdownGrace  = 500 * time.Millisecond
)

type host struct {
	ctx       context.Context
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	scanner   *bufio.Scanner
	stopClose func() bool
	nextID    int
}

func startHost(ctx context.Context, dir string) (*host, error) {
	const operation = "start app-server"
	if err := ctx.Err(); err != nil {
		return nil, contextError(operation, err)
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		kind := Unavailable
		if errors.Is(err, exec.ErrDot) || errors.Is(err, os.ErrPermission) {
			kind = Permission
		}
		return nil, hostError(kind, operation, "cannot resolve codex executable on PATH")
	}

	// The default provider can preconnect its model WebSocket at thread start.
	// An inert provider avoids that connection while Codex retains its MCP setup.
	cmd := exec.CommandContext(ctx, executable, "app-server", "--listen", "stdio://",
		"-c", `mcp_servers.devflow={enabled=false,command="devflow"}`,
		"-c", "features.hooks=false",
		"-c", "analytics.enabled=false",
		"-c", "check_for_update_on_startup=false",
		"-c", `model_provider="devflow_codex_host"`,
		"-c", `model_providers.devflow_codex_host={name="Devflow MCP transport",base_url="http://127.0.0.1:0",supports_websockets=false}`)
	cmd.Dir = dir
	cmd.Env = hostEnv()
	// A nil Stderr discards arbitrary diagnostics without retaining secrets or
	// letting logging back up the subprocess.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, hostError(Failure, operation, "cannot open app-server input")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, hostError(Failure, operation, "cannot open app-server output")
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		if ctx.Err() != nil {
			return nil, contextError(operation, ctx.Err())
		}
		if errors.Is(err, os.ErrPermission) {
			return nil, hostError(Permission, operation, "cannot launch app-server")
		}
		return nil, hostError(Failure, operation, "cannot launch app-server")
	}

	scanner := bufio.NewScanner(io.LimitReader(stdout, maxOutputBytes))
	scanner.Buffer(make([]byte, 64<<10), maxFrameBytes)
	stopClose := context.AfterFunc(ctx, func() {
		_ = stdin.Close()
		_ = stdout.Close()
	})
	return &host{ctx: ctx, cmd: cmd, stdin: stdin, scanner: scanner, stopClose: stopClose}, nil
}

func hostEnv() []string {
	env := []string{activeEnvKey + "=1"}
	// Forward only paths used for discovery and platform runtimes. Codex
	// manages host authentication through its own configuration and stores.
	for _, name := range []string{
		"PATH", "HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
		"XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP", "USERPROFILE", "APPDATA",
		"LOCALAPPDATA", "SystemRoot", "SystemDrive", "COMSPEC", "PATHEXT",
	} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func (h *host) close() {
	// EOF lets the app-server shut down its MCP sessions. Bound shutdown in
	// case the host or a server is stuck.
	_ = h.stdin.Close()
	timer := time.AfterFunc(shutdownGrace, func() { _ = h.cmd.Process.Kill() })
	_ = h.cmd.Wait()
	timer.Stop()
	h.stopClose()
}

type request struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (h *host) request(method string, params any) (json.RawMessage, error) {
	h.nextID++
	id := strconv.Itoa(h.nextID)
	if err := h.send(method, request{h.nextID, method, params}); err != nil {
		return nil, err
	}

	for h.scanner.Scan() {
		if err := h.ctx.Err(); err != nil {
			return nil, contextError(method, err)
		}
		var msg message
		if err := json.Unmarshal(h.scanner.Bytes(), &msg); err != nil {
			return nil, hostError(Failure, method, "invalid app-server message")
		}
		if msg.Method != "" {
			if len(msg.ID) != 0 {
				return nil, h.denyRequest(method, msg)
			}
			continue
		}
		if string(msg.ID) != id {
			return nil, hostError(Failure, method, "unexpected app-server response id")
		}
		if msg.Error != nil {
			err := hostError(classifyError(msg.Error.Code, msg.Error.Message), method, "app-server rejected request")
			err.RPCCode = msg.Error.Code
			return nil, err
		}
		if len(msg.Result) == 0 || msg.Result[0] != '{' {
			return nil, hostError(Failure, method, "missing or invalid app-server result")
		}
		return msg.Result, nil
	}
	if err := h.ctx.Err(); err != nil {
		return nil, contextError(method, err)
	}
	if h.scanner.Err() != nil {
		return nil, hostError(Failure, method, "invalid or oversized app-server output")
	}
	return nil, hostError(Failure, method, "app-server output ended or exceeded its limit")
}

func (h *host) send(operation string, value any) error {
	if err := h.ctx.Err(); err != nil {
		return contextError(operation, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) >= maxFrameBytes {
		return hostError(Failure, operation, "invalid or oversized app-server request")
	}
	if _, err := h.stdin.Write(append(encoded, '\n')); err != nil {
		if h.ctx.Err() != nil {
			return contextError(operation, h.ctx.Err())
		}
		return hostError(Failure, operation, "cannot write to app-server")
	}
	return nil
}

func contextError(operation string, cause error) *Error {
	err := hostError(Failure, operation, cause.Error())
	err.cause = cause
	return err
}

func (h *host) denyRequest(operation string, msg message) error {
	reply := struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result,omitempty"`
		Error  *rpcError       `json:"error,omitempty"`
	}{ID: msg.ID}
	kind := Permission
	switch msg.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		reply.Result = map[string]string{"decision": "cancel"}
	case "applyPatchApproval", "execCommandApproval":
		reply.Result = map[string]string{"decision": "abort"}
	case "item/permissions/requestApproval":
		reply.Result = map[string]any{"permissions": struct{}{}, "scope": "turn"}
	case "mcpServer/elicitation/request":
		reply.Result = map[string]string{"action": "cancel"}
	case "item/tool/requestUserInput":
		reply.Result = map[string]any{"answers": struct{}{}}
	case "item/tool/call":
		reply.Result = map[string]any{"contentItems": []any{}, "success": false}
	case "account/chatgptAuthTokens/refresh":
		kind = Authentication
		reply.Error = &rpcError{-32601, "external authentication is unavailable"}
	default:
		reply.Error = &rpcError{-32601, "server request is unsupported"}
	}
	if err := h.send(operation, reply); err != nil {
		return err
	}
	return hostError(kind, operation, "app-server requested an unsupported client action")
}
