package policy

import "time"

// OperationAudit is the audit line of one operation run through the command
// Invoker (internal/command) whose app method writes no audit line of its own
// (Policy.AuditByInvoker). It names the operation and its operation ID, so
// the console timeline can link to the line. It never carries the input, a
// token or error text: Code is a fixed identifier.
type OperationAudit struct {
	Class       Class
	Action      string
	Operation   string // the command.Spec name, e.g. "approval.approve"
	OperationID string // "op_" and 32 hex characters
	Outcome     string // applied, failed, suggested, awaiting_approval, prepared
	Code        string // set when Outcome is failed
}

// AuditOperation appends one line for o to audit.log: time, client, class,
// action, operation, operation ID, outcome and code. Like Audit, it is best
// effort and never fails the operation.
func (g *Gate) AuditOperation(o OperationAudit) {
	now := time.Now
	if g.now != nil {
		now = g.now
	}
	entry := map[string]any{
		"time": now().UTC().Format(time.RFC3339), "client": g.Client, "class": o.Class, "action": o.Action,
		"operation": o.Operation, "operation_id": o.OperationID, "result": o.Outcome,
	}
	if o.Code != "" {
		entry["code"] = o.Code
	}
	g.appendAudit(entry)
}
