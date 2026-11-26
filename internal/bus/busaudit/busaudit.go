// Package busaudit connects the bus's audit Wire Tap to policy.Gate.Audit.
// It lives apart from package bus so that bus never imports internal/policy
// and policy may later publish events without an import cycle.
package busaudit

import (
	"context"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// Result is the result recorded in the audit log for a published event.
const Result = "published"

// Recorder is the part of policy.Gate the tap uses.
type Recorder interface {
	Audit(class policy.Class, action string, payload any, result string)
}

var _ Recorder = (*policy.Gate)(nil)

// Auditor returns a bus.Auditor that writes each event as one audit line:
// class local, the event type as the action and the whole envelope as the
// payload (the log keeps only its hash).
func Auditor(r Recorder) bus.Auditor {
	return bus.AuditorFunc(func(_ context.Context, e bus.Event) {
		r.Audit(policy.Local, string(e.Type), e, Result)
	})
}

// Tap returns the audit Wire Tap over r: bus.AuditTap(Auditor(r)). Add it
// to bus.Options.Taps.
func Tap(r Recorder) bus.Tap { return bus.AuditTap(Auditor(r)) }
