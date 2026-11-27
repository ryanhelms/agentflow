package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/config"
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
// any case. Held calls do not survive a daemon restart.

// heldCall is one plugin write waiting for a person.
type heldCall struct {
	plugin   string
	command  string
	input    json.RawMessage
	cause    string
	target   Worktree
	approval string
	expires  *time.Time
}

// maxHeld bounds the held calls; the oldest is dropped past it.
const maxHeld = 256

// heldCalls are the Host's held calls, by approval ID.
type heldCalls struct {
	mu    sync.Mutex
	calls map[string]heldCall
	order []string
}

// pluginApp opens plugin id's App on target with the confirm ceiling.
func (h *Host) pluginApp(ctx context.Context, id string, target Worktree) (*app.App, error) {
	a, err := h.o.OpenApp(ctx, target.Root, OriginPrefix+id)
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

// claim removes and returns the held call of approval id.
func (h *Host) claim(id string) (heldCall, bool) {
	h.held.mu.Lock()
	defer h.held.mu.Unlock()
	c, ok := h.held.calls[id]
	if ok {
		delete(h.held.calls, id)
		for i, v := range h.held.order {
			if v == id {
				h.held.order = append(h.held.order[:i], h.held.order[i+1:]...)
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
func (h *Host) forget(id string) { h.claim(id) }

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
			var p bus.Approval
			if json.Unmarshal(e.Payload, &p) != nil || p.OperationID == "" {
				continue // an overflow marker or a malformed payload
			}
			if _, ok := h.peek(p.OperationID); ok {
				go h.settle(ctx, p.OperationID)
			}
		}
	}()
	return nil
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
	h.mu.Lock()
	_, running := h.runners[c.plugin]
	h.mu.Unlock()
	if !running {
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
	ctx, cancel := context.WithTimeout(ctx, InvokeTimeout)
	defer cancel()
	res := h.call(ctx, c.plugin, c.target, a, jc, c.input, c.approval, c.cause)
	h.logTo(c.plugin, "info", fmt.Sprintf("resumed %s after approval: %s", c.command, res.Outcome.Kind))
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
