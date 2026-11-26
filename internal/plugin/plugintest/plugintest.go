// Package plugintest has fakes for the plugin host's tests: a
// manifest.Source, plugin.Layers, an in-memory jsonrpc.Conn pair and a
// jsonrpc.Process that runs a scripted child in-process. No external
// program ever starts.
package plugintest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/plugin"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// Source is a manifest.Source over a list. Set VerifyErr to fail Verify.
type Source struct {
	mu        sync.Mutex
	plugins   []manifest.Installed
	rev       int
	VerifyErr map[string]error
	Err       error
}

// Set replaces the installed plugins and bumps the revision.
func (s *Source) Set(ps ...manifest.Installed) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plugins = append([]manifest.Installed(nil), ps...)
	sort.Slice(s.plugins, func(i, j int) bool { return s.plugins[i].ID < s.plugins[j].ID })
	s.rev++
}

// SetVerifyErr makes Verify of id fail with err (nil clears it).
func (s *Source) SetVerifyErr(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.VerifyErr == nil {
		s.VerifyErr = map[string]error{}
	}
	s.VerifyErr[id] = err
}

// Installed implements manifest.Source.
func (s *Source) Installed(context.Context) ([]manifest.Installed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]manifest.Installed(nil), s.plugins...), s.Err
}

// Verify implements manifest.Source.
func (s *Source) Verify(_ context.Context, p manifest.Installed) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.VerifyErr[p.ID]
}

// Revision implements manifest.Source.
func (s *Source) Revision() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprint(s.rev)
}

// Layers is a plugin.Layers over maps.
type Layers struct {
	mu       sync.Mutex
	personal map[string]plugin.PersonalPlugin
	disabled map[string][]string // by worktree root
}

// SetPersonal sets personal:plugins.<id>.
func (l *Layers) SetPersonal(id string, p plugin.PersonalPlugin) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.personal == nil {
		l.personal = map[string]plugin.PersonalPlugin{}
	}
	l.personal[id] = p
}

// SetDisabled sets repo:plugins.disabled of the worktree at root.
func (l *Layers) SetDisabled(root string, ids ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.disabled == nil {
		l.disabled = map[string][]string{}
	}
	l.disabled[root] = ids
}

// Personal implements plugin.Layers.
func (l *Layers) Personal(context.Context) (map[string]plugin.PersonalPlugin, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]plugin.PersonalPlugin{}
	for k, v := range l.personal {
		out[k] = v
	}
	return out, nil
}

// RepoDisabled implements plugin.Layers.
func (l *Layers) RepoDisabled(_ context.Context, root string) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.disabled[root]...), nil
}

// conn is one end of Pipe. A Call runs the peer's handler on its own
// goroutine, with params and results through JSON as on the wire.
type conn struct {
	peer    *conn
	handler jsonrpc.Handler

	once sync.Once
	done chan struct{}
	mu   sync.Mutex
	err  error
}

// Pipe returns two connected Conns; a's requests go to hb, b's to ha.
func Pipe(ha, hb jsonrpc.Handler) (a, b jsonrpc.Conn) {
	ca, cb := &conn{handler: ha, done: make(chan struct{})}, &conn{handler: hb, done: make(chan struct{})}
	ca.peer, cb.peer = cb, ca
	return ca, cb
}

func (c *conn) Call(ctx context.Context, method string, params, result any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, jsonrpc.DefaultCallTimeout)
		defer cancel()
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	type answer struct {
		v   any
		err error
	}
	ch := make(chan answer, 1)
	go func() {
		h := c.peer.handler
		if h == nil {
			ch <- answer{err: &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}}
			return
		}
		v, err := h.Handle(ctx, &jsonrpc.Request{ID: json.RawMessage(`1`), Method: method, Params: raw})
		ch <- answer{v, err}
	}()
	select {
	case <-c.done:
		return jsonrpc.ErrClosed
	case <-c.peer.done:
		return jsonrpc.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case a := <-ch:
		if a.err != nil {
			var rpcErr *jsonrpc.Error
			if errors.As(a.err, &rpcErr) {
				return rpcErr
			}
			return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "internal error"}
		}
		if result == nil {
			return nil
		}
		out, err := json.Marshal(a.v)
		if err != nil {
			return err
		}
		return json.Unmarshal(out, result)
	}
}

