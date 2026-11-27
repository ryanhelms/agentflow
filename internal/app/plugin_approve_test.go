package app

import (
	"context"
	"errors"
	"testing"

	"github.com/ryanhelms/agentflow/internal/policy"
)

// No caller names itself a plugin: the plugin: client prefix belongs to the
// plugin host (OpenPlugin), so a client such as `agentflow mcp --client
// plugin:ci-notify` cannot create or redeem a plugin's approvals.
func TestOpenRefusesThePluginClientPrefix(t *testing.T) {
	for _, c := range []string{"plugin:ci-notify", "plugin:"} {
		if _, err := Open(context.Background(), t.TempDir(), c); !errors.Is(err, policy.ErrReservedClient) {
			t.Fatalf("Open(%q): %v", c, err)
		}
	}
}
