package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// pinger is implemented by Instances that have a process to ping (the
// process tier). The host pings it every PingInterval: 2 missed pings make
// the plugin degraded, 4 restart it.
type pinger interface {
	Ping(ctx context.Context) error
}

// runner drives one enabled plugin through its lifecycle (GoF State, with
// plugin.Transitions as the table) on its own goroutine.
type runner struct {
	h      *Host
	p      manifest.Installed
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	live   chan map[string]any

	mu       sync.Mutex
	current  map[string]any
	removed  bool
	restarts []time.Time
}

func newRunner(h *Host, parent context.Context, p manifest.Installed, settings map[string]any) *runner {
	ctx, cancel := context.WithCancel(parent)
	return &runner{h: h, p: p, ctx: ctx, cancel: cancel, done: make(chan struct{}), live: make(chan map[string]any, 1), current: settings}
}

func (r *runner) settings() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

// updateSettings hands live settings to the running plugin.
func (r *runner) updateSettings(s map[string]any) {
	r.mu.Lock()
	r.current = s
	r.mu.Unlock()
	select {
	case <-r.live:
	default:
	}
	r.live <- s
}

// stop ends the runner and waits for it: the plugin ends in stopped, or in
// removed when removed is true.
func (r *runner) stop(removed bool) {
	r.mu.Lock()
	r.removed = removed
	r.mu.Unlock()
	r.cancel()
	<-r.done
}

func (r *runner) to(state bus.PluginState, code string, retryAt *time.Time) bool {
	return r.h.transition(r.p, state, code, retryAt)
}

// run is the lifecycle loop: starting (verify, start), running, then
// backoff and a restart, or failed.
func (r *runner) run() {
	defer close(r.done)
	code := bus.PluginCodeEnabled
	for {
		if r.ctx.Err() != nil {
			r.finish()
			return
		}
		r.to(bus.PluginStarting, code, nil)
		code = ""
		if err := r.h.o.Source.Verify(r.ctx, r.p); err != nil {
			reason := bus.PluginCodeManifestInvalid
			if errors.Is(err, manifest.ErrHashMismatch) {
				reason = bus.PluginCodeHashMismatch
			}
			r.failAndWait(reason)
			return
		}
		started := r.h.o.Now()
		err := r.serve()
		if r.ctx.Err() != nil {
			r.finish()
			return
		}
		if fatal(err) {
			r.failAndWait(Code(err))
			return
		}
		n := r.restart(started)
		if n >= CrashLoopCount {
			r.failAndWait(bus.PluginCodeCrashLoop)
			return
		}
		delay := backoff(n)
		retry := r.h.o.Now().Add(delay).UTC()
		r.to(bus.PluginBackoff, Code(err), &retry)
		select {
		case <-r.ctx.Done():
			r.finish()
			return
		case <-r.h.o.After(delay):
		}
	}
}

// restart records a restart and returns how many fall in the crash-loop
// window. A run that lasted the whole window resets the count.
func (r *runner) restart(started time.Time) int {
	now := r.h.o.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Sub(started) >= CrashLoopWindow {
		r.restarts = nil
	}
	r.restarts = append(r.restarts, now)
	r.restarts = slices.DeleteFunc(r.restarts, func(t time.Time) bool { return now.Sub(t) >= CrashLoopWindow })
	n := len(r.restarts)
	r.h.update(r.p.ID, func(st *Status) { st.Restarts = n })
	return n
}

// failAndWait moves the plugin to failed and waits there (failed is
// sticky) until the runner is stopped.
func (r *runner) failAndWait(code string) {
	r.to(bus.PluginFailed, code, nil)
	<-r.ctx.Done()
	r.finish()
}

// finish moves the plugin through stopping to stopped or removed.
func (r *runner) finish() {
	r.mu.Lock()
	removed := r.removed
	r.mu.Unlock()
	end, code := bus.PluginStopped, bus.PluginCodeDisabled
	if removed {
		end, code = bus.PluginRemoved, ""
	}
	r.to(bus.PluginStopping, code, nil)
	if !r.to(end, code, nil) && removed {
		r.to(bus.PluginRemoved, "", nil) // from installed or stopped
	}
}

