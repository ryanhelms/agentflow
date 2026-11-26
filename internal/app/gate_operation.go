package app

import (
	"time"

	"github.com/ryanhelms/agentflow/internal/policy"
)

// OperationGate is the gate input of an operation that the command Invoker
// gates (command.GateByInvoker). It mirrors the arguments of a.gate: the
// Invoker fills it from the Spec's Policy and Plan and the call's confirm
// token.
type OperationGate struct {
	Class   policy.Class
	Action  string
	Related []string
	// Summary is the exact reviewed payload, bound into the approval
	// fingerprint.
	Summary any
	// Commands are suggested when the policy is suggest.
	Commands []string
	// Token is the call's confirm token, or "".
	Token string
	// Force redeems Token even when the policy is now auto.
	Force bool
	// PrepareOnly prepares an exact candidate without applying it, as
	// prepareWithActions does for commit.prepare and push.prepare.
	PrepareOnly bool
	// Bounds cap the approval's expiry.
	Bounds []time.Time
}

// GateOperation gates an operation for the command Invoker, through the same
// checks as a.gate and prepareWithActions. A nil Pending and nil error mean
// proceed: the Invoker then runs the operation.
func (a *App) GateOperation(g OperationGate) (*Pending, error) {
	if g.PrepareOnly {
		return a.prepareWithActions(g.Class, g.Action, g.Related, g.Summary, g.Commands)
	}
	return a.gateWithActions(g.Class, g.Action, g.Related, g.Summary, g.Token, g.Force, g.Commands, g.Bounds...)
}
