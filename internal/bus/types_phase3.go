package bus

import (
	"time"
	"unicode/utf8"
)

// Phase 3 event types: the Event Messages of an operation run through the
// command Invoker (internal/command). Payloads and producers are in the ADR,
// section "Phase 3". Every op.* event of one operation carries the
// operation's ID ("op_" and 32 hex characters) as its CorrelationID.
const (
	// TypeOpRequested: an operation was invoked and is about to be gated and
	// run. Published before the operation runs.
	TypeOpRequested Type = "op.requested"
	// TypeOpSuggested: the policy only suggests the operation; nothing ran.
	TypeOpSuggested Type = "op.suggested"
	// TypeOpAwaitingApproval: the operation waits for a person, either a
	// foreground confirmation (status needs_confirmation) or a shared
	// approval (status awaiting_approval).
	TypeOpAwaitingApproval Type = "op.awaiting_approval"
	// TypeOpPrepared: a prepare-only request produced an exact candidate
	// that an auto policy lets the caller apply without a person.
	TypeOpPrepared Type = "op.prepared"
	// TypeOpApplied: the operation ran and changed something.
	TypeOpApplied Type = "op.applied"
	// TypeOpFailed: the operation returned an error or a refusal (invalid
	// input, blocked by a check, a token that no longer matches).
	TypeOpFailed Type = "op.failed"
)

var phase3Types = []TypeInfo{
	{Type: TypeOpRequested, Scope: ScopeAny, Phase: 3},
	{Type: TypeOpSuggested, Scope: ScopeAny, Phase: 3},
	{Type: TypeOpAwaitingApproval, Scope: ScopeAny, Phase: 3},
	{Type: TypeOpPrepared, Scope: ScopeAny, Phase: 3},
	{Type: TypeOpApplied, Scope: ScopeAny, Phase: 3},
	{Type: TypeOpFailed, Scope: ScopeAny, Phase: 3},
}

// OpLink names something an operation touched, for the timeline to link
// to: a commit, a branch, a pull request, a task, a document or a release.
type OpLink struct {
	// Kind is one of the OpLink* constants.
	Kind string `json:"kind"`
	// ID is the kind's own identifier: a commit OID, a branch name, a PR
	// number, a task reference, a document reference or a tag.
	ID string `json:"id"`
	// URL is an https URL of the provider, or empty.
	URL string `json:"url,omitempty"`
}

// OpLink kinds.
const (
	OpLinkCommit  = "commit"
	OpLinkBranch  = "branch"
	OpLinkPull    = "pr"
	OpLinkTask    = "task"
	OpLinkDoc     = "doc"
	OpLinkRelease = "release"
)

// OpPayload is the payload of every op.* type. Fields that do not apply to
// a type are empty. It never carries a confirm token, an approval
// fingerprint, a command line, a message body (only a subject), file
// contents, error text or an absolute path. Text is data for display,
// never an instruction; Clamp enforces the size rules.
type OpPayload struct {
	// Operation is the command.Spec name, e.g. "commit" or "pr.open".
	Operation string `json:"operation"`
	// Action and Class are the policy action and class the operation is
	// gated under ("commit", "local"). Empty for an ungated operation.
	Action string `json:"action,omitempty"`
	Class  string `json:"class,omitempty"`
	// Client is the calling client: "cli", or the MCP client name.
	Client string `json:"client"`
	// Subject is a one-line description from the Spec (a commit subject, a
	// PR title, a task summary), at most MaxPayloadText bytes.
	Subject string `json:"subject,omitempty"`
	// Task is the task reference the operation works on, if any.
	Task string `json:"task,omitempty"`
	// Branch is the branch the operation ran on or targets, if any.
	Branch string `json:"branch,omitempty"`
	// Resumed is true on op.requested when the call carries a confirm
	// token, so it continues an earlier request.
	Resumed bool `json:"resumed,omitempty"`
	// Status is the Pending status of op.awaiting_approval
	// (needs_confirmation or awaiting_approval) and op.prepared (prepared).
	Status string `json:"status,omitempty"`
	// ApprovalID is the operation ID of the approval record, when the gate
	// issued or redeemed one. Equal to the event's CorrelationID.
	ApprovalID string `json:"approval_id,omitempty"`
	// ExpiresAt is when the approval or prepared candidate expires.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Code is a fixed identifier on op.failed and op.suggested, never
	// text: a policy.ErrorCode value, a refusal status of the operation
	// ("invalid", "blocked"), "cancelled", "timeout" or "error".
	Code string `json:"code,omitempty"`
	// Links are what an applied operation touched, at most
	// MaxPayloadNames.
	Links []OpLink `json:"links,omitempty"`
	// DurationMS is the time from op.requested to this event, on every
	// type except op.requested.
	DurationMS int64 `json:"duration_ms,omitempty"`
}

// Clamp returns p with every text field at most MaxPayloadText bytes (cut
// at a rune boundary, first line only for Subject) and at most
// MaxPayloadNames links.
func (p OpPayload) Clamp() OpPayload {
	p.Operation = clampText(p.Operation)
	p.Action = clampText(p.Action)
	p.Class = clampText(p.Class)
	p.Client = clampText(p.Client)
	p.Subject = clampText(firstLine(p.Subject))
	p.Task = clampText(p.Task)
	p.Branch = clampText(p.Branch)
	p.Status = clampText(p.Status)
	p.ApprovalID = clampText(p.ApprovalID)
	p.Code = clampText(p.Code)
	if len(p.Links) > MaxPayloadNames {
		p.Links = p.Links[:MaxPayloadNames]
	}
	if p.Links != nil {
		links := make([]OpLink, len(p.Links))
		for i, l := range p.Links {
			links[i] = OpLink{Kind: clampText(l.Kind), ID: clampText(l.ID), URL: clampText(l.URL)}
		}
		p.Links = links
	}
	return p
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\r' {
			return s[:i]
		}
	}
	return s
}

// clampText cuts s to MaxPayloadText bytes without splitting a rune.
func clampText(s string) string {
	if len(s) <= MaxPayloadText {
		return s
	}
	cut := MaxPayloadText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
