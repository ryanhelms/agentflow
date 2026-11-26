package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

var timelineNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// timelineFixture builds op.* and approval.* events the way the Invoker
// and the approval translator do (ADR, "Phase 3 event types").
type timelineFixture struct {
	t      *testing.T
	repo   bus.RepoID
	events []bus.Event
}

func (f *timelineFixture) add(e bus.Event, at time.Duration) bus.Event {
	f.t.Helper()
	e.ID = fmt.Sprintf("ev-%03d", len(f.events)+1)
	e.Seq = uint64(len(f.events) + 1)
	e.Repo = f.repo
	e.Time = timelineNow.Add(at)
	if e.Source == "" {
		e.Source = "cli"
	}
	f.events = append(f.events, e)
	return e
}

// op adds one op.* event. cause is the event that caused it, or "".
func (f *timelineFixture) op(typ bus.Type, id, cause string, at time.Duration, p bus.OpPayload) bus.Event {
	f.t.Helper()
	if p.Operation == "" {
		p.Operation = "commit"
	}
	if p.Client == "" {
		p.Client = "cli"
	}
	return f.add(bus.Event{Type: typ, Worktree: "main", CorrelationID: id, CausationID: cause, Payload: payload(f.t, p)}, at)
}

func (f *timelineFixture) approval(typ bus.Type, id string, at time.Duration, state bus.ApprovalState) bus.Event {
	f.t.Helper()
	a := bus.Approval{OperationID: id, Action: "push", Class: "remote", Client: "claude-code", IssuedAt: timelineNow.Add(-time.Hour), ExpiresAt: timelineNow.Add(time.Hour)}
	var body any
	switch typ {
	case bus.TypeApprovalRequested:
		body = bus.ApprovalRequested{Approval: a}
	case bus.TypeApprovalResolved:
		body = bus.ApprovalResolved{Approval: a, State: state, Previous: bus.StatePending}
	default:
		body = bus.ApprovalExpired{Approval: a, Previous: bus.StatePending}
	}
	return f.add(bus.Event{Type: typ, Source: "watch.approvals", CorrelationID: id, Payload: payload(f.t, body)}, at)
}

func (f *timelineFixture) state() TimelineState { return timelineOf(f.events, timelineNow) }

func operationByID(t *testing.T, st TimelineState, id string) TimelineOperation {
	t.Helper()
	for _, op := range st.Operations {
		if op.ID == id {
			return op
		}
	}
	t.Fatalf("no operation %s in %+v", id, st.Operations)
	return TimelineOperation{}
}

func stepStates(op TimelineOperation) string {
	var out []string
	for _, s := range op.Steps {
		out = append(out, s.State)
	}
	return strings.Join(out, " ")
}

const (
	opA = "op_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	opB = "op_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	opC = "op_cccccccccccccccccccccccccccccccc"
	opD = "op_dddddddddddddddddddddddddddddddd"
)

