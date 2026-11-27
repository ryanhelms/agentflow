package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

func init() {
	registerTier(manifest.TierProcess, func(h *Host) Factory { return processFactory{h: h} })
}

var (
	errNoLauncher = errors.New("process plugins cannot be launched by this build")
	errBadExec    = errors.New("the plugin's exec command is not inside its directory")
)

type processFactory struct{ h *Host }

func (processFactory) Tier() manifest.Tier { return manifest.TierProcess }

func (f processFactory) New(p manifest.Installed) (Instance, error) {
	if p.Manifest.Exec == nil || p.Dir == "" {
		return nil, coded(errBadExec, bus.PluginCodeManifestInvalid, true)
	}
	return &process{h: f.h, p: p, done: make(chan struct{}), started: make(chan struct{})}, nil
}

// process is the process tier's Instance: a Proxy (and Channel Adapter)
// for the child, over JSON-RPC on its stdin and stdout. The child's own
// requests reach the host only through Env.
type process struct {
	h *Host
	p manifest.Installed

	env   Env
	proc  jsonrpc.Process
	conn  jsonrpc.Conn
	ready atomic.Bool
	// started is closed when Start returns. A child may send its first
	// request right after answering initialize, before Start has read the
	// answer: handle waits for it rather than refuse not_ready.
	started   chan struct{}
	startOnce sync.Once

	once sync.Once
	done chan struct{}
	mu   sync.Mutex
	err  error
}

// execPath is the absolute executable of p inside its installed copy.
func execPath(p manifest.Installed) (string, error) {
	cmd := p.Manifest.Exec.Command
	if cmd == "" || filepath.IsAbs(cmd) || strings.Contains(filepath.ToSlash(cmd), "..") {
		return "", errBadExec
	}
	path := filepath.Join(p.Dir, filepath.FromSlash(cmd))
	if runtime.GOOS == "windows" && filepath.Ext(path) == "" {
		if _, err := os.Stat(path); err != nil {
			path += ".exe"
		}
	}
	return path, nil
}

func (pr *process) Start(ctx context.Context, hostEnv Env) error {
	defer pr.startOnce.Do(func() { close(pr.started) })
	pr.env = hostEnv
	if pr.h.o.Launch == nil || pr.h.o.Environ == nil {
		return coded(errNoLauncher, bus.PluginCodeLaunch, false)
	}
	path, err := execPath(pr.p)
	if err != nil {
		return coded(err, bus.PluginCodeManifestInvalid, true)
	}
	data := filepath.Join(pr.h.o.Home, manifest.PluginsDir, pr.p.ID, manifest.DataDir)
	vars, err := pr.h.o.Environ(data, map[string]string{
		jsonrpc.EnvPluginID: pr.p.ID, jsonrpc.EnvPluginDir: pr.p.Dir, jsonrpc.EnvPluginData: data,
		jsonrpc.EnvPluginProtocol: strconv.Itoa(manifest.ProtocolVersion),
	})
	if err != nil {
		return coded(err, bus.PluginCodeLaunch, false)
	}
	var stderr io.Writer
	life := ctx // the launch context bounds the child's whole life
	if e, ok := hostEnv.(*env); ok {
		life = e.r.ctx
		if e.log != nil {
			stderr = e.log
		}
	}
	proc, err := pr.h.o.Launch(life, jsonrpc.LaunchSpec{Path: path, Root: pr.p.Dir, Dir: data, Args: pr.p.Manifest.Exec.Args, Env: vars,
		Stderr: stderr, Handler: jsonrpc.HandlerFunc(pr.handle)})
	if err != nil {
		return coded(err, bus.PluginCodeLaunch, false)
	}
	pr.proc, pr.conn = proc, proc.Conn()
	go pr.watch()
	perms := pr.p.Manifest.Permissions
	params := InitializeParams{ProtocolVersion: manifest.ProtocolVersion, Agentflow: pr.h.o.Version,
		Plugin: PluginIdent{ID: pr.p.ID, Version: pr.p.Version}, Settings: hostEnv.Settings(), Permissions: perms,
		Limits: WireLimits{MaxFrame: jsonrpc.DefaultMaxFrame, DeliverTimeoutMS: int(DeliverTimeout.Milliseconds()),
			InvokesPerMinute: InvokesPerMinute, PublishesPerMinute: PublishesPerMinute}}
	var res InitializeResult
	if err := pr.conn.Call(ctx, MethodInitialize, params, &res); err != nil {
		_ = proc.Kill()
		return pr.callError(err)
	}
	if res.ProtocolVersion != manifest.ProtocolVersion {
		_ = proc.Kill()
		return coded(errors.New("wrong protocol version"), bus.PluginCodeProtocol, true)
	}
	pr.ready.Store(true)
	return nil
}

// watch ends the instance when the child exits or breaks the protocol.
func (pr *process) watch() {
	select {
	case <-pr.proc.Exited():
		code := bus.PluginCodeExited
		if pr.proc.ExitCode() < 0 {
			code = bus.PluginCodeCrashed
		}
		pr.end(coded(nil, code, false))
	case <-pr.conn.Done():
		err := pr.conn.Err()
		if errors.Is(err, jsonrpc.ErrProtocol) || errors.Is(err, jsonrpc.ErrFrameTooLarge) {
			_ = pr.proc.Kill()
			pr.end(coded(nil, bus.PluginCodeProtocol, false))
			return
		}
		<-pr.proc.Exited()
		code := bus.PluginCodeExited
		if pr.proc.ExitCode() < 0 {
			code = bus.PluginCodeCrashed
		}
		pr.end(coded(nil, code, false))
	}
}

