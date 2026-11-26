package config

import (
	"slices"
	"testing"
)

func TestDefaultsPointAtRyanhelmsRepository(t *testing.T) {
	if DefaultFlowMap != "ryanhelms/agentflow@v0:flow-map.yml" {
		t.Fatalf("DefaultFlowMap = %q", DefaultFlowMap)
	}
	for _, org := range []string{"ryanhelms", "nscaledev"} {
		if !slices.Contains(DefaultInitOrgs, org) {
			t.Fatalf("DefaultInitOrgs = %v, want %s", DefaultInitOrgs, org)
		}
	}
}
