package bus

// Phase 8 event types: AI drafts (ADR, "Phase 8 events"). The session host
// (internal/aisession) produces ai.draft.finished once it wrote a draft's
// draft.json, before ai.session.ended, for the draft's repository and
// worktree. ai.draft.** is a socket type: any local process can send one,
// so no reader trusts a payload; the AI projection reads draft.json from
// the store. Payloads carry IDs, codes and counts only: no manifest part,
// drafted text or target label.
const (
	// TypeAIDraftFinished: the host wrote the draft's result.
	TypeAIDraftFinished Type = "ai.draft.finished"
)

var phase8Types = []TypeInfo{
	{Type: TypeAIDraftFinished, Scope: ScopeWorktree, Phase: 8},
}

func init() {
	for _, info := range phase8Types {
		registry[info.Type] = info
	}
}

// AIDraftFinished is the payload of ai.draft.finished: the counts of the
// draft's candidates, issues and warnings.
type AIDraftFinished struct {
	Session    string `json:"session"`
	Feature    string `json:"feature"`
	State      string `json:"state"` // ready, invalid, failed, cancelled
	Code       string `json:"code,omitempty"`
	Candidates int    `json:"candidates"`
	Issues     int    `json:"issues"`
	Warnings   int    `json:"warnings"`
}
