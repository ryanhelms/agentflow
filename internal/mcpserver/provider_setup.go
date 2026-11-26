package mcpserver

import (
	"context"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/setup"
)

type setupPlanIn struct {
	Companion string `json:"companion,omitempty" jsonschema:"scope readiness to this user-declared MCP companion id"`
	Client    string `json:"client,omitempty" jsonschema:"project adapter (claude, claude-desktop, codex, grok, cursor, vscode, gemini). Omit for headless readiness"`
}

type setupProvidersIn struct {
	Explain bool `json:"explain,omitempty" jsonschema:"include each selected provider's effective settings, their source layer and release constraints"`
}

type setupApplyIn struct {
	Companion    string `json:"companion,omitempty" jsonschema:"select one declared MCP companion; specify install or an explicit client"`
	Install      bool   `json:"install,omitempty" jsonschema:"install the selected pinned MCP companion only"`
	InstallGH    bool   `json:"install_gh,omitempty" jsonschema:"install only the pinned gh companion for selected providers"`
	Client       string `json:"client,omitempty" jsonschema:"explicit project adapter to register in the current workspace (use claude-desktop for Claude Code Desktop)"`
	ConfirmToken string `json:"confirm_token,omitempty"`
}

func (s *Server) registerProviderSetup() {
	add(s, "agentflow_setup_plan", "Inspect selected providers, probe a declared MCP companion, or check its registration in an explicit project client. Returns a fresh read-only readiness plan with next steps.", readOnly,
		func(ctx context.Context, a *app.App, in setupPlanIn) (any, error) {
			return setup.PlanSetup(ctx, a.Repo.Dir, in.Client, in.Companion)
		})
	add(s, "agentflow_setup_providers", "List every provider each role accepts, the default, which one this workspace selects, its contract, the provider definition files loaded, and whether agentflow checks readiness itself or through an MCP companion. With explain, also each selected provider's effective settings, the layer that set each one and any release constraint. Reads local configuration only.", readOnly,
		func(ctx context.Context, a *app.App, in setupProvidersIn) (any, error) {
			return setup.ListConfiguredProviders(ctx, a.Repo.Dir, a.Cfg, a.HasCfg, in.Explain)
		})
	add(s, "agentflow_setup_apply", "Install a pinned gh or MCP companion, or register the selected adapter or companion in an explicit project client. Each action requires local confirmation. Rerun agentflow_setup_plan to verify. Host trust and sign-in are handled by the client.", remote,
		func(ctx context.Context, a *app.App, in setupApplyIn) (any, error) {
			return a.ApplySetup(ctx, app.SetupAction{Client: in.Client, Companion: in.Companion, InstallGH: in.InstallGH, InstallMCP: in.Install}, in.ConfirmToken)
		})
}
