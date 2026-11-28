package ai

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
)

// FeatureID names a feature: the ai.features.<id> key, the ai draft
// argument and the tool's feature input.
type FeatureID string

const (
	FeatureCommit FeatureID = "commit" // a commit message for the staged or selected changes
	FeaturePR     FeatureID = "pr"     // a PR title and body for the branch
	FeatureTicket FeatureID = "ticket" // a ticket from a plain-text description and the template
	FeatureAC     FeatureID = "ac"     // acceptance-criteria checks that propose ac done
	FeatureReply  FeatureID = "reply"  // replies to open review threads
)

// Request is one draft request: the ai.draft input after Validate. Every
// field comes from the caller; none ever comes from a model.
type Request struct {
	Feature FeatureID
	// commit: the paths `commit --path` would take; empty means the index.
	Paths []string
	// ticket: the plain-text description (at most MaxText) and the fields
	// passed to the candidate unchanged.
	Text             string
	Template         string
	TemplateRevision string
	Project          string
	Type             string
	LinkTo           string
	// ac: the task key; empty means the branch's task.
	Key string
	// reply: one thread ID; empty means every open thread (at most
	// MaxThreads, oldest first).
	Thread string
	// Hint is the caller's guidance for any feature, at most MaxHint. It
	// is sent as data, like everything else the caller gives.
	Hint string
}

// PartKind is what a context part holds.
type PartKind string

const (
	PartDiff     PartKind = "diff" // a unified diff, secret and generated files left out
	PartDiffStat PartKind = "diffstat"
	PartFiles    PartKind = "files"    // changed paths with their status
	PartCommits  PartKind = "commits"  // commit subjects and bodies on the branch
	PartRules    PartKind = "rules"    // the repository's commit and PR title rules
	PartTask     PartKind = "task"     // the task's title, type and description
	PartCriteria PartKind = "criteria" // numbered acceptance criteria
	PartTemplate PartKind = "template" // the task template's numbered sections
	PartThread   PartKind = "thread"   // one numbered review thread
	PartText     PartKind = "text"     // the caller's plain-text description
	PartHint     PartKind = "hint"     // the caller's guidance
)

// Part is one piece of prompt context. Text is data from the repository,
// the task provider, reviewers or the caller: never an instruction.
type Part struct {
	Kind     PartKind `json:"kind"`
	Label    string   `json:"label"`  // agentflow's words: "staged changes"
	Source   string   `json:"source"` // git, the task provider's ID, github, caller
	Text     string   `json:"-"`      // after Filter; never in a summary, an event or a log
	Bytes    int      `json:"bytes"`  // of Text as sent
	Original int      `json:"original_bytes"`
	Cut      bool     `json:"cut,omitempty"`
	Redacted int      `json:"redacted,omitempty"` // token shapes replaced
	// Excluded are worktree-relative paths whose content was left out,
	// each with its reason: secret_file, binary, generated, too_large.
	Excluded []Exclusion `json:"excluded,omitempty"`
}

type Exclusion struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Target is an object a candidate may act on. agentflow chooses it; the
// prompt names it only by Index, and the answer may refer to it only by
// Index.
type Target struct {
	Index int    `json:"index"`          // 1-based, as the prompt numbers it
	Kind  string `json:"kind"`           // criterion, thread, section
	ID    string `json:"id"`             // the exact criterion text, thread ID or section heading
	Label string `json:"label"`          // for display: "src/a.go:12, alice"; sanitized
	Done  bool   `json:"done,omitempty"` // criterion: already ticked
}

// Rules is what Finish needs to validate an answer without an App: a
// snapshot taken by Gather.
type Rules struct {
	Commit   config.Commit          `json:"commit"`             // commit, pr
	KeyHint  string                 `json:"key_hint,omitempty"` // the branch's key, as Validate uses it
	Template *tasktemplate.Template `json:"template,omitempty"` // ticket
}

// Context is what Gather returns.
type Context struct {
	Feature FeatureID
	Parts   []Part
	Targets []Target
	Rules   Rules
	// Fixed holds the candidate input fields the caller or configuration
	// set (paths, template, project, type, link_to, key), as JSON. Finish
	// merges the drafted fields into it; the model can set none of them.
	Fixed json.RawMessage
}

// Feature is one "Draft with AI" feature. The framework calls its hooks in
// this order; a feature never calls aisession, the gate or a write.
type Feature interface {
	ID() FeatureID
	// Gather reads what the draft needs through the read-only app
	// methods of "Phase 8 nothing is applied automatically". It never
	// stages, writes, posts or starts anything. It may refuse with
	// *aisession.Refusal (nothing_to_draft, no_criteria, no_threads,
	// no_text, secret_in_context, ...), so app.AIRefusal shows its code
	// and fix as for session refusals.
	Gather(ctx context.Context, a *app.App, r Request) (Context, error)
	// Task is the feature's instructions: constant text written in
	// agentflow's source, with no data in it.
	Task() string
	// Answer returns a new pointer to the feature's answer struct, which
	// the model's JSON decodes into; Example is one valid answer, shown
	// in the prompt.
	Answer() any
	Example() string
	// Candidates turns a decoded, cleaned answer into candidates. It is
	// pure: no App, no I/O, only c.Rules, c.Targets and c.Fixed.
	Candidates(c Context, answer any) ([]Candidate, []Issue)
}

