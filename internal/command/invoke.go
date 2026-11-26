package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/provider/native"
)

// Origin is who sent an Invocation.
type Origin struct {
	// Source is the bus source of its events: "cli", "mcp:<client>" or
	// "daemon". It must pass notifysock.ValidSource for "cli" and "mcp:*".
	Source string
	// Client is the calling client, as app.App.Client: "cli", "claude".
	Client string
	// Cause is the ID of the event that triggered the call (a plugin
	// reacting to gh.ci.updated, phase 5), or "". It becomes the
	// op.requested event's CausationID.
	Cause string
}

// Invocation is the Command Message: one request to run one operation. It is
// addressed to the Invoker, never published on the bus and never stored.
type Invocation struct {
	// ID is the operation ID, "op_" and 32 hex characters, and the
	// CorrelationID of the call's events. A call that carries a confirm
	// token uses the operation ID the token names (policy.OperationIDOf),
	// so a resumed call joins the request it resumes. Otherwise it is new.
	ID string
	// Meta is the Spec's metadata.
	Meta Meta
	// Origin is who sent it.
	Origin Origin
	// Resumed is true when the call carries a confirm token.
	Resumed bool
	// Started is when the Invoker received it.
	Started time.Time
}

// Call is an Invocation with the type-erased Spec functions bound to its
// input. Stages read it; only the Invoker builds it (Bind).
type Call struct {
	Invocation
	App    *app.App
	Detail Detail
	// DryRun is Spec.DryRun(in): the call publishes no events.
	DryRun bool
	// Validate, Plan and Run are the Spec's functions bound to the input.
	// Plan is nil unless the Spec gates by Invoker.
	Validate func() error
	Plan     func(ctx context.Context) (Plan, error)
	Run      func(ctx context.Context) (any, error)
	// Classify turns Run's result into an Outcome (Classify[Out]).
	Classify func(v any, err error) Outcome
	// token is the input's confirm token and related the Spec's
	// Policy.Related, for the GateByInvoker gate. The token is unexported so
	// stages never see it.
	token   string
	related []string
}

// Handler runs a Call and returns Run's result unchanged.
type Handler func(ctx context.Context, c *Call) (any, error)

// Stage is one link of the Invoker's Chain of Responsibility. It does its
// part and calls next, or returns without calling it to stop the chain (the
// gate stage returns a *app.Pending). It must return the value next returned
// unchanged, so the caller's output is byte-identical.
type Stage interface {
	Handle(ctx context.Context, c *Call, next Handler) (any, error)
}

// StageFunc adapts a function to Stage.
type StageFunc func(ctx context.Context, c *Call, next Handler) (any, error)

// Handle calls f.
func (f StageFunc) Handle(ctx context.Context, c *Call, next Handler) (any, error) {
	return f(ctx, c, next)
}

// Invoker runs Calls. NewInvoker(Options) is a Template Method with this
// fixed skeleton:
//
//  1. validate: c.Validate; an error returns at once, without events.
//  2. stages: Options.Stages, in order (empty in phase 3; plugins'
//     permission rules in phase 5). A stage that stops the chain returns
//     its result as is, without events, like a validation error.
//  3. publish op.requested (unless the call is a dry run or the scope is
//     ScopeNone). With Meta.NativeReads it is published after step 5,
//     with the call's start time, and not at all for a native read step.
//  4. gate: with GateByInvoker, c.Plan then (*app.App).GateOperation; a
//     non-nil Pending stops here. With GateInRun, nothing.
//  5. run: c.Run.
//  6. audit: Auditor.AuditOperation with the Outcome, only with
//     Meta.AuditByInvoker, and never for a dry run, a read or a deferred
//     outcome. The audit.log line count of a call stays what it is today.
//  7. publish the outcome event (OutcomeEvent), unless the outcome is
//     deferred.
//
// An Emitter error never changes the result: events are notifications.
type Invoker interface {
	Invoke(ctx context.Context, c *Call) (any, error)
}

