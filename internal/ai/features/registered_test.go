package features_test

import (
	"testing"

	"github.com/ryanhelms/agentflow/internal/ai"
	_ "github.com/ryanhelms/agentflow/internal/ai/features" // the features register themselves
)

// TestEveryFeatureIsRegistered: no FeatureID constant is left without its
// feature, so each ai.features.<id>.harness setting turns something on.
func TestEveryFeatureIsRegistered(t *testing.T) {
	for _, id := range ai.IDs() {
		if _, ok := ai.Lookup(id); !ok {
			t.Errorf("feature %s is not registered", id)
		}
	}
}