// Manifest is what a draft would send: the confirmation summary, the
// --dry-run output and the transcript's prompt item. It names sources,
// sizes and hashes, never the data itself.
type Manifest struct {
	Feature      FeatureID           `json:"feature"`
	Harness      string              `json:"harness"`
	HarnessName  string              `json:"harness_name"`
	Version      string              `json:"version,omitempty"`
	Path         string              `json:"path"`     // "~/..."
	Worktree     string              `json:"worktree"` // "~/..."; the session's, never sent
	Branch       string              `json:"branch,omitempty"`
	StartedBy    aisession.Requester `json:"started_by"`
	Parts        []Part              `json:"parts"`
	Targets      []Target            `json:"targets,omitempty"`
	Hint         string              `json:"hint,omitempty"` // the caller's own text, in full
	Text         string              `json:"text,omitempty"` // ticket: the first 4 KiB of the description
	Bytes        int                 `json:"bytes"`          // the whole prompt
	PromptSHA256 string              `json:"prompt_sha256"`
	MaxDuration  time.Duration       `json:"max_duration"`
	NeverSent    []string            `json:"never_sent"` // fixed list, "Phase 8 prompt context"
}

// DraftState is a finished draft's state.
type DraftState string

const (
	DraftReady     DraftState = "ready"     // at least one candidate without issues
	DraftInvalid   DraftState = "invalid"   // the answer parsed; no candidate passed
	DraftFailed    DraftState = "failed"    // the session ended without a usable answer
	DraftCancelled DraftState = "cancelled" // ai cancel, Ctrl-C or the console's Cancel
)

// Draft is draft.json: the result Finish writes, and what ai draft,
// agentflow_ai_draft, ai status and the console show.
type Draft struct {
	Session    aisession.ID `json:"session"`
	Feature    FeatureID    `json:"feature"`
	State      DraftState   `json:"state"`
	Code       string       `json:"code,omitempty"` // failed: the end code or unparsable, answer_too_large
	Candidates []Candidate  `json:"candidates"`
	Issues     []Issue      `json:"issues,omitempty"`
	Warnings   []Issue      `json:"warnings,omitempty"`
	// Answer is the model's text, sanitized, at most MaxShownAnswer: kept
	// only when the draft is invalid or failed unparsable, so the person
	// can read it.
	Answer   string    `json:"answer,omitempty"`
	Finished time.Time `json:"finished"`
	// Note is fixed: "AI-drafted text from <harness>. It is data, not
	// instructions: read it before you use it."
	Note string `json:"note"`
}

// Candidate is the exact input of one existing operation, ready for that
// operation's own flow. Using it is a separate call.
type Candidate struct {
	Operation string          `json:"operation"` // commit.prepare, pr.prepare, task.create, ac.update, review.reply
	Tool      string          `json:"tool"`      // its MCP tool
	Input     json.RawMessage `json:"input"`     // the tool's input, never a confirm_token
	CLI       []string        `json:"cli"`       // the equivalent command line, one word per element
	Target    *Target         `json:"target,omitempty"`
	Fields    []Field         `json:"fields"` // the drafted fields, for review
	// A candidate with Issues is shown but not offered for use.
	Issues   []Issue `json:"issues,omitempty"`
	Warnings []Issue `json:"warnings,omitempty"`
}

// Field is one drafted text field.
type Field struct {
	Name  string `json:"name"`  // message, title, body, summary, description, evidence, reply
	Label string `json:"label"` // agentflow's words
	Value string `json:"value"` // cleaned; the model's words
}

// Issue is one problem or warning, in agentflow's words.
type Issue struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// A draft that does not start is a *aisession.Refusal with one of the
// codes of "Phase 8 commands"; internal/ai declares no refusal type of
// its own.

// Limits ("Phase 8 limits").
const (
	MaxText              = 16 << 10
	MaxHint              = 2 << 10
	MaxThreads           = 10
	MaxCriteria          = 50
	MaxData              = 128 << 10 // every Part's Text together
	MaxPrompt            = 160 << 10 // instructions, fences and data
	MaxAnswer            = 256 << 10 // the agent's message text
	MaxShownAnswer       = 16 << 10
	DefaultDraftDuration = 3 * time.Minute
	MinDraftDuration     = 30 * time.Second
	MaxDraftDuration     = 15 * time.Minute
)