// A gated operation: the first request has its own ID, the gate answers
// with the approval's ID, the approval is granted and the resumed call
// applies. Everything ends in the approval's group, in order.
func TestTimelineGroupsAGatedOperation(t *testing.T) {
	f := &timelineFixture{t: t, repo: repoA}
	expires := timelineNow.Add(time.Hour)
	req := f.op(bus.TypeOpRequested, opA, "", -10*time.Minute, bus.OpPayload{Operation: "push", Action: "push", Class: "remote", Client: "claude-code", Subject: "Push issue-57", Task: "ryanhelms/agentflow#57", Branch: "issue-57"})
	f.op(bus.TypeOpAwaitingApproval, opB, req.ID, -10*time.Minute+time.Second, bus.OpPayload{Operation: "push", Client: "claude-code", Status: "awaiting_approval", ApprovalID: opB, ExpiresAt: &expires})
	f.approval(bus.TypeApprovalRequested, opB, -10*time.Minute+2*time.Second, "")

	st := f.state()
	if len(st.Operations) != 1 {
		t.Fatalf("the first request kept its own group: %+v", st.Operations)
	}
	op := st.Operations[0]
	if op.ID != opB || op.State != "awaiting_approval" || stepStates(op) != "requested awaiting_approval awaiting_approval" {
		t.Fatalf("waiting operation = %+v", op)
	}
	if op.Operation != "push" || op.Action != "push" || op.Class != "remote" || op.Client != "claude-code" || op.Worktree != "main" ||
		op.Subject != "Push issue-57" || op.Task != "ryanhelms/agentflow#57" || op.Branch != "issue-57" {
		t.Fatalf("description = %+v", op)
	}
	if op.Approval == nil || op.Approval.State != "pending" || op.Approval.ExpiresAt == nil || !op.Approval.ExpiresAt.Equal(expires) {
		t.Fatalf("approval = %+v", op.Approval)
	}
	if !op.Started.Equal(timelineNow.Add(-10*time.Minute)) || !op.Updated.Equal(timelineNow.Add(-10*time.Minute+2*time.Second)) {
		t.Fatalf("started %s, updated %s", op.Started, op.Updated)
	}
	if op.Steps[0].EventID != req.ID || op.Steps[0].Seq != 1 || op.Steps[0].Type != bus.TypeOpRequested {
		t.Fatalf("first step = %+v", op.Steps[0])
	}

	f.approval(bus.TypeApprovalResolved, opB, -9*time.Minute, bus.StateApproved)
	if op := f.state().Operations[0]; op.State != "approved" || op.Approval.State != "approved" {
		t.Fatalf("approved operation = %+v", op)
	}
	// The resumed call carries the token, so it uses the approval's ID.
	again := f.op(bus.TypeOpRequested, opB, "", -8*time.Minute, bus.OpPayload{Operation: "push", Client: "claude-code", Resumed: true})
	if op := f.state().Operations[0]; op.State != "requested" {
		t.Fatalf("resumed operation = %+v", op)
	}
	f.op(bus.TypeOpApplied, opB, again.ID, -8*time.Minute+time.Second, bus.OpPayload{Operation: "push", Client: "claude-code", Links: []bus.OpLink{
		{Kind: bus.OpLinkBranch, ID: "issue-57"},
		{Kind: bus.OpLinkPull, ID: "61", URL: "https://github.com/ryanhelms/agentflow/pull/61"},
		{Kind: bus.OpLinkPull, ID: "61", URL: "https://github.com/ryanhelms/agentflow/pull/61"},
		{Kind: bus.OpLinkCommit, ID: "9f8e7d6", URL: "javascript:alert(1)"},
		{Kind: bus.OpLinkDoc, ID: "docs/x", URL: "http://example.com/x"},
		{Kind: "", ID: "nameless"},
	}})
	// The watcher reports the consumed approval after the call finished.
	f.approval(bus.TypeApprovalResolved, opB, -8*time.Minute+2*time.Second, bus.StateConsumed)

	st = f.state()
	op = operationByID(t, st, opB)
	if len(st.Operations) != 1 || op.State != "applied" || op.Code != "" || op.Approval.State != "consumed" {
		t.Fatalf("applied operation = %+v", op)
	}
	if stepStates(op) != "requested awaiting_approval awaiting_approval approved requested applied consumed" {
		t.Fatalf("steps = %s", stepStates(op))
	}
	want := []bus.OpLink{{Kind: "branch", ID: "issue-57"}, {Kind: "pr", ID: "61", URL: "https://github.com/ryanhelms/agentflow/pull/61"}, {Kind: "commit", ID: "9f8e7d6"}, {Kind: "doc", ID: "docs/x"}}
	if fmt.Sprint(op.Links) != fmt.Sprint(want) {
		t.Fatalf("links = %+v", op.Links)
	}
}