// serve starts one instance and delivers events to it until the runner
// stops (nil) or the instance ends (its error, with a code).
func (r *runner) serve() error {
	f, ok := r.h.factories[r.p.Tier]
	if !ok {
		return coded(errNoFactory, bus.PluginCodeManifestInvalid, true)
	}
	inst, err := f.New(r.p)
	if err != nil {
		if Code(err) == ReasonRPCError {
			err = coded(err, bus.PluginCodeLaunch, false)
		}
		return err
	}
	data := filepath.Join(r.h.o.Home, manifest.PluginsDir, r.p.ID, manifest.DataDir)
	if err := os.MkdirAll(data, 0o700); err != nil {
		return coded(err, bus.PluginCodeLaunch, false)
	}
	env := newEnv(r, newPluginLog(filepath.Join(data, "plugin.log"), r.h.o.Now))
	startCtx, cancelStart := context.WithTimeout(r.ctx, r.h.o.InitializeTimeout)
	err = inst.Start(startCtx, env)
	cancelStart()
	if err != nil {
		stopInstance(inst)
		if Code(err) == ReasonRPCError {
			err = coded(err, bus.PluginCodeLaunch, false)
		}
		return err
	}
	sub := r.subscribe()
	defer sub.Close()
	r.to(bus.PluginRunning, "", nil)

	health, cancelHealth := context.WithCancel(r.ctx)
	defer cancelHealth()
	kill := make(chan struct{}, 1)
	if p, ok := inst.(pinger); ok {
		go r.ping(health, p, kill)
	}
	for {
		select {
		case <-r.ctx.Done():
			stopInstance(inst)
			return nil
		case <-inst.Done():
			if err := inst.Err(); err != nil {
				return err
			}
			return coded(nil, bus.PluginCodeExited, false)
		case <-kill:
			stopInstance(inst)
			return coded(nil, bus.PluginCodePing, false)
		case s := <-r.live:
			env.setSettings(s)
			ctx, cancel := context.WithTimeout(r.ctx, r.h.o.DeliverTimeout)
			_ = inst.Settings(ctx, s)
			cancel()
		case e, ok := <-sub.Events():
			if !ok {
				stopInstance(inst)
				return nil
			}
			r.deliver(inst, env, sub, e)
		}
	}
}

func stopInstance(inst Instance) {
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout+time.Second)
	defer cancel()
	_ = inst.Stop(ctx)
}

// subscribe opens the plugin's own bus subscription over every pattern it
// was granted; env filters each event against what it accepted.
func (r *runner) subscribe() bus.Subscription {
	var topics []bus.Topic
	for _, g := range r.p.Manifest.Permissions.Subscribe {
		if pat, err := bus.ParsePattern(g); err == nil && subscribable(pat) {
			topics = append(topics, bus.Topic{Repo: bus.Any, Worktree: bus.Any, Types: pat})
		}
	}
	if len(topics) == 0 {
		return idleSubscription{}
	}
	sub, err := r.h.o.Bus.Subscribe(bus.SubscribeOptions{Name: OriginPrefix + r.p.ID, Buffer: SubscriptionBuffer}, topics...)
	if err != nil {
		return idleSubscription{}
	}
	return sub
}

// deliver hands one event to the instance, under DeliverTimeout. A failure
// sends the event to the Dead Letter Channel.
func (r *runner) deliver(inst Instance, env *env, sub bus.Subscription, e bus.Event) {
	if e.Type == bus.TypeStreamOverflow {
		dropped := sub.Dropped()
		r.h.update(r.p.ID, func(st *Status) { st.Dropped = dropped })
		ctx, cancel := context.WithTimeout(r.ctx, r.h.o.DeliverTimeout)
		_ = inst.Deliver(ctx, e) // an overflow marker is never a dead letter
		cancel()
		return
	}
	if !env.accepts(e) {
		return
	}
	env.remember(e)
	ctx, cancel := context.WithTimeout(r.ctx, r.h.o.DeliverTimeout)
	err := inst.Deliver(ctx, e)
	cancel()
	if err == nil {
		r.h.update(r.p.ID, func(st *Status) { st.Delivered++ })
		return
	}
	if r.ctx.Err() != nil {
		return // the host is stopping the plugin
	}
	code := Code(err)
	select {
	case <-inst.Done():
		if c := Code(inst.Err()); c != "" {
			code = c // it died with the event in flight
		}
	default:
	}
	r.h.dead(r.p.ID, e, code)
	if _, ok := inst.(pinger); ok && code == bus.PluginCodeTimeout {
		r.to(bus.PluginDegraded, bus.PluginCodeTimeout, nil)
	}
}

// ping pings a process plugin until ctx ends. 2 missed pings in a row make
// it degraded, 4 ask serve to restart it; an answer makes it running again.
func (r *runner) ping(ctx context.Context, p pinger, kill chan<- struct{}) {
	missed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.h.o.After(r.h.o.PingInterval):
		}
		pctx, cancel := context.WithTimeout(ctx, r.h.o.PingTimeout)
		err := p.Ping(pctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			if r.h.state(r.p.ID) == bus.PluginDegraded {
				r.to(bus.PluginRunning, "", nil)
			}
			missed = 0
			continue
		}
		missed++
		if missed == MissedPingsDegraded {
			r.to(bus.PluginDegraded, bus.PluginCodePing, nil)
		}
		if missed >= MissedPingsRestart {
			select {
			case kill <- struct{}{}:
			default:
			}
			return
		}
	}
}

// idleSubscription is the subscription of a plugin granted nothing to
// receive.
type idleSubscription struct{}

func (idleSubscription) Events() <-chan bus.Event { return nil }
func (idleSubscription) Dropped() uint64          { return 0 }
func (idleSubscription) Close() error             { return nil }
