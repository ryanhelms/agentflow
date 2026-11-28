// Package features holds the "Draft with AI" features (ADR "Phase 8
// features"): commit, pr and ac (lane 8.2), ticket and reply (lane 8.3).
// Each file registers its feature with ai.Register in init;
// internal/command/ops/ai_draft.go imports this package for that side
// effect.
//
// A feature supplies typed ai.Parts, constant Task and Example text and an
// answer struct. It writes no prompt text of its own beyond Task and
// Example: every untrusted string goes through ai.Filter and
// ai.PromptBuilder. Its Gather reads only through the read methods
// TestDraftNeverWrites allows, and its Candidates is pure.
//
// The answer struct's text fields carry their cap and form in a draft tag,
// which ai.Finish applies when it cleans the answer:
//
//	Message string `json:"message" draft:"max=8192"`
//	Title   string `json:"title" draft:"max=256,line"`
//
// A feature may implement ai.Caps for the per-part caps of the feature
// table.
package features
