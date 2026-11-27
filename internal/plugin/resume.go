package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// The host resumes a plugin's held write itself once a person approves it
// (ADR, "Phase 5 approving a plugin's write"). A declarative plugin never
// resumes, and a process plugin need not: the write happens with no other
// action, exactly once.
//
// The host keeps each held call in memory: the plugin, the command, its exact
// input (the approval's fingerprint binds it), the cause and the target. It
// watches approval.resolved and approval.expired on the bus itself; plugins
// never receive them. On each, it reads the record, the source of truth,
// through the plugin's own gate: approved resumes the call, pending keeps it,
// anything else drops it. A held call is claimed before it runs, so a
// repeated event cannot run it twice, and the gate consumes the approval in
// any case. When the plugin resumes the call itself, whichever of the two
// claims it first runs it; the other runs nothing and publishes nothing
// (CodeResumedByHost). Before it runs a call the host checks the plugin
// again: the same installed version and hash that asked, still running,
// still granted the command. When its approval subscription overflows it
// settles every held call. Held calls do not survive a daemon restart. Stop
// waits for a resume in flight (Host.drain) and no resume starts after it.

// heldCall is one plugin write waiting for a person.
type heldCall struct {
	plugin   string
	version  string // the installed version and hash that asked
	hash     string
	command  string
	input    json.RawMessage
	cause    string
	target   Worktree
	approval string
	expires  *time.Time
}

// maxHeld bounds the held calls; the oldest is dropped past it.
const maxHeld = 256

// heldCalls are the Host's held calls, by approval ID, and the approval IDs
// the host claimed to resume itself (at most maxHeld, oldest dropped).
type heldCalls struct {
	mu      sync.Mutex
	calls   map[string]heldCall
	order   []string
	resumed map[string]bool
	rsOrder []string
}

// pluginApp opens plugin id's App on target with the confirm ceiling.
func (h *Host) pluginApp(ctx context.Context, id string, target Worktree) (*app.App, error) {
	a, err := h.o.OpenApp(ctx, target.Root, id)
	if err != nil {
		return nil, err
	}
	return a.WithAutonomyCeiling(config.Confirm), nil
}

// call runs one plugin command through the Invoker under origin plugin:<id>.
// A panic in it is the plugin's failure, never the daemon's.
func (h *Host) call(ctx context.Context, id string, w Worktree, a *app.App, jc command.JSONCommand, input json.RawMessage, resume, cause string) (res command.JSONResult) {
	client := OriginPrefix + id
	origin := command.Origin{Source: client, Client: client, Cause: cause}
	res = jc.InvokeJSON(ctx, h.invoker(w), a, input, resume, origin)
	if !errors.Is(res.Err, command.ErrConfirmTokenInput) && !errors.Is(res.Err, command.ErrJSONInput) && !errors.Is(res.Err, command.ErrNoResume) {
		h.update(id, func(st *Status) { st.Invocations++ })
	}
	return res
}

// hold remembers c until its approval is answered.
func (h *Host) hold(c heldCall) {
	h.held.mu.Lock()
	defer h.held.mu.Unlock()
	if h.held.calls == nil {
		h.held.calls = map[string]heldCall{}
	}
	if _, ok := h.held.calls[c.approval]; !ok {
		h.held.order = append(h.held.order, c.approval)
	}
	h.held.calls[c.approval] = c
	for len(h.held.order) > maxHeld {
		delete(h.held.calls, h.held.order[0])
		h.held.order = h.held.order[1:]
	}
}

// claim removes and returns the held call of approval id, for the host to
// resume: a later takeForPlugin of id answers that the host has it.
func (h *Host) claim(id string) (heldCall, bool) {
	h.held.mu.Lock()
	defer h.held.mu.Unlock()
	c, ok := h.held.remove(id)
	if ok {
		if h.held.resumed == nil {
			h.held.resumed = map[string]bool{}
		}
		h.held.resumed[id] = true
		h.held.rsOrder = append(h.held.rsOrder, id)
		for len(h.held.rsOrder) > maxHeld {
			delete(h.held.resumed, h.held.rsOrder[0])
			h.held.rsOrder = h.held.rsOrder[1:]
		}
	}
	return c, ok
}

// takeForPlugin is plugin id resuming approval op itself. It reports
// whether the host already claimed op; otherwise it drops op's held call,
// so the host never runs it.
func (h *Host) takeForPlugin(id, op string) (hostHasIt bool) {
	h.held.mu.Lock()
	defer h.held.mu.Unlock()
	if h.held.resumed[op] {
		return true
	}
	if c, ok := h.held.calls[op]; ok && c.plugin == id {
		h.held.remove(op)
	}
	return false
}

// remove deletes held call id; the caller holds mu.
func (hc *heldCalls) remove(id string) (heldCall, bool) {
	c, ok := hc.calls[id]
	if ok {
		delete(hc.calls, id)
		for i, v := range hc.order {
			if v == id {
				hc.order = append(hc.order[:i], hc.order[i+1:]...)
				break
			}
		}
	}
	return c, ok
}

func (h *Host) peek(id string) (heldCall, bool) {
	h.held.mu.Lock()
	defer h.held.mu.Unlock()
	c, ok := h.held.calls[id]
	return c, ok
}

