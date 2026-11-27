package config

import (
	"fmt"
	"time"

	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// UserAI is the personal ai section: ACP harness detection (ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md, section
// "Phase 6 configuration keys"). A repository has no ai keys.
type UserAI struct {
	Harnesses UserHarnesses `yaml:"harnesses,omitempty"`
}

// UserHarnesses configures harness detection.
type UserHarnesses struct {
	AutoProbe    *bool                            `yaml:"auto_probe,omitempty"`    // nil means on
	SignInCheck  *bool                            `yaml:"sign_in_check,omitempty"` // nil means on
	PollInterval *time.Duration                   `yaml:"poll_interval,omitempty"`
	ProbeTimeout *time.Duration                   `yaml:"probe_timeout,omitempty"`
	Disabled     []string                         `yaml:"disabled,omitempty"`
	Overrides    map[string]harnessdef.Definition `yaml:"overrides,omitempty"`
}

// Harness detection defaults and limits.
const (
	DefaultHarnessPollInterval = 30 * time.Second
	MinHarnessPollInterval     = 10 * time.Second
	MaxHarnessPollInterval     = 10 * time.Minute
	DefaultHarnessProbeTimeout = 5 * time.Second
	MinHarnessProbeTimeout     = 1 * time.Second
	MaxHarnessProbeTimeout     = 30 * time.Second
	MaxHarnessesDisabled       = 64
	MaxHarnessOverrides        = 64
)

// AutoProbeOn reports the effective auto_probe: nil means on.
func (h UserHarnesses) AutoProbeOn() bool { return h.AutoProbe == nil || *h.AutoProbe }

// SignInCheckOn reports the effective sign_in_check: nil means on.
func (h UserHarnesses) SignInCheckOn() bool { return h.SignInCheck == nil || *h.SignInCheck }

// EffectivePollInterval is poll_interval, defaulted and clamped to its limits.
func (h UserHarnesses) EffectivePollInterval() time.Duration {
	return clampDuration(h.PollInterval, DefaultHarnessPollInterval, MinHarnessPollInterval, MaxHarnessPollInterval)
}

// EffectiveProbeTimeout is probe_timeout, defaulted and clamped to its limits.
func (h UserHarnesses) EffectiveProbeTimeout() time.Duration {
	return clampDuration(h.ProbeTimeout, DefaultHarnessProbeTimeout, MinHarnessProbeTimeout, MaxHarnessProbeTimeout)
}

func clampDuration(v *time.Duration, def, lo, hi time.Duration) time.Duration {
	if v == nil || *v <= 0 {
		return def
	}
	return min(max(*v, lo), hi)
}

// ValidateAI checks the ai section's limits: the durations and the list
// sizes. Harness definitions are checked by harnessdef.Validate in the
// settings check chain and reported by detection; a bad override never
// stops agentflow from loading.
func ValidateAI(a UserAI) error {
	h := a.Harnesses
	for _, d := range []struct {
		name   string
		v      *time.Duration
		lo, hi time.Duration
	}{
		{"ai.harnesses.poll_interval", h.PollInterval, MinHarnessPollInterval, MaxHarnessPollInterval},
		{"ai.harnesses.probe_timeout", h.ProbeTimeout, MinHarnessProbeTimeout, MaxHarnessProbeTimeout},
	} {
		if d.v != nil && (*d.v < d.lo || *d.v > d.hi) {
			return fmt.Errorf("%s must be from %s to %s", d.name, d.lo, d.hi)
		}
	}
	if len(h.Disabled) > MaxHarnessesDisabled {
		return fmt.Errorf("ai.harnesses.disabled has more than %d items", MaxHarnessesDisabled)
	}
	if len(h.Overrides) > MaxHarnessOverrides {
		return fmt.Errorf("ai.harnesses.overrides has more than %d entries", MaxHarnessOverrides)
	}
	return nil
}
