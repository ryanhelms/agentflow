package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
)

// The timeline projection (lane 3.5). The contract is in the ADR, section
// "Phase 3 timeline projection"; the shapes are in
// projection_timeline_state.go. The state is an event-sourced read model:
// it is rebuilt from the repository's stored op.* and approval.* events on
// every such event, so a missed event is repaired by the next one or by a
// snapshot.

func init() {
	projectionExtensions = append(projectionExtensions, func(r *repoEvents) Projection {
		return &timelineProjection{repo: r, now: time.Now}
	})
}

// timelineWindow is how many of the newest stored events the projection
// reads. The scan never reads the whole store.
const timelineWindow = store.MaxReplay

// maxTimelineID bounds an operation ID taken from an event. A real one is
// "op_" and 32 hex characters; anything longer is not an operation.
const maxTimelineID = 128

// Step states that only approval events produce. The op.* states are the
// type's last word: requested, suggested, awaiting_approval, prepared,
// applied, failed.
const (
	timelineRequested = "requested"
	timelineSuggested = "suggested"
	timelineAwaiting  = "awaiting_approval"
	timelinePrepared  = "prepared"
	timelineApplied   = "applied"
	timelineFailed    = "failed"
	timelineApproved  = "approved"
	timelineCancelled = "cancelled"
	timelineExpired   = "expired"
	// timelineConsumed is a step state only: the approval was used. The
	// operation's own state comes from the op.* event of the call that
	// used it.
	timelineConsumed = "consumed"
)

// timelineProjection keeps one TimelineState per repository.
type timelineProjection struct {
	repo *repoEvents
	now  func() time.Time
}

func (p *timelineProjection) Name() string { return ProjectionTimeline }

func (p *timelineProjection) Topics() []bus.Topic {
	return []bus.Topic{
		{Repo: p.repo.id, Worktree: bus.Any, Types: "op.**"},
		{Repo: p.repo.id, Worktree: bus.Any, Types: "approval.**"},
		{Repo: p.repo.id, Worktree: bus.Any, Types: bus.Pattern(bus.TypePluginAnnotationAdded)},
	}
}

func (p *timelineProjection) Keys(e bus.Event) []ProjectionKey {
	if e.Repo != p.repo.id || !bus.MatchAny(p.Topics(), e) {
		return nil
	}
	return p.AllKeys()
}

func (p *timelineProjection) AllKeys() []ProjectionKey {
	return []ProjectionKey{{Repo: p.repo.id}}
}

func (p *timelineProjection) State(ctx context.Context, key ProjectionKey) (json.RawMessage, error) {
	if key.Repo != p.repo.id || key.Worktree != "" {
		return jsonNull, nil
	}
	events, cut, err := p.events(ctx)
	if err != nil {
		return nil, err
	}
	st := timelineOf(events, p.now())
	attachAnnotations(&st, events)
	st.Truncated = st.Truncated || cut
	return json.Marshal(st)
}

// events reads the repository's newest timelineWindow stored events and
// keeps the op.*, approval.* and plugin.annotation.added ones, oldest
// first. cut is true when older
// events exist that were not read.
func (p *timelineProjection) events(ctx context.Context) (events []bus.Event, cut bool, err error) {
	s := p.repo.store
	if s == nil {
		return nil, false, nil
	}
	head, err := s.Head(ctx)
	if err != nil {
		return nil, false, err
	}
	from := bus.Cursor{}
	if newest := head[p.repo.id]; newest > timelineWindow {
		from[p.repo.id] = newest - timelineWindow
		cut = true
	}
	topics := p.Topics()
	read := func(from bus.Cursor) error {
		for e, err := range s.ReadFrom(ctx, from) {
			if err != nil {
				return err
			}
			// The store holds one repository; the check keeps the
			// timeline to it whatever a store returns.
			if e.Repo == p.repo.id && bus.MatchAny(topics, e) {
				events = append(events, e)
			}
		}
		return nil
	}
	err = read(from)
	if errors.Is(err, store.ErrCursorExpired) {
		// Fewer events are retained than the window: read what is left.
		events = events[:0]
		err = read(bus.Cursor{})
	}
	if err != nil {
		return nil, false, err
	}
	return events, cut, nil
}

// timelineEvent is one op.* or approval.* event, decoded.
type timelineEvent struct {
	bus.Event
	// group is the operation ID the event belongs to.
	group string
	// op is set for op.* events, approval for approval.* events.
	op       *bus.OpPayload
	approval *bus.Approval
	// state is the step's state.
	state string
	order int
}

