package mcpserver

import (
	"context"

	"github.com/nscaledev/agentflow/internal/app"
)

type notifyIn struct {
	app.NotifyInput
	DryRun       bool   `json:"dry_run,omitempty"`
	ConfirmToken string `json:"confirm_token,omitempty"`
}

func (s *Server) registerNotifications() {
	add(s, "agentflow_notify", "Preview or deliver a configured notification. Supply a stable operation_id, and reuse it for retries.", remote,
		func(ctx context.Context, in notifyIn) (any, error) {
			in.NotifyInput.DryRun = in.DryRun
			result, pending, err := s.app.Notify(ctx, in.NotifyInput, in.ConfirmToken)
			if pending != nil {
				return pending, err
			}
			return result, err
		})
}
