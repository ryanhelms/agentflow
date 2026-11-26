package webui

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/watch"
)

// approvalsProjection keeps one ApprovalsState per repository. It reads the
// approval records (ApprovalSource) on every approval.* event, so the
// records, not the events, are the source of truth.
type approvalsProjection struct {
	repo   *repoEvents
	source func() ApprovalSource // nil result: no journal reader yet
	now    func() time.Time
}

func (a *approvalsProjection) Name() string { return ProjectionApprovals }

func (a *approvalsProjection) Topics() []bus.Topic {
	return []bus.Topic{{Repo: a.repo.id, Worktree: bus.Any, Types: "approval.**"}}
}

func (a *approvalsProjection) Keys(e bus.Event) []ProjectionKey {
	if e.Repo != a.repo.id || !bus.MatchAny(a.Topics(), e) {
		return nil
	}
	return a.AllKeys()
}

func (a *approvalsProjection) AllKeys() []ProjectionKey {
	return []ProjectionKey{{Repo: a.repo.id}}
}

func (a *approvalsProjection) State(ctx context.Context, key ProjectionKey) (json.RawMessage, error) {
	if key.Repo != a.repo.id || key.Worktree != "" {
		return jsonNull, nil
	}
	var records []ApprovalItem
	if a.source != nil {
		if src := a.source(); src != nil {
			var err error
			if records, err = src.Approvals(ctx, a.repo.id); err != nil {
				return nil, err
			}
		}
	}
	return json.Marshal(approvalsOf(records, a.now()))
}

// approvalsOf sorts records into open and recent ones. A resolved record
// carries no resolution time, so Recent ages and orders by IssuedAt.
func approvalsOf(records []ApprovalItem, now time.Time) ApprovalsState {
	st := ApprovalsState{Open: []ApprovalItem{}, Recent: []ApprovalItem{}}
	for _, it := range records {
		switch it.State {
		case bus.StatePending, bus.StateApproved:
			it.Expired = watch.ObservedExpiry(it.State, it.ExpiresAt, now)
			st.Open = append(st.Open, it)
		case bus.StateConsumed, bus.StateCancelled, bus.StateExpired:
			if now.Sub(it.IssuedAt) <= ApprovalsRecentAge {
				it.Expired = false
				st.Recent = append(st.Recent, it)
			}
		}
	}
	sort.SliceStable(st.Open, func(i, j int) bool {
		if !st.Open[i].IssuedAt.Equal(st.Open[j].IssuedAt) {
			return st.Open[i].IssuedAt.Before(st.Open[j].IssuedAt)
		}
		return st.Open[i].OperationID < st.Open[j].OperationID
	})
	sort.SliceStable(st.Recent, func(i, j int) bool {
		if !st.Recent[i].IssuedAt.Equal(st.Recent[j].IssuedAt) {
			return st.Recent[i].IssuedAt.After(st.Recent[j].IssuedAt)
		}
		return st.Recent[i].OperationID > st.Recent[j].OperationID
	})
	if len(st.Recent) > MaxApprovalsRecent {
		st.Recent = st.Recent[:MaxApprovalsRecent]
	}
	return st
}
