package mcpserver

import (
	"context"
	"errors"
	"testing"

	"github.com/ryanhelms/agentflow/internal/policy"
)

// agentflow mcp --client plugin:<id> is refused: that prefix belongs to the
// plugin host, and a client using it could redeem a plugin's approvals.
func TestServerRefusesAPluginClientName(t *testing.T) {
	if _, err := New(context.Background(), t.TempDir(), "plugin:ci-notify"); !errors.Is(err, policy.ErrReservedClient) {
		t.Fatalf("New with a plugin client: %v", err)
	}
}
