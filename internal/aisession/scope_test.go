package aisession_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
)

// A caller sees and acts only on the sessions of its own repository (every
// worktree of it); another repository's session answers like an unknown
// ID.
func TestSessionsAreScopedToTheCallersRepository(t *testing.T) {
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		writeCall("held.txt"),
		{WaitCancel: true},
	}}, aisessiontest.Settings{})
	ctx := person(context.Background())
	s := r.start(t, ctx, "write")
	a := r.waitAsk(t, s.ID)

	_, otherPrimary := aisessiontest.Worktree(t)
	other := aisession.WithScope(ctx, filepath.Join(otherPrimary, ".git"))
	if list, err := r.m.List(other); err != nil || len(list) != 0 {
		t.Errorf("another repository lists %+v, %v", list, err)
	}
	if _, _, err := r.m.Get(other, s.ID); !errors.Is(err, aisession.ErrNoSession) {
		t.Errorf("another repository gets the session: %v", err)
	}
	if _, _, err := r.m.Items(other, s.ID, 0, 10); !errors.Is(err, aisession.ErrNoSession) {
		t.Errorf("another repository reads the transcript: %v", err)
	}
	if _, err := r.m.Answer(other, aisession.AnswerRequest{Session: s.ID, Ask: a.ID, Decision: aisession.Deny}); refusal(t, err) != aisession.AnswerNotFound {
		t.Errorf("another repository answered: %v", err)
	}
	if _, err := r.m.Cancel(other, s.ID); refusal(t, err) != aisession.AnswerNotFound {
		t.Errorf("another repository cancelled: %v", err)
	}
	// The primary checkout of the same repository shares its common dir.
	same := aisession.WithScope(ctx, filepath.Join(r.primary, ".git"))
	if list, err := r.m.List(same); err != nil || len(list) != 1 {
		t.Errorf("the same repository lists %+v, %v", list, err)
	}
	if _, err := r.m.Cancel(same, s.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.d.Wait(t, s.ID, 60*time.Second); got.Code != aisession.CodeCancelled {
		t.Errorf("session ended %s", got.Code)
	}
}
