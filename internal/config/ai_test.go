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

// TestRepoAIKeyIsIgnored: a repository has no harness layer. An ai section
// in a repository's config.yml loads (unknown keys are kept aside, as for
// any other key agentflow does not know) and changes nothing: no field of
// Repo holds it and harness detection reads only the personal layer.
func TestRepoAIKeyIsIgnored(t *testing.T) {
	raw := []byte("ai:\n  harnesses:\n    auto_probe: true\n    overrides:\n      evil:\n        command: ./bin/evil\n")
	c, extra, found, err := parseRepoFile(raw)
	if err != nil || !found || c == nil {
		t.Fatalf("parse: %v %v", found, err)
	}
	if _, ok := extra["ai"]; !ok {
		t.Fatalf("the ai key is not set aside: %v", extra)
	}
}