// forget drops the held call of approval id, if any.
func (h *Host) forget(id string) {
	h.held.mu.Lock()
	defer h.held.mu.Unlock()
	h.held.remove(id)
}

// Held returns the approval IDs of the held calls, oldest first.
func (h *Host) Held() []string {
	h.held.mu.Lock()
	defer h.held.mu.Unlock()
	return append([]string(nil), h.held.order...)
}

// watchApprovals follows the approval events of every repository until ctx
// ends, and settles the held call each one names.
func (h *Host) watchApprovals(ctx context.Context) error {
	if h.o.Bus == nil {
		return nil
	}
	sub, err := h.o.Bus.Subscribe(bus.SubscribeOptions{Name: "plugin-host/approvals", Buffer: 256},
		bus.Topic{Repo: bus.Any, Worktree: bus.Any, Types: bus.Pattern(bus.TypeApprovalResolved)},
		bus.Topic{Repo: bus.Any, Worktree: bus.Any, Types: bus.Pattern(bus.TypeApprovalExpired)})
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = sub.Close()
	}()
	go func() {
		for e := range sub.Events() {
			h.approvalEvent(ctx, e)
		}
	}()
	return nil
}

// approvalEvent settles the held call approval event e names. On an
// overflow marker events were dropped, so any held call may have been
// answered: it settles every one.
func (h *Host) approvalEvent(ctx context.Context, e bus.Event) {
	if e.Type == bus.TypeStreamOverflow {
		for _, id := range h.Held() {
			h.settleSoon(ctx, id)
		}
		return
	}
	var p bus.Approval
	if json.Unmarshal(e.Payload, &p) != nil || p.OperationID == "" {
		return // a malformed payload
	}
	if _, ok := h.peek(p.OperationID); ok {
		h.settleSoon(ctx, p.OperationID)
	}
}

// settleSoon runs settle on its own goroutine, which Stop waits for. After
// Stop it does nothing.
func (h *Host) settleSoon(ctx context.Context, id string) {
	done, ok := h.begin()
	if !ok {
		return
	}
	go func() {
		defer done()
		h.settle(ctx, id)
	}()
}

// settle reads held call id's approval and resumes it when approved, keeps
// it while pending, and drops it otherwise.
func (h *Host) settle(ctx context.Context, id string) {
	c, ok := h.peek(id)
	if !ok {
		return
	}
	if c.expires != nil && !h.o.Now().Before(*c.expires) {
		h.forget(id)
		return
	}
	a, err := h.pluginApp(ctx, c.plugin, c.target)
	if err != nil || a.Gate == nil {
		return // try again on the next event
	}
	ap, err := a.Gate.Status(id)
	switch {
	case err != nil:
		h.forget(id)
		return
	case ap.State == approvalPending:
		return
	case ap.State != "approved":
		h.forget(id) // declined, expired or used
		return
	}
	if c, ok = h.claim(id); !ok {
		return // another event got there first
	}
	h.resume(ctx, c, a)
}

// resume runs held call c with its approval. It drops it when the plugin was
// turned off, removed or disabled by the repository in the meantime.
func (h *Host) resume(ctx context.Context, c heldCall, a *app.App) {
	defer func() {
		if p := recover(); p != nil {
			h.logTo(c.plugin, "error", fmt.Sprintf("resuming %s panicked", c.command))
		}
	}()
	if why := h.stillAllowed(c); why != "" {
		h.logTo(c.plugin, "warn", fmt.Sprintf("did not resume %s after approval: %s", c.command, why))
		return
	}
	if _, err := h.target(c.plugin, c.target.Repo, c.target.ID); err != nil {
		return
	}
	cmd, ok := h.o.Commands(c.command)
	if !ok {
		return
	}
	jc, ok := cmd.(command.JSONCommand)
	if !ok {
		return
	}
	if ctx.Err() != nil {
		return // the daemon is stopping: start nothing
	}
	ctx, release := h.approvedWrite(ctx)
	defer release()
	ctx, cancel := context.WithTimeout(ctx, InvokeTimeout)
	defer cancel()
	res := h.call(ctx, c.plugin, c.target, a, jc, c.input, c.approval, c.cause)
	h.logTo(c.plugin, "info", fmt.Sprintf("resumed %s after approval: %s", c.command, res.Outcome.Kind))
}

// stillAllowed is why held call c may no longer run, or "": the plugin must
// still run as the installed version and hash that asked, and may still
// invoke c's command.
func (h *Host) stillAllowed(c heldCall) string {
	h.mu.Lock()
	r := h.runners[c.plugin]
	h.mu.Unlock()
	switch {
	case r == nil:
		return "the plugin is not running"
	case r.p.Version != c.version || r.p.Hash != c.hash:
		return "the plugin was replaced since it asked"
	case !slices.Contains(manifest.InvocableCommands, c.command):
		return "no plugin may invoke this command"
	case !slices.Contains(r.p.Manifest.Permissions.Invoke, c.command):
		return "the plugin is no longer granted this command"
	}
	return ""
}

// logTo writes to plugin id's log when it is running.
func (h *Host) logTo(id, level, message string) {
	h.mu.Lock()
	r := h.runners[id]
	h.mu.Unlock()
	if r == nil {
		return
	}
	if e := r.currentEnv(); e != nil {
		e.Log(level, message)
	}
}