// InvokerFunc adapts a function to Invoker.
type InvokerFunc func(ctx context.Context, c *Call) (any, error)

// Invoke calls f.
func (f InvokerFunc) Invoke(ctx context.Context, c *Call) (any, error) { return f(ctx, c) }

// Options configure the Invoker (NewInvoker).
type Options struct {
	// Emitters gives the Emitter for an App's repository. Nil, or a nil
	// Emitter, publishes nothing.
	Emitters EmitterSource
	// Auditor writes the operation's audit line. Nil audits nothing.
	Auditor Auditor
	// Stages run after validate, in order.
	Stages []Stage
	// Now and NewID default to time.Now and NewOperationID; tests fix them.
	Now   func() time.Time
	NewID func() string
}

// Emitter publishes op.* events of one repository. The production one is in
// internal/command/deliver: notifysock.Deliver to the
// repository's store as store.RoleAppender, then the daemon's socket, with
// one stderr warning on a drop. The daemon's own calls use its bus.
type Emitter interface {
	// Emit fills e.Repo, and e.Worktree when worktree is true, then
	// publishes e. When the daemon refuses the worktree (unknown_worktree),
	// it sends e once more without it.
	Emit(ctx context.Context, e bus.Event, worktree bool) error
}

// EmitterSource returns the Emitter for a's repository, or nil when a has
// none (no repository, or the repository has no event store and must not get
// one).
type EmitterSource func(ctx context.Context, a *app.App) (Emitter, error)

// Auditor writes one audit line per finished call: time, client, class,
// action, operation name, operation ID (Outcome.OperationID), outcome kind
// and code. AuditLog writes it through policy.Gate.AuditOperation. The app
// method's own a.audit line, which hashes the exact reviewed summary, stays.
type Auditor interface {
	AuditOperation(a *app.App, inv Invocation, out Outcome)
}

// OutcomeKind is how a call ended.
type OutcomeKind string

// Outcome kinds and the event each publishes.
const (
	OutcomeApplied          OutcomeKind = "applied"           // op.applied
	OutcomeFailed           OutcomeKind = "failed"            // op.failed
	OutcomeSuggested        OutcomeKind = "suggested"         // op.suggested
	OutcomeAwaitingApproval OutcomeKind = "awaiting_approval" // op.awaiting_approval
	OutcomePrepared         OutcomeKind = "prepared"          // op.prepared
	// OutcomeDeferred: Run returned a native read request. The step
	// publishes no event and writes no audit line; the client's next call,
	// with the read results, is the one that publishes.
	OutcomeDeferred OutcomeKind = "deferred"
)

// Publishes reports whether k publishes an outcome event.
func (k OutcomeKind) Publishes() bool { return k != OutcomeDeferred }

// EventType returns the op.* type of k; "" for OutcomeDeferred.
func (k OutcomeKind) EventType() bus.Type {
	switch k {
	case OutcomeDeferred:
		return ""
	case OutcomeApplied:
		return bus.TypeOpApplied
	case OutcomeSuggested:
		return bus.TypeOpSuggested
	case OutcomeAwaitingApproval:
		return bus.TypeOpAwaitingApproval
	case OutcomePrepared:
		return bus.TypeOpPrepared
	default:
		return bus.TypeOpFailed
	}
}

// Outcome is how a call ended, as its event reports it.
type Outcome struct {
	Kind OutcomeKind
	// Status is the Pending status (suggest, needs_confirmation,
	// awaiting_approval, prepared), or "".
	Status string
	// Code is a fixed identifier on failed: see ErrorCode and Classify.
	Code string
	// ApprovalID and ExpiresAt come from the Pending's approval record.
	ApprovalID string
	ExpiresAt  time.Time
	// Links are what an applied result touched.
	Links []bus.OpLink
}

// OperationID is the ID the outcome's event and audit line use: the
// approval's ID when the gate issued or redeemed one, else the call's.
func (o Outcome) OperationID(c *Call) string {
	if o.ApprovalID != "" {
		return o.ApprovalID
	}
	return c.ID
}

