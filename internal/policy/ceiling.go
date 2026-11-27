package policy

import "github.com/ryanhelms/agentflow/internal/config"

// CeilingSource is the AutonomyDecision.Source of a level the ceiling
// lowered.
const CeilingSource = "plugin autonomy ceiling"

// WithCeiling returns a copy of g whose Explain caps every resolved level
// at ceiling (ADR, "Phase 5 command invocation", step 5): with
// config.Confirm, auto becomes confirm and suggest stays suggest, so a
// write never applies without a person. The copy shares g's secret, prompt
// owner and journal. A ceiling that is not a known level is treated as
// config.Suggest, the strictest.
func (g *Gate) WithCeiling(ceiling config.Level) *Gate {
	current := *g
	switch ceiling {
	case config.Suggest, config.Confirm, config.Auto:
	default:
		ceiling = config.Suggest
	}
	if current.ceiling != "" {
		ceiling = config.Min(current.ceiling, ceiling)
	}
	current.ceiling = ceiling
	return &current
}

// clamp lowers d to the ceiling.
func (g *Gate) clamp(d config.AutonomyDecision) config.AutonomyDecision {
	if g.ceiling == "" || d.Level == g.ceiling || config.Min(d.Level, g.ceiling) == d.Level {
		return d
	}
	d.Level, d.Source = g.ceiling, CeilingSource
	return d
}