// timelineOf builds the state from events in stored order (oldest first).
// It groups them by operation ID, moves a gated operation's first request
// into its approval's group (ADR, "Grouping by correlation ID"), drops
// operations older than TimelineAge and caps the lists.
func timelineOf(events []bus.Event, now time.Time) TimelineState {
	groups := map[string][]*timelineEvent{}
	requested := map[string]*timelineEvent{} // op.requested events by event ID
	for i, e := range events {
		te, ok := decodeTimelineEvent(e)
		if !ok {
			continue
		}
		te.order = i
		groups[te.group] = append(groups[te.group], te)
		if e.Type == bus.TypeOpRequested && e.ID != "" {
			requested[e.ID] = te
		}
	}
	// An outcome whose cause is an op.requested of another group takes
	// that request with it; the group it leaves goes when it is empty.
	for _, e := range events {
		if e.CausationID == "" || e.Type == bus.TypeOpRequested || !isOpType(e.Type) {
			continue
		}
		req := requested[e.CausationID]
		to := timelineGroup(e.CorrelationID)
		if req == nil || to == "" || req.group == to {
			continue
		}
		if _, ok := groups[to]; !ok {
			continue // the outcome itself was not usable
		}
		from := groups[req.group]
		kept := from[:0]
		for _, other := range from {
			if other != req {
				kept = append(kept, other)
			}
		}
		if len(kept) == 0 {
			delete(groups, req.group)
		} else {
			groups[req.group] = kept
		}
		req.group = to
		groups[to] = append(groups[to], req)
	}
	st := TimelineState{Operations: []TimelineOperation{}}
	oldest := now.Add(-TimelineAge)
	for id, group := range groups {
		sort.SliceStable(group, func(i, j int) bool { return group[i].order < group[j].order })
		op := timelineOperation(id, group)
		if op.Updated.Before(oldest) {
			continue
		}
		st.Operations = append(st.Operations, op)
	}
	sort.Slice(st.Operations, func(i, j int) bool {
		a, b := st.Operations[i], st.Operations[j]
		if !a.Updated.Equal(b.Updated) {
			return a.Updated.After(b.Updated)
		}
		return a.ID > b.ID
	})
	if len(st.Operations) > MaxTimelineOperations {
		st.Operations = st.Operations[:MaxTimelineOperations]
		st.Truncated = true
	}
	return st
}

func isOpType(t bus.Type) bool {
	switch t {
	case bus.TypeOpRequested, bus.TypeOpSuggested, bus.TypeOpAwaitingApproval, bus.TypeOpPrepared, bus.TypeOpApplied, bus.TypeOpFailed:
		return true
	}
	return false
}

// timelineGroup is id when it can name an operation, else "".
func timelineGroup(id string) string {
	if id == "" || len(id) > maxTimelineID {
		return ""
	}
	return id
}

// decodeTimelineEvent reads e's payload. An event of another type, without
// an operation ID or with a payload that does not decode is left out.
func decodeTimelineEvent(e bus.Event) (*timelineEvent, bool) {
	te := &timelineEvent{Event: e}
	if isOpType(e.Type) {
		var p bus.OpPayload
		if json.Unmarshal(e.Payload, &p) != nil {
			return nil, false
		}
		p = p.Clamp()
		te.op, te.group = &p, timelineGroup(e.CorrelationID)
		te.state = string(e.Type)[len("op."):]
		return te, te.group != ""
	}
	var a bus.Approval
	switch e.Type {
	case bus.TypeApprovalRequested:
		var p bus.ApprovalRequested
		if json.Unmarshal(e.Payload, &p) != nil {
			return nil, false
		}
		a, te.state = p.Approval, timelineAwaiting
	case bus.TypeApprovalResolved:
		var p bus.ApprovalResolved
		if json.Unmarshal(e.Payload, &p) != nil {
			return nil, false
		}
		a = p.Approval
		switch p.State {
		case bus.StateApproved:
			te.state = timelineApproved
		case bus.StateCancelled:
			te.state = timelineCancelled
		case bus.StateConsumed:
			te.state = timelineConsumed
		default:
			return nil, false
		}
	case bus.TypeApprovalExpired:
		var p bus.ApprovalExpired
		if json.Unmarshal(e.Payload, &p) != nil {
			return nil, false
		}
		a, te.state = p.Approval, timelineExpired
	default:
		return nil, false
	}
	te.approval, te.group = &a, timelineGroup(a.OperationID)
	return te, te.group != ""
}