func TestTimelineStates(t *testing.T) {
	f := &timelineFixture{t: t, repo: repoA}
	// Applied without a gate.
	r := f.op(bus.TypeOpRequested, opA, "", -50*time.Minute, bus.OpPayload{})
	f.op(bus.TypeOpApplied, opA, r.ID, -50*time.Minute, bus.OpPayload{})
	// Failed: a code, never text.
	r = f.op(bus.TypeOpRequested, opB, "", -40*time.Minute, bus.OpPayload{})
	f.op(bus.TypeOpFailed, opB, r.ID, -40*time.Minute, bus.OpPayload{Code: "blocked"})
	// Suggested.
	r = f.op(bus.TypeOpRequested, opC, "", -30*time.Minute, bus.OpPayload{})
	f.op(bus.TypeOpSuggested, opC, r.ID, -30*time.Minute, bus.OpPayload{Status: "suggest", Code: "suggest_only"})
	// Waiting, then cancelled through approval.resolved (a "no" at the prompt).
	r = f.op(bus.TypeOpRequested, opD, "", -20*time.Minute, bus.OpPayload{})
	f.op(bus.TypeOpAwaitingApproval, opD, r.ID, -20*time.Minute, bus.OpPayload{Status: "needs_confirmation", ApprovalID: opD})
	f.approval(bus.TypeApprovalResolved, opD, -19*time.Minute, bus.StateCancelled)
	// Prepared.
	const opE = "op_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	r = f.op(bus.TypeOpRequested, opE, "", -10*time.Minute, bus.OpPayload{})
	f.op(bus.TypeOpPrepared, opE, r.ID, -10*time.Minute, bus.OpPayload{Status: "prepared"})
	// Waiting, then expired.
	const opF = "op_ffffffffffffffffffffffffffffffff"
	r = f.op(bus.TypeOpRequested, opF, "", -5*time.Minute, bus.OpPayload{})
	f.op(bus.TypeOpAwaitingApproval, opF, r.ID, -5*time.Minute, bus.OpPayload{Status: "awaiting_approval", ApprovalID: opF})
	f.approval(bus.TypeApprovalExpired, opF, -time.Minute, "")
	// Still running.
	const opG = "op_00000000000000000000000000000000"
	f.op(bus.TypeOpRequested, opG, "", 0, bus.OpPayload{})

	st := f.state()
	var got []string
	for _, op := range st.Operations {
		got = append(got, op.ID[3:4]+":"+op.State+":"+op.Code)
	}
	// Newest first by the last step.
	if want := "0:requested: f:expired: e:prepared: d:cancelled: c:suggested:suggest_only b:failed:blocked a:applied:"; strings.Join(got, " ") != want {
		t.Fatalf("states = %v, want %s", got, want)
	}
	if st.Truncated {
		t.Fatal("a short timeline is marked truncated")
	}
	if op := operationByID(t, st, opD); op.Approval == nil || op.Approval.State != "cancelled" {
		t.Fatalf("cancelled approval = %+v", op.Approval)
	}
	if op := operationByID(t, st, opB); op.Steps[1].Code != "blocked" || op.Steps[0].Code != "" {
		t.Fatalf("failed steps = %+v", op.Steps)
	}
}

// Approval events come from a file watcher and can arrive after the op.*
// event of the call that used the approval. They never undo that result.
func TestTimelineLateApprovalEventsKeepTheResult(t *testing.T) {
	f := &timelineFixture{t: t, repo: repoA}
	r := f.op(bus.TypeOpRequested, opA, "", -5*time.Minute, bus.OpPayload{Resumed: true})
	f.op(bus.TypeOpApplied, opA, r.ID, -5*time.Minute, bus.OpPayload{})
	f.approval(bus.TypeApprovalRequested, opA, -4*time.Minute, "")
	f.approval(bus.TypeApprovalResolved, opA, -4*time.Minute, bus.StateApproved)
	f.approval(bus.TypeApprovalResolved, opA, -4*time.Minute, bus.StateConsumed)
	f.approval(bus.TypeApprovalExpired, opA, -3*time.Minute, "")
	if op := f.state().Operations[0]; op.State != "applied" {
		t.Fatalf("state = %s after late approval events", op.State)
	}
	// A failed call stays failed when the approval is granted afterwards,
	// and ends when the approval is cancelled.
	r = f.op(bus.TypeOpRequested, opB, "", -2*time.Minute, bus.OpPayload{Resumed: true})
	f.op(bus.TypeOpFailed, opB, r.ID, -2*time.Minute, bus.OpPayload{Code: "approval_pending"})
	f.approval(bus.TypeApprovalResolved, opB, -time.Minute, bus.StateApproved)
	if op := operationByID(t, f.state(), opB); op.State != "failed" || op.Code != "approval_pending" {
		t.Fatalf("failed operation = %+v", op)
	}
	f.approval(bus.TypeApprovalResolved, opB, 0, bus.StateCancelled)
	if op := operationByID(t, f.state(), opB); op.State != "cancelled" || op.Code != "" {
		t.Fatalf("cancelled operation = %+v", op)
	}
}

// An approval with no op.* event (an older binary) is shown with the
// approval's action as its operation.
func TestTimelineApprovalWithoutOperation(t *testing.T) {
	f := &timelineFixture{t: t, repo: repoA}
	f.approval(bus.TypeApprovalRequested, opA, -time.Minute, "")
	op := f.state().Operations[0]
	if op.Operation != "push" || op.Action != "push" || op.Class != "remote" || op.Client != "claude-code" || op.State != "awaiting_approval" || op.Worktree != "" {
		t.Fatalf("approval-only operation = %+v", op)
	}
	f.approval(bus.TypeApprovalResolved, opB, 0, bus.StateConsumed)
	if op := operationByID(t, f.state(), opB); op.State != "approved" || op.Approval.State != "consumed" {
		t.Fatalf("consumed-only operation = %+v", op)
	}
}