// Failure codes besides policy.ErrorCode values and refusal statuses.
const (
	CodeError            = "error"
	CodeCancelled        = "cancelled"
	CodeTimeout          = "timeout"
	CodeUnexpectedResult = "unexpected_result"
	CodeUnknownStatus    = "unknown_status"
)

// ErrorCode is the fixed code of err: a policy.ErrorCode value, cancelled,
// timeout, or error. Never the error's text, which can hold paths, provider
// responses or secrets.
func ErrorCode(err error) string {
	switch {
	case policy.ErrorCode(err) != "":
		return policy.ErrorCode(err)
	case errors.Is(err, context.Canceled):
		return CodeCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return CodeTimeout
	default:
		return CodeError
	}
}

// Classify turns a Run result into an Outcome:
//
//   - a *native.Request error: deferred, which publishes nothing;
//   - any other error: failed, with ErrorCode;
//   - a *app.Pending: suggested, awaiting_approval or prepared by its
//     status, with its approval's ID and expiry;
//   - an Out or *Out (or nil): applied, with links;
//   - anything else: failed, with the value's "status" field as the code
//     (commit's {"status": "invalid"}, {"status": "blocked"}), or
//     unexpected_result.
//
// With Out = any every non-Pending value is applied.
func Classify[Out any](v any, err error, links func(Out) []bus.OpLink) Outcome {
	var nr *native.Request
	if errors.As(err, &nr) {
		return Outcome{Kind: OutcomeDeferred}
	}
	if err != nil {
		return Outcome{Kind: OutcomeFailed, Code: ErrorCode(err)}
	}
	if p, ok := v.(*app.Pending); ok {
		if p == nil {
			return Outcome{Kind: OutcomeApplied}
		}
		return pendingOutcome(p)
	}
	applied := func(o Out) Outcome {
		out := Outcome{Kind: OutcomeApplied}
		if links != nil {
			out.Links = links(o)
		}
		return out
	}
	switch o := v.(type) {
	case nil:
		var zero Out
		return applied(zero)
	case *Out:
		if o != nil {
			return applied(*o)
		}
		var zero Out
		return applied(zero)
	case Out:
		return applied(o)
	}
	if status := statusOf(v); status != "" {
		return Outcome{Kind: OutcomeFailed, Code: status}
	}
	return Outcome{Kind: OutcomeFailed, Code: CodeUnexpectedResult}
}

func pendingOutcome(p *app.Pending) Outcome {
	out := Outcome{Status: p.Status}
	if p.Approval != nil {
		out.ApprovalID = p.Approval.OperationID
		out.ExpiresAt = p.Approval.ExpiresAt
	}
	switch policy.Outcome(p.Status) {
	case policy.SuggestOnly:
		out.Kind = OutcomeSuggested
	case policy.NeedsConfirmation, policy.AwaitingApproval:
		out.Kind = OutcomeAwaitingApproval
	case policy.Prepared:
		out.Kind = OutcomePrepared
	default:
		out.Kind, out.Code = OutcomeFailed, CodeUnknownStatus
	}
	return out
}

// statusOf returns the string "status" field of a map or struct result.
func statusOf(v any) string {
	if m, ok := v.(map[string]any); ok {
		s, _ := m["status"].(string)
		return s
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer && !rv.IsNil() {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ""
	}
	b, err := json.Marshal(rv.Interface())
	if err != nil {
		return ""
	}
	var s struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(b, &s)
	return s.Status
}

// NewOperationID returns a new operation ID: "op_" and 32 lowercase hex
// characters from crypto/rand, the form of policy approval IDs.
func NewOperationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // Since Go 1.24, crypto/rand.Read never returns an error.
	return "op_" + hex.EncodeToString(b[:])
}