// timelineOperation builds one operation from its events, oldest first.
func timelineOperation(id string, group []*timelineEvent) TimelineOperation {
	op := TimelineOperation{ID: id, Steps: make([]TimelineStep, 0, len(group))}
	described := false
	seen := map[bus.OpLink]bool{}
	for _, te := range group {
		step := TimelineStep{EventID: te.ID, Seq: te.Seq, Type: te.Type, Time: te.Time.UTC(), State: te.state}
		if p := te.op; p != nil {
			if !described {
				op.Operation, op.Action, op.Class, op.Client = p.Operation, p.Action, p.Class, p.Client
				described = true
			}
			if op.Worktree == "" {
				op.Worktree = te.Worktree
			}
			if op.Subject == "" {
				op.Subject = p.Subject
			}
			if op.Task == "" {
				op.Task = p.Task
			}
			if op.Branch == "" {
				op.Branch = p.Branch
			}
			if te.Type == bus.TypeOpFailed || te.Type == bus.TypeOpSuggested {
				step.Code = p.Code
			}
			for _, l := range p.Links {
				l.URL = httpsURL(l.URL)
				key := bus.OpLink{Kind: l.Kind, ID: l.ID}
				if l.Kind == "" || l.ID == "" || seen[key] || len(op.Links) >= bus.MaxPayloadNames {
					continue
				}
				seen[key] = true
				op.Links = append(op.Links, l)
			}
			// A waiting operation names its approval before the
			// approval's own event arrives.
			if te.Type == bus.TypeOpAwaitingApproval && p.ApprovalID != "" && op.Approval == nil {
				op.Approval = &TimelineApproval{State: string(bus.StatePending), ExpiresAt: utcTime(p.ExpiresAt)}
			}
		}
		if a := te.approval; a != nil {
			expires := a.ExpiresAt
			op.Approval = &TimelineApproval{State: approvalRecordState(te.state), ExpiresAt: utcTime(&expires)}
		}
		op.State, op.Code = nextTimelineState(op.State, op.Code, te, step.Code)
		op.Steps = append(op.Steps, step)
	}
	if !described {
		// Approval events only: an operation run before phase 3, or by
		// an older binary (ADR, grouping rule 3).
		for _, te := range group {
			if a := te.approval; a != nil {
				op.Operation, op.Action, op.Class, op.Client = a.Action, a.Action, a.Class, a.Client
				break
			}
		}
	}
	op.Started, op.Updated = op.Steps[0].Time, op.Steps[0].Time
	for _, step := range op.Steps {
		if step.Time.Before(op.Started) {
			op.Started = step.Time
		}
		if step.Time.After(op.Updated) {
			op.Updated = step.Time
		}
	}
	if len(op.Steps) > MaxTimelineSteps {
		op.Steps = op.Steps[len(op.Steps)-MaxTimelineSteps:]
		op.Truncated = true
	}
	return op
}

// nextTimelineState is the operation's state and code after step te.
//
// An op.* step always decides: it is what a call reported. An approval
// step decides only where it can still be true, because approval events
// come from a file watcher and may arrive after the op.* event of the call
// that used the approval:
//
//   - cancelled and expired end an operation that has not been applied;
//   - approved moves a waiting operation on;
//   - a new request makes an operation wait when nothing ran yet;
//   - consumed never changes the state.
func nextTimelineState(state, code string, te *timelineEvent, stepCode string) (string, string) {
	if te.op != nil {
		return te.state, stepCode
	}
	switch te.state {
	case timelineCancelled, timelineExpired:
		if state != timelineApplied {
			return te.state, ""
		}
	case timelineApproved:
		if state == "" || state == timelineRequested || state == timelineAwaiting {
			return te.state, ""
		}
	case timelineAwaiting:
		if state == "" || state == timelineRequested {
			return te.state, ""
		}
	}
	if state == "" {
		// A lone approval.resolved{consumed}: the approval was used, and
		// no op.* event says how the operation ended.
		return timelineApproved, ""
	}
	return state, code
}

// approvalRecordState maps a step state back to the approval record's.
func approvalRecordState(step string) string {
	switch step {
	case timelineAwaiting:
		return string(bus.StatePending)
	case timelineConsumed:
		return string(bus.StateConsumed)
	}
	return step // approved, cancelled, expired
}

func utcTime(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	at := t.UTC()
	return &at
}

// httpsURL is raw when it is an absolute https URL, else "". The console
// renders a link only for a URL that passes.
func httpsURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return raw
}

// attachAnnotations adds each plugin.annotation.added event in events
// (oldest first) to the operation its operation_id names, keeping the
// newest MaxTimelineAnnotations, oldest first. An annotation for an
// operation outside st is not shown. Text and URL are plugin text: clamped,
// the URL kept only when it is https.
func attachAnnotations(st *TimelineState, events []bus.Event) {
	index := map[string]int{}
	for i, op := range st.Operations {
		index[op.ID] = i
	}
	for _, e := range events {
		if e.Type != bus.TypePluginAnnotationAdded {
			continue
		}
		var p bus.PluginAnnotationAdded
		if json.Unmarshal(e.Payload, &p) != nil {
			continue
		}
		p = p.Clamp()
		i, ok := index[p.OperationID]
		if !ok || p.Plugin == "" || p.Text == "" {
			continue
		}
		if p.Level != bus.AnnotationWarning {
			p.Level = bus.AnnotationInfo
		}
		op := &st.Operations[i]
		op.Annotations = append(op.Annotations, TimelineAnnotation{EventID: e.ID, Plugin: p.Plugin, Text: p.Text,
			URL: httpsURL(p.URL), Level: p.Level, Time: e.Time.UTC()})
		if n := len(op.Annotations) - MaxTimelineAnnotations; n > 0 {
			op.Annotations = append([]TimelineAnnotation(nil), op.Annotations[n:]...)
		}
	}
}
