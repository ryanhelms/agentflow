package command

import (
	"context"
	"errors"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// NewInvoker returns the production Invoker: the fixed skeleton described on
// Invoker (validate, stages, op.requested, gate, run, audit, outcome event).
// It returns Run's value and error unchanged, or the gate's Pending for a
// GateByInvoker operation that may not proceed yet.
func NewInvoker(o Options) Invoker {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &invoker{o: o}
}

type invoker struct{ o Options }

// errNoPlan: a GateByInvoker call without a Plan or an App. Register
// refuses such a Spec, so only a hand-built Call can reach it.
var errNoPlan = errors.New("command: GateByInvoker needs Plan and an App")

// Invoke implements Invoker.
func (inv *invoker) Invoke(ctx context.Context, c *Call) (any, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	stages := make([]Stage, 0, len(inv.o.Stages)+len(RegisteredStages()))
	stages = append(stages, inv.o.Stages...)
	stages = append(stages, RegisteredStages()...)
	next := Handler(inv.operate)
	for i := len(stages) - 1; i >= 0; i-- {
		stage, rest := stages[i], next
		next = func(ctx context.Context, c *Call) (any, error) { return stage.Handle(ctx, c, rest) }
	}
	return next(ctx, c)
}

// operate is the part of the skeleton after the stages: it publishes,
// gates, runs and audits.
func (inv *invoker) operate(ctx context.Context, c *Call) (any, error) {
	var em Emitter
	if c.Publishes() && inv.o.Emitters != nil {
		em, _ = inv.o.Emitters(ctx, c.App)
	}
	worktree := c.Meta.Events == ScopeWorktree
	var requested string
	publishRequested := func(at time.Time) {
		if em == nil {
			return
		}
		ev, err := c.RequestedEvent(at)
		if err != nil {
			return
		}
		if em.Emit(ctx, ev, worktree) == nil {
			requested = ev.ID
		}
	}
	if !c.Meta.NativeReads {
		publishRequested(inv.o.Now())
	}
	v, err := inv.gateAndRun(ctx, c)
	if c.DryRun || c.Meta.Class == policy.Read || c.Meta.Class == "" {
		return v, err
	}
	out := c.Classify(v, err)
	if !out.Kind.Publishes() {
		return v, err
	}
	if c.Meta.NativeReads {
		publishRequested(c.Started)
	}
	if c.Meta.AuditByInvoker && inv.o.Auditor != nil {
		inv.o.Auditor.AuditOperation(c.App, c.Invocation, out)
	}
	if em != nil {
		if ev, e := c.OutcomeEvent(out, requested, inv.o.Now()); e == nil {
			_ = em.Emit(ctx, ev, worktree)
		}
	}
	return v, err
}

// gateAndRun gates a GateByInvoker call through GateOperation, then runs
// it on proceed. Every other call runs at once: a GateInRun app method gates
// itself.
func (inv *invoker) gateAndRun(ctx context.Context, c *Call) (any, error) {
	if c.Meta.Gate != GateByInvoker {
		return c.Run(ctx)
	}
	if c.Plan == nil || c.App == nil {
		return nil, errNoPlan
	}
	plan, err := c.Plan(ctx)
	if err != nil {
		return nil, err
	}
	p, err := c.App.GateOperation(app.OperationGate{
		Class: c.Meta.Class, Action: c.Meta.Action, Related: c.related, Summary: plan.Summary, Commands: plan.Commands,
		Token: c.token, Force: plan.Force, PrepareOnly: c.Meta.PrepareOnly, Bounds: plan.Bounds,
	})
	if err != nil {
		return nil, err
	}
	if p != nil {
		return p, nil
	}
	return c.Run(ctx)
}

// AuditLog is the production Auditor: it writes the call's line through the
// App's gate (policy.Gate.AuditOperation), next to the app methods' own lines
// in audit.log.
type AuditLog struct{}

// AuditOperation implements Auditor.
func (AuditLog) AuditOperation(a *app.App, inv Invocation, out Outcome) {
	if a == nil || a.Gate == nil {
		return
	}
	a.Gate.AuditOperation(policy.OperationAudit{
		Class: inv.Meta.Class, Action: inv.Meta.Action, Operation: inv.Meta.Name,
		OperationID: out.operationID(inv.ID), Outcome: string(out.Kind), Code: out.Code,
	})
}

// operationID is OperationID for an Invocation's ID.
func (o Outcome) operationID(id string) string {
	if o.ApprovalID != "" {
		return o.ApprovalID
	}
	return id
}
