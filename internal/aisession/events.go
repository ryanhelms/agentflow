package aisession

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// events publishes a session's ai.session.* events through Options.Emit.
// Payloads carry IDs, codes, counts and kinds only. ai.session.output is
// coalesced (an Aggregator): at most one every OutputEventInterval.
type events struct {
	emit  func(ctx context.Context, e bus.Event) error
	now   func() time.Time
	repo  bus.RepoID
	wt    bus.WorktreeID
	id    ID
	mu    sync.Mutex
	seq   int64
	n     int
	kinds []string
	last  time.Time
	timer *time.Timer
}

func newEvents(o Options, s Session) *events {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &events{emit: o.Emit, now: now, repo: bus.RepoID(s.Repo), wt: bus.WorktreeID(s.WorktreeID), id: s.ID}
}

func (e *events) send(ctx context.Context, t bus.Type, payload any) {
	if e == nil || e.emit == nil {
		return
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ev := bus.Event{Type: t, Repo: e.repo, Worktree: e.wt, Source: "cli", Time: e.now().UTC(), Payload: b}
	_ = e.emit(context.WithoutCancel(ctx), ev)
}

func (e *events) started(ctx context.Context, s Session) {
	e.send(ctx, bus.TypeAISessionStarted, bus.AISessionStarted{
		Session: string(s.ID), Harness: s.Harness, StartedBy: s.StartedBy.Kind, Client: SanitizeLine(s.StartedBy.Client, 64), Ceiling: s.Ceiling,
		Kind: s.Kind,
	})
}

func (e *events) changed(ctx context.Context, s Session, previous State) {
	e.send(ctx, bus.TypeAISessionChanged, bus.AISessionChanged{Session: string(s.ID), State: string(s.State), Previous: string(previous), Code: s.Code})
}

func (e *events) asked(ctx context.Context, a AskRecord) {
	e.send(ctx, bus.TypeAISessionAsked, bus.AISessionAsked{Session: string(a.Session), Ask: a.ID, Kind: string(a.Kind), ToolKind: string(a.ToolKind), Reason: a.Reason})
}

func (e *events) answered(ctx context.Context, ask string, v Verdict, by string) {
	e.send(ctx, bus.TypeAISessionAnswered, bus.AISessionAnswered{Session: string(e.id), Ask: ask, Decision: string(v.Decision), By: by, Handler: v.Handler, Code: v.Code})
}

func (e *events) ended(ctx context.Context, s Session) {
	var d int64
	if s.EndedAt != nil {
		d = s.EndedAt.Sub(s.CreatedAt).Milliseconds()
	}
	c := s.Counters
	e.send(ctx, bus.TypeAISessionEnded, bus.AISessionEnded{
		Session: string(s.ID), Code: s.Code, DurationMS: d, ToolCalls: c.ToolCalls, Asks: c.Asks,
		Allowed: c.Allowed, Denied: c.Denied, Unasked: c.Unasked,
	})
}

// output records new items and sends ai.session.output at most every
// OutputEventInterval.
func (e *events) output(ctx context.Context, items []Item) {
	if e == nil || e.emit == nil || len(items) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, it := range items {
		e.seq = max(e.seq, it.Seq)
		e.n++
		if !slices.Contains(e.kinds, it.Kind) {
			e.kinds = append(e.kinds, it.Kind)
		}
	}
	wait := OutputEventInterval - e.now().Sub(e.last)
	if wait <= 0 {
		e.flushLocked(ctx)
		return
	}
	if e.timer == nil {
		e.timer = time.AfterFunc(wait, func() {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.timer = nil
			e.flushLocked(ctx)
		})
	}
}

// flush sends what is pending at once (the session ends).
func (e *events) flush(ctx context.Context) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	e.flushLocked(ctx)
}

func (e *events) flushLocked(ctx context.Context) {
	if e.n == 0 {
		return
	}
	p := bus.AISessionOutput{Session: string(e.id), Seq: e.seq, Items: e.n, Kinds: e.kinds}
	e.n, e.kinds, e.last = 0, nil, e.now()
	e.send(ctx, bus.TypeAISessionOutput, p)
}