func TestTimelineCapsAndAge(t *testing.T) {
	f := &timelineFixture{t: t, repo: repoA}
	// One operation with more steps than the cap keeps the newest.
	for i := range MaxTimelineSteps + 3 {
		f.op(bus.TypeOpRequested, opA, "", time.Duration(i-100)*time.Second, bus.OpPayload{Resumed: i > 0})
	}
	op := f.state().Operations[0]
	if len(op.Steps) != MaxTimelineSteps || !op.Truncated || op.Steps[0].Seq != 4 || !op.Started.Equal(timelineNow.Add(-100*time.Second)) {
		t.Fatalf("capped steps = %d, truncated %v, first seq %d, started %s", len(op.Steps), op.Truncated, op.Steps[0].Seq, op.Started)
	}
	// An operation whose last step is older than TimelineAge is dropped;
	// one just inside stays.
	f.op(bus.TypeOpRequested, opB, "", -TimelineAge-time.Minute, bus.OpPayload{})
	f.op(bus.TypeOpRequested, opC, "", -TimelineAge+time.Minute, bus.OpPayload{})
	st := f.state()
	if len(st.Operations) != 2 || st.Operations[1].ID != opC || st.Truncated {
		t.Fatalf("aged timeline = %+v", st.Operations)
	}
	// More operations than the cap: the newest stay, and the state says so.
	for i := range MaxTimelineOperations + 5 {
		f.op(bus.TypeOpRequested, fmt.Sprintf("op_%032x", i+1), "", time.Duration(i)*time.Second, bus.OpPayload{})
	}
	st = f.state()
	if len(st.Operations) != MaxTimelineOperations || !st.Truncated || st.Operations[0].ID != fmt.Sprintf("op_%032x", MaxTimelineOperations+5) {
		t.Fatalf("capped timeline = %d operations, truncated %v, first %s", len(st.Operations), st.Truncated, st.Operations[0].ID)
	}
}

// Events that cannot belong to an operation are left out, and payload text
// is clamped whatever the producer sent.
func TestTimelineReadsDefensively(t *testing.T) {
	f := &timelineFixture{t: t, repo: repoA}
	f.add(bus.Event{Type: bus.TypeOpRequested, Payload: payload(t, bus.OpPayload{Operation: "commit", Client: "cli"})}, 0) // no correlation ID
	f.add(bus.Event{Type: bus.TypeOpRequested, CorrelationID: strings.Repeat("x", maxTimelineID+1), Payload: payload(t, bus.OpPayload{})}, 0)
	f.add(bus.Event{Type: bus.TypeOpRequested, CorrelationID: opA, Payload: json.RawMessage(`"text"`)}, 0)
	f.add(bus.Event{Type: bus.TypeApprovalResolved, Payload: json.RawMessage(`{"operation_id":"` + opB + `","state":"unknown"}`)}, 0)
	f.add(bus.Event{Type: bus.TypeGitHeadChanged, CorrelationID: opC, Payload: json.RawMessage(`{}`)}, 0)
	// An outcome that names a request that is not there moves nothing.
	f.op(bus.TypeOpFailed, opD, "ev-missing", 0, bus.OpPayload{Code: "error", Subject: strings.Repeat("s", 400) + "\nsecond line"})
	st := f.state()
	if len(st.Operations) != 1 || st.Operations[0].ID != opD {
		t.Fatalf("operations = %+v", st.Operations)
	}
	if got := st.Operations[0].Subject; len(got) != bus.MaxPayloadText || strings.Contains(got, "second") {
		t.Fatalf("subject has %d bytes", len(got))
	}
	raw, err := json.Marshal(timelineOf(nil, timelineNow))
	if err != nil || string(raw) != `{"operations":[]}` {
		t.Fatalf("empty state = %s, %v", raw, err)
	}
}

func TestTimelineProjectionKeys(t *testing.T) {
	ev := &repoEvents{id: repoA, worktrees: map[bus.WorktreeID]registeredWorktree{}}
	p := &timelineProjection{repo: ev, now: func() time.Time { return timelineNow }}
	for _, typ := range []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed, bus.TypeApprovalResolved} {
		if keys := p.Keys(bus.Event{Type: typ, Repo: repoA, Worktree: "main"}); len(keys) != 1 || keys[0] != (ProjectionKey{Repo: repoA}) {
			t.Fatalf("%s keys = %v", typ, keys)
		}
	}
	for _, e := range []bus.Event{{Type: bus.TypeOpApplied, Repo: repoB}, {Type: bus.TypeCommitCreated, Repo: repoA}} {
		if keys := p.Keys(e); keys != nil {
			t.Fatalf("%s of %s keys = %v", e.Type, e.Repo, keys)
		}
	}
	// No store yet, another repository, or a worktree key.
	if raw, err := p.State(context.Background(), ProjectionKey{Repo: repoA}); err != nil || string(raw) != `{"operations":[]}` {
		t.Fatalf("state without a store = %s, %v", raw, err)
	}
	for _, key := range []ProjectionKey{{Repo: repoB}, {Repo: repoA, Worktree: "main"}} {
		if raw, _ := p.State(context.Background(), key); string(raw) != "null" {
			t.Fatalf("state of %+v = %s", key, raw)
		}
	}
}

