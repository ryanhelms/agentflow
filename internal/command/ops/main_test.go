package ops_test

import (
	"os"
	"testing"

	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

// TestMain lets this test binary play the fake ACP agent
// (aisessiontest.Install) when a test starts it as one.
func TestMain(m *testing.M) {
	jsonrpctest.RunChild()
	os.Exit(m.Run())
}
