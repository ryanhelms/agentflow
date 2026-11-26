package bus

import (
	"context"
	"strings"
)

// AuditedFamilies are the event families the audit tap records. They are
// actions; every other family (git.*, config.*, stream.*) is an observation
// and is not audited. approval.* is not audited either: the gate's own
// approval journal already records every transition (ADR, phase 2
// decision 6). op.* is not audited by the tap either: the command
// Invoker's audit stage writes one line per operation in the calling
// process, with its client, and the tap would add a second line per event
// in the daemon (ADR, phase 3 decision 4). So the tap is wired but records
// nothing until a later phase adds an audited family.
var AuditedFamilies []string

// Audited reports whether the audit tap records events of type t.
func Audited(t Type) bool {
	family, _, _ := strings.Cut(string(t), ".")
	for _, f := range AuditedFamilies {
		if family == f {
			return true
		}
	}
	return false
}

// Auditor writes one audited event to the audit log. Package
// internal/bus/busaudit adapts policy.Gate.Audit to it, so this package does
// not depend on internal/policy.
type Auditor interface {
	AuditEvent(ctx context.Context, e Event)
}

// AuditorFunc adapts a function to Auditor.
type AuditorFunc func(ctx context.Context, e Event)

// AuditEvent calls f.
func (f AuditorFunc) AuditEvent(ctx context.Context, e Event) { f(ctx, e) }

// AuditTap returns the Wire Tap that hands each Audited event to a.
func AuditTap(a Auditor) Tap {
	return TapFunc(func(ctx context.Context, e Event) {
		if Audited(e.Type) {
			a.AuditEvent(ctx, e)
		}
	})
}
