package mcpserver

import (
	"context"

	"github.com/nscaledev/devflow/internal/setup"
)

type setupPlanIn struct {
	Client string `json:"client,omitempty" jsonschema:"project adapter (claude, codex, grok, cursor, vscode, gemini). Omit for headless readiness"`
}

type setupApplyIn struct {
	Client       string `json:"client" jsonschema:"explicit project adapter to register in the current workspace"`
	ConfirmToken string `json:"confirm_token,omitempty"`
}

func (s *Server) registerProviderSetup() {
	add(s, "devflow_setup_plan", "Inspect selected providers and optionally a project adapter in the environment running this stdio server. Returns a fresh read-only readiness plan with next steps.", readOnly,
		func(ctx context.Context, in setupPlanIn) (any, error) {
			return setup.PlanProviderSetup(ctx, s.app.Repo.Dir, in.Client)
		})
	add(s, "devflow_setup_apply", "Register a project adapter after provider readiness and user confirmation. Rerun devflow_setup_plan to verify. Host trust and sign-in are handled by the client.", local,
		func(ctx context.Context, in setupApplyIn) (any, error) {
			return s.app.ApplyProviderSetup(ctx, in.Client, in.ConfirmToken)
		})
}
