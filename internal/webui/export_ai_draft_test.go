package webui

import (
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness"
)

// SetAIFeatureStateForTest replaces what the projection reads to say
// whether a draft feature is available, and resets the draft routes' rate
// limit and kept confirmations.
func SetAIFeatureStateForTest(t *testing.T, user *config.User, published map[string]harness.PublishedState) {
	old := aiFeatureState
	aiFeatureState = func() (*config.User, map[string]harness.PublishedState) { return user, published }
	aiDrafts.mu.Lock()
	aiDrafts.seen, aiDrafts.confirm = nil, nil
	aiDrafts.mu.Unlock()
	t.Cleanup(func() { aiFeatureState = old })
}

// ResetAIDraftRateForTest forgets the draft routes' recent requests.
func ResetAIDraftRateForTest() {
	aiDrafts.mu.Lock()
	aiDrafts.seen = nil
	aiDrafts.mu.Unlock()
}
