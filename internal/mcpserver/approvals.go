package mcpserver

import "context"

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
	add(s, "devflow_policy_explain", "Explain the personal settings, repository ceilings, review requirements and window for an action.", readOnly,
		func(_ context.Context, in explainIn) (any, error) { return s.app.PolicyExplain(in.Action) })
	add(s, "devflow_approval_status", "Read shared approval state, prompt owner and expiry without exposing its confirmation token.", readOnly,
		func(_ context.Context, in statusIn) (any, error) { return s.app.ApprovalStatus(in.OperationID) })
	add(s, "devflow_approval_approve", "After explicit user approval, authorize one worker to resume the reviewed operation. This only records the decision.", local,
		func(_ context.Context, in approveIn) (any, error) {
			return s.app.ApproveOperation(in.OperationID, in.ConfirmToken)
		})
}
