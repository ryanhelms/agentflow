package mcpserver

import (
	"context"

	"github.com/ryanhelms/agentflow/internal/app"
)

type approvalTokenIn struct {
	ConfirmToken string `json:"confirm_token,omitempty" jsonschema:"original confirmation token or a shared operation ID after approval"`
}

func (s *Server) registerApprovalTools() {
	type explainIn struct {
		Action string `json:"action,omitempty" jsonschema:"exact action key or omit to list the available actions"`
	}
	type statusIn struct {
		OperationID string `json:"operation_id" jsonschema:"public ID from the shared approval"`
	}
	type approveIn struct {
		OperationID  string `json:"operation_id" jsonschema:"public ID of the reviewed operation"`
		ConfirmToken string `json:"confirm_token" jsonschema:"original review token, only after the user agrees to the exact operation"`
	}
	add(s, "agentflow_policy_explain", "Explain the personal settings, repository ceilings, review requirements and window for an action.", readOnly,
		func(ctx context.Context, a *app.App, in explainIn) (any, error) {
			return a.PolicyExplain(ctx, in.Action)
		})
	add(s, "agentflow_approval_status", "Read shared approval state, prompt owner and expiry without exposing its confirmation token.", readOnly,
		func(_ context.Context, a *app.App, in statusIn) (any, error) { return a.ApprovalStatus(in.OperationID) })
	add(s, "agentflow_approval_approve", "After explicit user approval, authorize one worker to resume the reviewed operation. This only records the decision.", local,
		func(_ context.Context, a *app.App, in approveIn) (any, error) {
			return a.ApproveOperation(in.OperationID, in.ConfirmToken)
		})
}
