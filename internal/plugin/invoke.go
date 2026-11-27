package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// Invoke implements Env: command.invoke and the declarative invoke action
// (ADR, "Phase 5 command invocation"). A plugin acts only by sending a
// Command to the Invoker: under origin plugin:<id>, with a confirm
// ceiling, and never holding a confirm token. The returned error is a
// *Refusal; how an admitted call ended is in the result.
func (e *env) Invoke(ctx context.Context, r InvokeRequest) (InvokeResult, error) {
	if !slices.Contains(manifest.InvocableCommands, r.Command) {
		return InvokeResult{}, refuse(RefuseNotAllowed)
	}
	if !slices.Contains(e.perms.Invoke, r.Command) {
		return InvokeResult{}, refuse(RefuseNotGranted)
	}
	if r.Resume != "" && !operationIDRe.MatchString(r.Resume) {
		return InvokeResult{}, refuse(RefuseInvalid)
	}
	w, ce, err := e.where(r.Cause, r.Repo, r.Worktree)
	if err != nil {
		return InvokeResult{}, err
	}
	if !e.invokes.Take() {
		return InvokeResult{}, refuse(RefuseRateLimited)
	}
	cmd, ok := e.r.h.o.Commands(r.Command)
	if !ok {
		return InvokeResult{}, refuse(RefuseNotAllowed)
	}
	jc, ok := cmd.(command.JSONCommand)
	if !ok {
		return InvokeResult{}, refuse(RefuseNotAllowed)
	}
	ctx, cancel := context.WithTimeout(ctx, InvokeTimeout)
	defer cancel()
	a, err := e.r.h.pluginApp(ctx, e.id, w)
	if err != nil {
		return InvokeResult{Outcome: string(command.OutcomeFailed), Code: command.ErrorCode(err)}, nil
	}
	if r.Resume != "" && a.Gate != nil {
		// Still waiting for a person: answer without running the call, so
		// a poll publishes nothing (no op.requested, no op.failed for a
		// plugin to react to).
		ap, err := a.Gate.Status(r.Resume)
		if errors.Is(err, policy.ErrNotRequester) {
			return InvokeResult{}, refuse(RefuseInvalid) // another client's approval
		}
		if err == nil && ap.State == approvalPending {
			out := InvokeResult{OperationID: r.Resume, Outcome: string(command.OutcomeAwaitingApproval), ApprovalID: r.Resume}
			if !ap.ExpiresAt.IsZero() {
				t := ap.ExpiresAt.UTC()
				out.ExpiresAt = &t
			}
			return out, nil
		}
	}
	if r.Resume != "" && e.r.h.takeForPlugin(e.id, r.Resume) {
		// The host is resuming this write itself, or already did: run
		// nothing and publish nothing.
		return InvokeResult{OperationID: r.Resume, Outcome: string(command.OutcomeFailed), Code: CodeResumedByHost}, nil
	}
	var cause string
	if ce != nil {
		cause = ce.ID
	}
	res := e.r.h.call(ctx, e.id, w, a, jc, r.Input, r.Resume, cause)
	switch {
	case errors.Is(res.Err, command.ErrConfirmTokenInput):
		return InvokeResult{}, refuse(RefuseConfirmToken)
	case errors.Is(res.Err, command.ErrJSONInput), errors.Is(res.Err, command.ErrNoResume):
		return InvokeResult{}, refuse(RefuseInvalid)
	}
	out := invokeResult(res, r.Resume)
	if r.Resume == "" && out.Outcome == string(command.OutcomeAwaitingApproval) && out.ApprovalID != "" {
		// Held for a person: describe it for plugin approve, and resume it
		// here once it is approved (ADR, "Phase 5 approving a plugin's
		// write").
		if a.Gate != nil {
			_ = DescribeHeld(a.Gate, out.ApprovalID, r.Command, res)
		}
		e.r.h.hold(heldCall{plugin: e.id, version: e.r.p.Version, hash: e.r.p.Hash, command: r.Command, input: append(json.RawMessage(nil), r.Input...), cause: cause,
			target: w, approval: out.ApprovalID, expires: out.ExpiresAt})
	}
	return out, nil
}

// DescribeHeld stores on approval id what a person needs to answer it with
// agentflow plugin approve: command, res's Detail, and the confirmation
// summary the command's gate returned (res.Value's *app.Pending), as a
// person at a terminal would see it. g is the plugin's gate, and res is the
// held call's result. Without a summary the request cannot be approved.
func DescribeHeld(g *policy.Gate, id, cmd string, res command.JSONResult) error {
	req := policy.PluginRequest{Command: cmd, Subject: res.Detail.Subject, Task: res.Detail.Task, Branch: res.Detail.Branch}
	if p, ok := res.Value.(*app.Pending); ok && p.Summary != nil {
		if raw, err := json.Marshal(p.Summary); err == nil {
			req.Summary = raw
		}
	}
	if req.Summary == nil {
		req.Omitted = "the command returned no confirmation summary"
	}
	return g.DescribePluginRequest(id, req)
}

// approvalPending is the state of an approval record no person answered.
const approvalPending = "pending"

// invokeResult turns the Invoker's result into what the plugin sees: no
// confirm token, no approval fingerprint, no error text.
func invokeResult(res command.JSONResult, resume string) InvokeResult {
	out := InvokeResult{OperationID: res.OperationID}
	switch res.Outcome.Kind {
	case command.OutcomeApplied:
		out.Outcome = string(command.OutcomeApplied)
		if raw, err := json.Marshal(res.Value); err == nil && len(raw) <= MaxResultBytes {
			out.Result = raw
		} else {
			out.Truncated = true
		}
	case command.OutcomeAwaitingApproval, command.OutcomePrepared:
		// A needs_confirmation result is reported as awaiting_approval and
		// its token is dropped: a person answers it on the console.
		out.Outcome = string(command.OutcomeAwaitingApproval)
		out.ApprovalID = res.Outcome.ApprovalID
		if !res.Outcome.ExpiresAt.IsZero() {
			t := res.Outcome.ExpiresAt.UTC()
			out.ExpiresAt = &t
		}
	case command.OutcomeSuggested:
		out.Outcome, out.Code = string(command.OutcomeSuggested), res.Outcome.Status
	case command.OutcomeFailed:
		if res.Outcome.Code == "approval_pending" && resume != "" {
			// Resumed before a person approved: still waiting.
			out.Outcome, out.ApprovalID = string(command.OutcomeAwaitingApproval), resume
			return out
		}
		fallthrough
	default:
		out.Outcome, out.Code = string(command.OutcomeFailed), res.Outcome.Code
		if out.Code == "" {
			out.Code = command.CodeError
		}
	}
	return out
}

// httpsURL reports whether raw is an absolute https URL without user
// information.
func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}