func (pr *process) end(err error) {
	pr.once.Do(func() {
		pr.mu.Lock()
		pr.err = err
		pr.mu.Unlock()
		close(pr.done)
	})
}

// callError gives a failed call to the child its code.
func (pr *process) callError(err error) error {
	var rpcErr *jsonrpc.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return coded(err, bus.PluginCodeTimeout, false)
	case errors.As(err, &rpcErr):
		return coded(errors.New("plugin answered with an error"), ReasonRPCError, false)
	case errors.Is(err, jsonrpc.ErrProtocol), errors.Is(err, jsonrpc.ErrFrameTooLarge):
		return coded(err, bus.PluginCodeProtocol, false)
	}
	if e := pr.conn.Err(); errors.Is(e, jsonrpc.ErrProtocol) || errors.Is(e, jsonrpc.ErrFrameTooLarge) {
		return coded(err, bus.PluginCodeProtocol, false)
	}
	// The connection closed: the child is ending; wait for its reason.
	select {
	case <-pr.done:
		return pr.Err()
	case <-time.After(ShutdownTimeout):
		return coded(err, bus.PluginCodeExited, false)
	}
}

func (pr *process) Deliver(ctx context.Context, e bus.Event) error {
	if err := pr.conn.Call(ctx, MethodEventDeliver, EventDeliverParams{Event: e}, nil); err != nil {
		return pr.callError(err)
	}
	return nil
}

func (pr *process) Settings(ctx context.Context, s map[string]any) error {
	return pr.conn.Notify(ctx, MethodSettingsChanged, SettingsChangedParams{Settings: s})
}

func (pr *process) Ping(ctx context.Context) error {
	return pr.conn.Call(ctx, MethodPing, struct{}{}, nil)
}

// Stop sends shutdown, then stops the child: stdin closed, killed after
// the grace period.
func (pr *process) Stop(ctx context.Context) error {
	if pr.proc == nil {
		return nil
	}
	sctx, cancel := context.WithTimeout(ctx, ShutdownTimeout)
	_ = pr.conn.Call(sctx, MethodShutdown, struct{}{}, nil)
	cancel()
	err := pr.proc.Stop()
	_ = pr.conn.Close()
	return err
}

func (pr *process) Done() <-chan struct{} { return pr.done }

func (pr *process) Err() error {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	return pr.err
}

// handle answers the child's requests through Env. Nothing is answered
// before initialize succeeded. A panic in a request (a command the plugin
// invoked, say) is this plugin's crash: the child is killed, restarted
// with backoff, and the daemon carries on.
func (pr *process) handle(ctx context.Context, r *jsonrpc.Request) (result any, err error) {
	defer func() {
		if p := recover(); p != nil {
			if pr.env != nil {
				pr.env.Log("error", fmt.Sprintf("request %s panicked in the host", r.Method))
			}
			if pr.proc != nil {
				_ = pr.proc.Kill()
			}
			pr.end(coded(fmt.Errorf("request %s panicked", r.Method), bus.PluginCodeCrashed, false))
			result, err = nil, errors.New("internal error") // sent as internal error
		}
	}()
	if !pr.ready.Load() {
		select {
		case <-pr.started:
		case <-pr.done:
		case <-ctx.Done():
		}
	}
	if !pr.ready.Load() {
		return nil, rpcRefusal(refuse(RefuseNotReady))
	}
	switch r.Method {
	case MethodSubscribe:
		var p SubscribeParams
		if err := strictParams(r.Params, &p); err != nil {
			return nil, err
		}
		types, err := pr.env.Subscribe(ctx, p.Types)
		if err != nil {
			return nil, rpcRefusal(err)
		}
		return SubscribeResult{Types: types}, nil
	case MethodCommandInvoke:
		var p InvokeRequest
		if err := strictParams(r.Params, &p); err != nil {
			return nil, err
		}
		res, err := pr.env.Invoke(ctx, p)
		if err != nil {
			return nil, rpcRefusal(err)
		}
		return res, nil
	case MethodEventPublish:
		var p PublishRequest
		if err := strictParams(r.Params, &p); err != nil {
			return nil, err
		}
		res, err := pr.env.Publish(ctx, p)
		if err != nil {
			return nil, rpcRefusal(err)
		}
		return res, nil
	case MethodLog:
		var p LogParams
		if strictParams(r.Params, &p) == nil {
			pr.env.Log(p.Level, p.Message)
		}
		return nil, nil
	}
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
}

func strictParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params"}
	}
	return nil
}

// rpcRefusal turns a host refusal into a JSON-RPC error with its code.
func rpcRefusal(err error) error {
	var r *Refusal
	if !errors.As(err, &r) {
		return err // becomes "internal error"; its text is never sent
	}
	code := jsonrpc.CodeForbidden
	if r.Code == RefuseRateLimited {
		code = jsonrpc.CodeRateLimited
	}
	data, _ := json.Marshal(jsonrpc.ErrorData{Code: r.Code})
	return &jsonrpc.Error{Code: code, Message: "refused: " + r.Code, Data: data}
}
