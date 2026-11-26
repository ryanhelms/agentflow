package cli

import (
	"testing"

	"github.com/ryanhelms/agentflow/internal/goldentest"
)

// TestUsageGolden pins the usage text. The text is rendered from the command
// registry's usage blocks and the legacy blocks of commands not migrated yet
// (usage_legacy.go); a migration must not change a byte of it. Regenerate
// with go test ./internal/cli -run TestUsageGolden -update and review the
// diff.
func TestUsageGolden(t *testing.T) {
	goldentest.Check(t, "usage.txt", usage)
}
