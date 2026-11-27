package webui

import (
	"time"

	"github.com/ryanhelms/agentflow/internal/harness"
)

// Phase 6 harnesses projection state (lane 6.2). The contract is in the
// ADR, section "Phase 6 harnesses projection". Changing a shape needs an
// amendment.

const ProjectionHarnesses = "harnesses"

// HarnessesState is the "harnesses" state. It is the daemon's last
// harness.Report, so it is the same in every repository's frames.
type HarnessesState struct {
	// Detector is false when the daemon runs no harness source.
	Detector    bool                 `json:"detector"`
	Registry    harness.RegistryInfo `json:"registry"`
	AutoProbe   bool                 `json:"auto_probe"`
	Harnesses   []harness.Harness    `json:"harnesses"` // found states only; not_installed is left out
	SkippedPath []harness.SkippedDir `json:"skipped_path,omitempty"`
	Problems    []harness.Problem    `json:"problems,omitempty"`
	CheckedAt   time.Time            `json:"checked_at"`
	// Probing lists IDs being probed right now.
	Probing []string `json:"probing,omitempty"`
}
