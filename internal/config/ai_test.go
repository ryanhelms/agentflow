package config

import (
	"testing"
	"time"
)

func TestUserHarnessesEffectiveValues(t *testing.T) {
	var h UserHarnesses
	if !h.AutoProbeOn() || !h.SignInCheckOn() {
		t.Fatal("nil toggles must mean on")
	}
	off := false
	h.AutoProbe, h.SignInCheck = &off, &off
	if h.AutoProbeOn() || h.SignInCheckOn() {
		t.Fatal("false toggles must mean off")
	}
	if h.EffectiveProbeTimeout() != DefaultHarnessProbeTimeout || h.EffectivePollInterval() != DefaultHarnessPollInterval {
		t.Fatal("defaults")
	}
	long := time.Hour
	h.ProbeTimeout, h.PollInterval = &long, &long
	if h.EffectiveProbeTimeout() != MaxHarnessProbeTimeout || h.EffectivePollInterval() != MaxHarnessPollInterval {
		t.Fatal("clamping")
	}
	if err := ValidateAI(UserAI{Harnesses: h}); err == nil {
		t.Fatal("ValidateAI accepted an hour")
	}
	if err := ValidateAI(UserAI{Harnesses: UserHarnesses{Disabled: make([]string, 65)}}); err == nil {
		t.Fatal("ValidateAI accepted 65 disabled IDs")
	}
}
