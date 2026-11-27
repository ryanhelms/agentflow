package bus

// Phase 7 event types: ACP sessions (ADR, "Phase 7 events"). The session
// host (internal/aisession) produces them for the session's repository,
// tagged with its worktree, through notifysock.Deliver: ai.session.** is a
// socket type, so any local process can send one and no reader trusts a
// payload. Payloads carry IDs, codes, counts and kinds only: no prompt,
// message, title, path, command, option name, cost or agent text. That text
// stays in the session store in the agentflow home.
const (
	// TypeAISessionStarted: the session record was written.
	TypeAISessionStarted Type = "ai.session.started"
	// TypeAISessionChanged: the session moved to another state.
	TypeAISessionChanged Type = "ai.session.changed"
	// TypeAISessionOutput: new transcript items. Transient; at most 4 a
	// second per session (coalesced).
	TypeAISessionOutput Type = "ai.session.output"
	// TypeAISessionAsked: an agent request is held for a person.
	TypeAISessionAsked Type = "ai.session.asked"
	// TypeAISessionAnswered: an ask was answered, timed out or cancelled.
	TypeAISessionAnswered Type = "ai.session.answered"
	// TypeAISessionEnded: the session ended.
	TypeAISessionEnded Type = "ai.session.ended"
)

var phase7Types = []TypeInfo{
	{Type: TypeAISessionStarted, Scope: ScopeWorktree, Phase: 7},
	{Type: TypeAISessionChanged, Scope: ScopeWorktree, Phase: 7},
	{Type: TypeAISessionOutput, Scope: ScopeWorktree, Transient: true, Phase: 7},
	{Type: TypeAISessionAsked, Scope: ScopeWorktree, Phase: 7},
	{Type: TypeAISessionAnswered, Scope: ScopeWorktree, Phase: 7},
	{Type: TypeAISessionEnded, Scope: ScopeWorktree, Phase: 7},
}

// The phase 7 types register here, so types.go changes only by its
// ReservedFamilies line.
func init() {
	for _, info := range phase7Types {
		registry[info.Type] = info
	}
}

// AISessionStarted is the payload of ai.session.started.
type AISessionStarted struct {
	Session   string `json:"session"`
	Harness   string `json:"harness"`    // definition ID
	StartedBy string `json:"started_by"` // person, agent
	Client    string `json:"client,omitempty"`
	Ceiling   string `json:"ceiling,omitempty"` // "confirm" for an agent's session
}

// AISessionChanged is the payload of ai.session.changed.
type AISessionChanged struct {
	Session  string `json:"session"`
	State    string `json:"state"`
	Previous string `json:"previous,omitempty"`
	Code     string `json:"code,omitempty"`
}

// AISessionOutput is the payload of ai.session.output: Seq is the newest
// transcript item, Items how many arrived since the last event, Kinds
// their item kinds.
type AISessionOutput struct {
	Session string   `json:"session"`
	Seq     int64    `json:"seq"`
	Items   int      `json:"items"`
	Kinds   []string `json:"kinds"`
}

// AISessionAsked is the payload of ai.session.asked.
type AISessionAsked struct {
	Session  string `json:"session"`
	Ask      string `json:"ask"`
	Kind     string `json:"kind"` // permission, fs_read, fs_write, terminal
	ToolKind string `json:"tool_kind,omitempty"`
	Reason   string `json:"reason"`
}

// AISessionAnswered is the payload of ai.session.answered.
type AISessionAnswered struct {
	Session  string `json:"session"`
	Ask      string `json:"ask"`
	Decision string `json:"decision"`     // allow, deny
	By       string `json:"by,omitempty"` // person, agent, policy
	Handler  string `json:"handler"`      // person, timeout, cancel
	Code     string `json:"code,omitempty"`
}

// AISessionEnded is the payload of ai.session.ended.
type AISessionEnded struct {
	Session    string `json:"session"`
	Code       string `json:"code"`
	DurationMS int64  `json:"duration_ms"`
	ToolCalls  int    `json:"tool_calls"`
	Asks       int    `json:"asks"`
	Allowed    int    `json:"allowed"`
	Denied     int    `json:"denied"`
	Unasked    int    `json:"unasked"`
}
