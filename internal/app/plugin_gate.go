package app

import "github.com/ryanhelms/agentflow/internal/config"

// WithAutonomyCeiling returns a copy of a whose gate caps every resolved
// autonomy level at ceiling (policy.Gate.WithCeiling). The plugin host
// opens a plugin's App with config.Confirm, so auto never applies a
// plugin's write without a person (ADR, "Phase 5 command invocation"). The
// copy shares a's repository, configuration and client; provider clients
// are opened again on first use.
func (a *App) WithAutonomyCeiling(ceiling config.Level) *App {
	gate := a.Gate
	if gate != nil {
		gate = gate.WithCeiling(ceiling)
	}
	return &App{Repo: a.Repo, Cfg: a.Cfg, HasCfg: a.HasCfg, User: a.User, Gate: gate, Client: a.Client, Home: a.Home}
}
