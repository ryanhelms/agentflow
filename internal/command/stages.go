package command

import "sync"

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
