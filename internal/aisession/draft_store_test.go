package aisession

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// addDraft is addSession for a draft record.
func addDraft(t *testing.T, s *fileStore, state State, created, ended time.Time, worktree string) ID {
	t.Helper()
	id := addSession(t, s, state, created, ended, 0)
	if _, err := s.Update(id, func(x *Session) error { x.Kind, x.Feature, x.Worktree = KindDraft, "commit", worktree; return nil }); err != nil {
		t.Fatal(err)
	}
	return id
}

func capsCode(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

// Drafts and ai run sessions are counted apart for the per-worktree and
// hourly caps, and together for max_running.
func TestDraftCapsCountApart(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	wt := t.TempDir()
	draft := Session{Kind: KindDraft, Worktree: wt}
	run := Session{Worktree: wt}
	caps := Caps{MaxRunning: 2, PerWorktree: PerWorktree, StartsPerHour: StartsPerHour}
	dcaps := draftCaps(Limits{MaxRunning: 2})

	// A running ai run session in the worktree does not use the draft's cap.
	s := retentionStore(t, now)
	id := addSession(t, s, StateRunning, now.Add(-time.Minute), time.Time{}, 0)
	_, _ = s.Update(id, func(x *Session) error { x.Worktree = wt; return nil })
	if err := s.precheck(draft, dcaps); err != nil {
		t.Fatalf("a draft beside a running session: %v", err)
	}
	// A running draft in the worktree refuses the next draft, not a session.
	addDraft(t, s, StateRunning, now.Add(-time.Minute), time.Time{}, wt)
	if got := capsCode(s.precheck(draft, dcaps)); got != RefuseLimitDraftWorktree {
		t.Fatalf("second draft: %q", got)
	}
	// Both count toward max_running (2): a new session elsewhere is refused.
	other := Session{Worktree: t.TempDir()}
	if got := capsCode(s.precheck(other, caps)); got != RefuseLimitRunning {
		t.Fatalf("third running: %q, want limit_running", got)
	}

	// The hourly caps: 30 drafts an hour, apart from the 10 session starts.
	s = retentionStore(t, now)
	for range DraftsPerHour {
		addDraft(t, s, StateEnded, now.Add(-30*time.Minute), now.Add(-29*time.Minute), t.TempDir())
	}
	if got := capsCode(s.precheck(draft, dcaps)); got != RefuseLimitDraftRate {
		t.Fatalf("31st draft in an hour: %q, want limit_draft_rate", got)
	}
	if err := s.precheck(run, caps); err != nil {
		t.Fatalf("a session after 30 drafts: %v", err)
	}
	s = retentionStore(t, now)
	for range StartsPerHour {
		addSession(t, s, StateEnded, now.Add(-30*time.Minute), now.Add(-29*time.Minute), 0)
	}
	if got := capsCode(s.precheck(run, caps)); got != RefuseLimitRate {
		t.Fatalf("11th session: %q", got)
	}
	if err := s.precheck(draft, dcaps); err != nil {
		t.Fatalf("a draft after 10 sessions: %v", err)
	}
}

// Ended drafts are kept at most 24 hours and at most 20, apart from the
// sessions: they never push session history out. draft.json stays when
// keep_transcripts is off.
func TestDraftRetention(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s := retentionStore(t, now)
	var sessions []ID
	for i := range MaxEndedKept {
		sessions = append(sessions, addSession(t, s, StateEnded, now.Add(-time.Duration(i+100)*time.Minute), now.Add(-time.Duration(i+99)*time.Minute), 0))
	}
	old := addDraft(t, s, StateEnded, now.Add(-30*time.Hour), now.Add(-25*time.Hour), "/wt")
	var drafts []ID
	for i := range MaxDraftsKept + 3 {
		id := addDraft(t, s, StateEnded, now.Add(-time.Duration(i+1)*time.Minute), now.Add(-time.Duration(i)*time.Minute), "/wt")
		if err := writePrivate(s.path(id, draftFile), []byte("{}")); err != nil {
			t.Fatal(err)
		}
		if err := writePrivate(s.path(id, transcriptFile), []byte("")); err != nil {
			t.Fatal(err)
		}
		drafts = append(drafts, id)
	}
	if err := s.Prune(DefaultKeepDays, false); err != nil {
		t.Fatal(err)
	}
	for _, id := range sessions {
		if !exists(s, id) {
			t.Fatalf("session %s was pruned by drafts", id)
		}
	}
	if exists(s, old) {
		t.Error("a draft older than 24 hours was kept")
	}
	for i, id := range drafts {
		if want := i < MaxDraftsKept; exists(s, id) != want {
			t.Errorf("draft %d kept = %v, want %v", i, !want, want)
		}
		if i < MaxDraftsKept {
			if _, err := os.Stat(s.path(id, draftFile)); err != nil {
				t.Errorf("draft %d lost draft.json: %v", i, err)
			}
			if _, err := os.Stat(s.path(id, transcriptFile)); err == nil {
				t.Errorf("draft %d kept its transcript with keep_transcripts off", i)
			}
		}
	}
}

// A lost host's cleanup also removes the draft's prompt and working folder.
func TestDraftLostHostCleanup(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s := retentionStore(t, now)
	id := addDraft(t, s, StateRunning, now.Add(-time.Hour), time.Time{}, "/wt")
	_, _ = s.Update(id, func(x *Session) error { x.Host.Heartbeat = now.Add(-time.Hour); return nil })
	if err := s.writeDraftFiles(id, &DraftStart{Feature: "commit", Prompt: "p", Targets: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); err != nil {
		t.Fatal(err)
	}
	got, err := s.read(id)
	if err != nil || got.Code != CodeHostLost {
		t.Fatalf("lost draft: %+v %v", got, err)
	}
	for _, name := range []string{draftPromptFile, draftWorkDir} {
		if _, err := os.Stat(s.path(id, name)); err == nil {
			t.Errorf("%s outlived the lost host", name)
		}
	}
}

// The draft link denies every request that is not a think permission;
// an Ask from any link becomes a deny.
func TestDraftLink(t *testing.T) {
	next := func(v Verdict) Next { return func(context.Context, *Request) Verdict { return v } }
	allow := Verdict{Decision: Allow, Handler: HandlerRule, Code: "rule:x"}
	for _, r := range []*Request{
		{Kind: KindRead}, {Kind: KindWrite}, {Kind: KindTerminal},
		{Kind: KindPermission, ToolKind: "read"}, {Kind: KindPermission, ToolKind: "edit"},
		{Kind: KindPermission, ToolKind: "execute"}, {Kind: KindPermission, ToolKind: "fetch"},
		{Kind: KindPermission, ToolKind: "search"}, {Kind: KindPermission, ToolKind: "other"},
		{Kind: KindPermission, ToolKind: "switch_mode"}, {Kind: KindPermission},
	} {
		v := draftLink{}.Decide(context.Background(), r, next(allow))
		if v.Decision != Deny || v.Code != CodeDraftProfile {
			t.Errorf("%s %s: %+v", r.Kind, r.ToolKind, v)
		}
	}
	think := &Request{Kind: KindPermission, ToolKind: "think"}
	if v := (draftLink{}).Decide(context.Background(), think, next(allow)); v != allow {
		t.Errorf("think did not go on through the chain: %+v", v)
	}
	if v := draftVerdict(Verdict{Decision: Ask, Handler: HandlerRule}); v.Decision != Deny || v.Code != CodeDraftProfile {
		t.Errorf("an ask was kept: %+v", v)
	}
}
