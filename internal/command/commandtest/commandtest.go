// Package commandtest has fakes for the command contract: a recording
// Emitter and Auditor, a reference Invoker that follows the ADR's skeleton
// for GateInRun operations, and a CLIEnv that writes to a buffer. Lanes code
// and test against them until lane 3.1's NewInvoker and production Emitter
// land; TestEveryWriteOperationEmitsEvents (lane 3.4) uses them too.
package commandtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
)

// Repo and Worktree are what Emitter fills in.
const (
	Repo     bus.RepoID     = "0123456789abcdef"
	Worktree bus.WorktreeID = "main"
)

// Emitter records events, filling Repo and Worktree, and validates each one
// as the bus would.
type Emitter struct {
	mu     sync.Mutex
	events []bus.Event
	// Err, when set, is returned by Emit after recording.
	Err error
}

// Emit implements command.Emitter.
func (e *Emitter) Emit(_ context.Context, ev bus.Event, worktree bool) error {
	ev.Repo = Repo
	if worktree {
		ev.Worktree = Worktree
	}
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("invalid op event: %w", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	return e.Err
}

// Source returns an EmitterSource that always gives e.
func (e *Emitter) Source() command.EmitterSource {
	return func(context.Context, *app.App) (command.Emitter, error) { return e, nil }
}

// Events returns the recorded events.
func (e *Emitter) Events() []bus.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]bus.Event(nil), e.events...)
}

// Types returns the recorded event types, in order.
func (e *Emitter) Types() []bus.Type {
	var out []bus.Type
	for _, ev := range e.Events() {
		out = append(out, ev.Type)
	}
	return out
}

// Payload decodes ev's payload.
func Payload(ev bus.Event) (bus.OpPayload, error) {
	var p bus.OpPayload
	err := json.Unmarshal(ev.Payload, &p)
	return p, err
}

// Audit is one recorded audit line.
type Audit struct {
	Invocation command.Invocation
	Outcome    command.Outcome
}

// Auditor records AuditOperation calls.
type Auditor struct {
	mu    sync.Mutex
	lines []Audit
}

// AuditOperation implements command.Auditor.
func (a *Auditor) AuditOperation(_ *app.App, inv command.Invocation, out command.Outcome) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lines = append(a.lines, Audit{inv, out})
}

// Lines returns the recorded lines.
func (a *Auditor) Lines() []Audit {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Audit(nil), a.lines...)
}

// ErrGateByInvoker is returned by Invoker for GateByInvoker operations,
// which need (*app.App).GateOperation from lane 3.1.
var ErrGateByInvoker = errors.New("commandtest: GateByInvoker needs lane 3.1's NewInvoker")

// Invoker is a reference implementation of the ADR's Invoker skeleton for
// GateInRun and read-only operations: validate, op.requested, run, audit,
// outcome event. It returns Run's result unchanged.
type Invoker struct {
	Options command.Options
}

// Invoke implements command.Invoker.
func (inv Invoker) Invoke(ctx context.Context, c *command.Call) (any, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Meta.Gate == command.GateByInvoker {
		return nil, ErrGateByInvoker
	}
	now := inv.Options.Now
	if now == nil {
		now = time.Now
	}
	var em command.Emitter
	if c.Publishes() && inv.Options.Emitters != nil {
		em, _ = inv.Options.Emitters(ctx, c.App)
	}
	worktree := c.Meta.Events == command.ScopeWorktree
	var requested string
	if em != nil {
		if ev, err := c.RequestedEvent(now()); err == nil && em.Emit(ctx, ev, worktree) == nil {
			requested = ev.ID
		}
	}
	next := func(ctx context.Context, c *command.Call) (any, error) { return c.Run(ctx) }
	for i := len(inv.Options.Stages) - 1; i >= 0; i-- {
		stage, rest := inv.Options.Stages[i], next
		next = func(ctx context.Context, c *command.Call) (any, error) { return stage.Handle(ctx, c, rest) }
	}
	v, err := next(ctx, c)
	if c.DryRun || c.Meta.Events == command.ScopeNone {
		return v, err
	}
	out := c.Classify(v, err)
	if inv.Options.Auditor != nil {
		inv.Options.Auditor.AuditOperation(c.App, c.Invocation, out)
	}
	if em != nil {
		if ev, e := c.OutcomeEvent(out, requested, now()); e == nil {
			_ = em.Emit(ctx, ev, worktree)
		}
	}
	return v, err
}

// Env is a CLIEnv that writes to Out. App is returned by Open; Answer
// decides the confirmation prompt.
type Env struct {
	App    *app.App
	Answer bool
	Out    bytes.Buffer
	// Tokens records the token of each run of the confirmation flow.
	Tokens []string
}

// Open implements command.CLIEnv.
func (e *Env) Open(context.Context) (*app.App, error) { return e.App, nil }

// Origin implements command.CLIEnv.
func (e *Env) Origin(*app.App) command.Origin { return command.Origin{Source: "cli", Client: "cli"} }

// Confirm implements command.CLIEnv like cli.confirmLoop, without a
// terminal: on needs_confirmation it runs again with the token when Answer
// or yes is set.
func (e *Env) Confirm(yes bool, fn func(token string) (any, error)) error {
	e.Tokens = append(e.Tokens, "")
	out, err := fn("")
	if err != nil {
		return err
	}
	if p, ok := out.(*app.Pending); ok && p.Status == "needs_confirmation" {
		if !yes && !e.Answer {
			return errors.New("cancelled")
		}
		e.Tokens = append(e.Tokens, p.ConfirmToken)
		if out, err = fn(p.ConfirmToken); err != nil {
			return err
		}
	}
	return e.Print(out, nil)
}

// Print implements command.CLIEnv like cli.writeResult.
func (e *Env) Print(v any, err error) error {
	if err != nil {
		return err
	}
	enc := json.NewEncoder(&e.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