// Bind builds the Call for s and in. id is the operation ID to use when in
// carries no confirm token (NewOperationID when empty).
func Bind[In, Out any](a *app.App, s Spec[In, Out], in In, o Origin, id string, now time.Time) *Call {
	c := &Call{App: a, related: s.Policy.Related}
	c.Meta = s.Meta()
	c.Origin = o
	c.Started = now
	if s.Token != nil {
		if tok := *s.Token(&in); tok != "" {
			c.Resumed, c.token = true, tok
			if resumed, ok := policy.OperationIDOf(tok); ok {
				id = resumed
			}
		}
	}
	if id == "" {
		id = NewOperationID()
	}
	c.ID = id
	if s.Describe != nil {
		c.Detail = s.Describe(in)
	}
	if s.DryRun != nil {
		c.DryRun = s.DryRun(in)
	}
	c.Validate = func() error {
		if s.Validate == nil {
			return nil
		}
		return s.Validate(in)
	}
	if s.Plan != nil {
		c.Plan = func(ctx context.Context) (Plan, error) { return s.Plan(ctx, a, in) }
	}
	c.Run = func(ctx context.Context) (any, error) { return s.Run(ctx, a, in) }
	c.Classify = func(v any, err error) Outcome { return Classify(v, err, s.Links) }
	return c
}

// Invoke binds s and in and runs them through inv. It returns Run's result
// unchanged.
func Invoke[In, Out any](ctx context.Context, inv Invoker, a *app.App, s Spec[In, Out], in In, o Origin) (any, error) {
	return inv.Invoke(ctx, Bind(a, s, in, o, "", time.Now()))
}

// Publishes reports whether c publishes events at all.
func (c *Call) Publishes() bool {
	return !c.DryRun && c.Meta.Events != ScopeNone
}

// RequestedEvent returns c's op.requested event, without Repo and Worktree
// (the Emitter fills them). Its ID is new; pass it to OutcomeEvent as the
// outcome's cause. A NativeReads call passes c.Started as now, because the
// event is built after Run.
func (c *Call) RequestedEvent(now time.Time) (bus.Event, error) {
	p := c.payload()
	p.Resumed = c.Resumed
	return c.event(bus.TypeOpRequested, c.ID, c.Origin.Cause, p, now)
}

// OutcomeEvent returns the event for out, caused by the op.requested event
// requested. When out names an approval whose ID differs from c.ID (the first
// call of a gated operation, whose approval the gate issued just now), the
// event's CorrelationID is the approval's ID: every later event of the
// operation, its approval.* events included, uses that ID, and the timeline
// moves the op.requested event into that group through this event's
// CausationID.
func (c *Call) OutcomeEvent(out Outcome, requested string, now time.Time) (bus.Event, error) {
	p := c.payload()
	p.Status, p.Code, p.ApprovalID, p.Links = out.Status, out.Code, out.ApprovalID, out.Links
	if !out.ExpiresAt.IsZero() {
		t := out.ExpiresAt.UTC()
		p.ExpiresAt = &t
	}
	if d := now.Sub(c.Started); d > 0 {
		p.DurationMS = d.Milliseconds()
	}
	return c.event(out.Kind.EventType(), out.OperationID(c), requested, p, now)
}

func (c *Call) payload() bus.OpPayload {
	return bus.OpPayload{Operation: c.Meta.Name, Action: c.Meta.Action, Class: string(c.Meta.Class), Client: c.Origin.Client,
		Subject: c.Detail.Subject, Task: c.Detail.Task, Branch: c.Detail.Branch}
}

func (c *Call) event(typ bus.Type, correlation, causation string, p bus.OpPayload, now time.Time) (bus.Event, error) {
	raw, err := json.Marshal(p.Clamp())
	if err != nil {
		return bus.Event{}, err
	}
	return bus.Event{ID: bus.NewID(now), Type: typ, Source: c.Origin.Source, CorrelationID: correlation, CausationID: causation,
		Time: now.UTC(), SchemaVersion: bus.SchemaVersion, Payload: raw}, nil
}
