package mcpserver

import (
	"context"

	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/setup"
)

type setupPlanIn struct {
	Companion string `json:"companion,omitempty" jsonschema:"scope readiness to this user-declared MCP companion id"`
	Client    string `json:"client,omitempty" jsonschema:"project adapter (claude, codex, grok, cursor, vscode, gemini). Omit for headless readiness"`
}

type setupApplyIn struct {
	Companion    string `json:"companion,omitempty" jsonschema:"select one declared MCP companion; specify install or an explicit client"`
	Install      bool   `json:"install,omitempty" jsonschema:"install the selected pinned MCP companion only"`
	InstallGH    bool   `json:"install_gh,omitempty" jsonschema:"install only the pinned gh companion for selected providers"`
	Client       string `json:"client,omitempty" jsonschema:"explicit project adapter to register in the current workspace"`
	ConfirmToken string `json:"confirm_token,omitempty"`
}

func (s *Server) registerProviderSetup() {
	add(s, "devflow_setup_plan", "Inspect selected providers, probe a declared MCP companion, or check its registration in an explicit project client. Returns a fresh read-only readiness plan with next steps.", readOnly,
		func(ctx context.Context, in setupPlanIn) (any, error) {
			return setup.PlanSetup(ctx, s.app.Repo.Dir, in.Client, in.Companion)
		})
	add(s, "devflow_setup_apply", "Install a pinned gh or MCP companion, or register the selected adapter or companion in an explicit project client. Each action requires local confirmation. Rerun devflow_setup_plan to verify. Host trust and sign-in are handled by the client.", remote,
		func(ctx context.Context, in setupApplyIn) (any, error) {
			return s.app.ApplySetup(ctx, app.SetupAction{Client: in.Client, Companion: in.Companion, InstallGH: in.InstallGH, InstallMCP: in.Install}, in.ConfirmToken)
		})
}
