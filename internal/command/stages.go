package command

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Registered Invoker stages (ADR, "Phase 5 permission rules"). A package
// that must run in every Invoker — the plugin host's permission_rule Stage —
// registers its Stage from init with RegisterStage. NewInvoker runs
// RegisteredStages after Options.Stages on every call, so the CLI, MCP and
// daemon Invokers all apply it without each caller listing it.

var registered struct {
	mu     sync.RWMutex
	stages []Stage
}

// RegisterStage adds s to the stages every Invoker runs. Call it from init.
// A nil s is ignored.
func RegisterStage(s Stage) {
	if s == nil {
		return
	}
	registered.mu.Lock()
	defer registered.mu.Unlock()
	registered.stages = append(registered.stages, s)
}

// RegisteredStages returns the registered stages, in registration order.
func RegisteredStages() []Stage {
	registered.mu.RLock()
	defer registered.mu.RUnlock()
	return append([]Stage(nil), registered.stages...)
}

// Legacy returns the Meta of the registered operation whose CLI path is the
// longest prefix of args, among the operations without a CLI binding of
// their own: their command line still runs a handler in internal/cli that
// does not go through an Invoker.
func (r *Registry) Legacy(args []string) (Meta, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var best Meta
	found := false
	for _, c := range r.byName {
		m := c.Meta()
		if m.OwnCLI || len(m.CLIPath) == 0 || len(m.CLIPath) > len(args) || found && len(m.CLIPath) <= len(best.CLIPath) {
			continue
		}
		if slices.Equal(m.CLIPath, args[:len(m.CLIPath)]) {
			best, found = m, true
		}
	}
	return best, found
}

// CheckStages runs only the registered stages for an operation that a
// legacy CLI handler runs itself, so a plugin's permission rule refuses it
// there as it does through an Invoker. It returns the stage's refusal, or
// nil when every stage passed the call on.
func CheckStages(ctx context.Context, m Meta, o Origin, d Detail) error {
	c := &Call{Detail: d}
	c.Meta, c.Origin, c.Started = m, o, time.Now()
	c.Validate = func() error { return nil }
	c.Run = func(context.Context) (any, error) { return nil, nil }
	next := Handler(func(context.Context, *Call) (any, error) { return nil, nil })
	stages := RegisteredStages()
	for i := len(stages) - 1; i >= 0; i-- {
		stage, rest := stages[i], next
		next = func(ctx context.Context, c *Call) (any, error) { return stage.Handle(ctx, c, rest) }
	}
	_, err := next(ctx, c)
	return err
}