// The timeline reaches a stream that asks for op.** and approval.** (the
// console's topics) as a snapshot and as deltas, built from the
// repository's store.
func TestTimelineOverEventStream(t *testing.T) {
	c := newLiveConsole(t)
	topics := "?topic=" + url.QueryEscape("*/*/op.**") + "&topic=" + url.QueryEscape("*/*/approval.**")
	stream, _ := c.open(t, topics)
	snap := decode[SnapshotFrame](t, stream.expect(t, FrameSnapshot))
	if st, ok := findState(snap.States, ProjectionTimeline, ""); !ok || string(st.State) != `{"operations":[]}` {
		t.Fatalf("snapshot = %+v", snap.States)
	}
	op := func(typ bus.Type, id, cause string, p bus.OpPayload) bus.Event {
		p.Operation, p.Client = "commit", "claude-code"
		return c.publish(t, bus.Event{Type: typ, Source: "mcp:claude-code", Repo: c.ev.id, Worktree: c.worktree, CorrelationID: id, CausationID: cause, Payload: payload(t, p)})
	}
	req := op(bus.TypeOpRequested, opA, "", bus.OpPayload{Subject: "feat: show a live operation timeline"})
	st, d := nextState[TimelineState](t, stream, ProjectionTimeline, "")
	if len(st.Operations) != 1 || st.Operations[0].State != "requested" || st.Operations[0].Worktree != c.worktree || d.Event.Type != bus.TypeOpRequested {
		t.Fatalf("after the request: %+v", st)
	}
	if st.Operations[0].Steps[0].Seq == 0 || st.Operations[0].Steps[0].EventID != req.ID {
		t.Fatalf("the step does not locate its stored event: %+v", st.Operations[0].Steps[0])
	}
	op(bus.TypeOpAwaitingApproval, opB, req.ID, bus.OpPayload{Status: "needs_confirmation", ApprovalID: opB})
	st, _ = nextState[TimelineState](t, stream, ProjectionTimeline, "")
	if len(st.Operations) != 1 || st.Operations[0].ID != opB || st.Operations[0].State != "awaiting_approval" || len(st.Operations[0].Steps) != 2 {
		t.Fatalf("after the gate: %+v", st)
	}
	now := time.Now().UTC()
	c.publish(t, bus.Event{Type: bus.TypeApprovalResolved, Repo: c.ev.id, Payload: payload(t, bus.ApprovalResolved{
		Approval: bus.Approval{OperationID: opB, Action: "commit", Class: "local", IssuedAt: now, ExpiresAt: now.Add(time.Hour)}, State: bus.StateCancelled, Previous: bus.StatePending})})
	st, d = nextState[TimelineState](t, stream, ProjectionTimeline, "")
	if st.Operations[0].State != "cancelled" || d.Event.Type != bus.TypeApprovalResolved {
		t.Fatalf("after the cancellation: %+v", st)
	}

	// A new stream gets the same state in its snapshot, from the store.
	again, _ := c.open(t, topics)
	snap = decode[SnapshotFrame](t, again.expect(t, FrameSnapshot))
	fresh, ok := findState(snap.States, ProjectionTimeline, "")
	var rebuilt TimelineState
	if !ok || json.Unmarshal(fresh.State, &rebuilt) != nil || len(rebuilt.Operations) != 1 || rebuilt.Operations[0].State != "cancelled" || len(rebuilt.Operations[0].Steps) != 3 {
		t.Fatalf("rebuilt snapshot = %s", fresh.State)
	}

	// A stream that does not ask for operations or approvals gets no timeline.
	other, _ := c.open(t, "?topic="+url.QueryEscape("*/*/gh.**"))
	snap = decode[SnapshotFrame](t, other.expect(t, FrameSnapshot))
	if _, ok := findState(snap.States, ProjectionTimeline, ""); ok {
		t.Fatalf("a stream without op.** got the timeline: %+v", snap.States)
	}
}