func (c *conn) Notify(ctx context.Context, method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if h := c.peer.handler; h != nil {
		_, _ = h.Handle(ctx, &jsonrpc.Request{Method: method, Params: raw})
	}
	return nil
}

// Fail closes both ends with err, as a protocol error does.
func (c *conn) Fail(err error) {
	c.close(err)
	c.peer.close(err)
}

func (c *conn) close(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

func (c *conn) Close() error { c.Fail(nil); return nil }

func (c *conn) Done() <-chan struct{} { return c.done }

func (c *conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Child is a scripted process plugin: its Handler answers the host's
// requests, and it talks back through Host once launched.
type Child struct {
	// Handle answers host requests; nil answers initialize correctly and
	// everything else with {}.
	Handle func(c *Child, r *jsonrpc.Request) (any, error)
	// Host is the child's end of the connection, set at launch.
	Host jsonrpc.Conn
	Proc *Process
	// Subscribe, when set, is sent as subscribe once the host accepts
	// requests after initialize.
	Subscribe []string
}

// Process is a fake jsonrpc.Process running a Child in-process.
type Process struct {
	conn, child *conn
	once        sync.Once
	exited      chan struct{}
	mu          sync.Mutex
	code        int
	Spec        jsonrpc.LaunchSpec
}

// Exit ends the process with code (-1: killed by a signal).
func (p *Process) Exit(code int) {
	p.once.Do(func() {
		p.mu.Lock()
		p.code = code
		p.mu.Unlock()
		p.conn.Fail(nil)
		close(p.exited)
	})
}

// Break closes the connection with jsonrpc.ErrProtocol, as a malformed
// frame does. The process keeps running until the host kills it.
func (p *Process) Break() { p.conn.Fail(jsonrpc.ErrProtocol) }

func (p *Process) Conn() jsonrpc.Conn      { return p.conn }
func (p *Process) PID() int                { return 1 }
func (p *Process) Stop() error             { p.Exit(0); return nil }
func (p *Process) Kill() error             { p.Exit(-1); return nil }
func (p *Process) Exited() <-chan struct{} { return p.exited }
func (p *Process) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code
}

// Launcher launches Children made by New, recording every launch.
type Launcher struct {
	New func() *Child

	mu       sync.Mutex
	Launches []*Child
}

// Launch implements the plugin host's Launch option.
func (l *Launcher) Launch(_ context.Context, s jsonrpc.LaunchSpec) (jsonrpc.Process, error) {
	c := l.New()
	childHandler := jsonrpc.HandlerFunc(func(_ context.Context, r *jsonrpc.Request) (any, error) {
		if r.Method == plugin.MethodInitialize && len(c.Subscribe) > 0 {
			go c.subscribe() //nolint:gosec // the child's own calls outlive the initialize request

		}
		if c.Handle != nil {
			return c.Handle(c, r)
		}
		return Serve(c, r)
	})
	hostEnd, childEnd := Pipe(s.Handler, childHandler)
	p := &Process{conn: hostEnd.(*conn), child: childEnd.(*conn), exited: make(chan struct{}), Spec: s}
	c.Host, c.Proc = childEnd, p
	l.mu.Lock()
	l.Launches = append(l.Launches, c)
	l.mu.Unlock()
	return p, nil
}

// Count returns how many children were launched.
func (l *Launcher) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.Launches)
}

// Last returns the newest child.
func (l *Launcher) Last() *Child {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.Launches) == 0 {
		return nil
	}
	return l.Launches[len(l.Launches)-1]
}

// Serve is the well-behaved default: initialize with the right protocol,
// {} for everything else.
func Serve(_ *Child, r *jsonrpc.Request) (any, error) {
	if r.Method == plugin.MethodInitialize {
		return plugin.InitializeResult{ProtocolVersion: manifest.ProtocolVersion}, nil
	}
	return struct{}{}, nil
}

// subscribe retries until initialize has completed on the host.
func (c *Child) subscribe() {
	for i := 0; i < 1000; i++ {
		err := c.Host.Call(context.Background(), plugin.MethodSubscribe, plugin.SubscribeParams{Types: c.Subscribe}, nil)
		var rpcErr *jsonrpc.Error
		if !errors.As(err, &rpcErr) || !strings.Contains(string(rpcErr.Data), plugin.RefuseNotReady) {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
