package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ryanhelms/agentflow/internal/config"
)

// ErrNothingToUndo is returned when the scope has no journal record.
var ErrNothingToUndo = errors.New("no settings save to undo in this repository")

// Undo reverses the scope's last save (config.undo). It is a save of the
// inverse ChangeSet, with each edit expecting the value the save left, so it
// goes through the same checks, gate and writers. A field that changed since
// conflicts: the undo refuses unless req.SkipConflicts, which reverts the
// others. Fields whose pull request is still open get published. The undo
// writes its own record, so a second undo redoes the save.
func (s *Service) Undo(ctx context.Context, req UndoRequest, token string, gate Gate) (any, error) {
	p, rec, err := s.undoPlan(ctx, req)
	if err != nil {
		return nil, err
	}
	return s.commit(ctx, p, "config.undo", rec.Operation, token, gate)
}

// PreviewUndo previews Undo without writing.
func (s *Service) PreviewUndo(ctx context.Context, req UndoRequest) (Preview, error) {
	p, _, err := s.undoPlan(ctx, req)
	if err != nil {
		return Preview{}, err
	}
	return p.preview, nil
}

func (s *Service) undoPlan(ctx context.Context, req UndoRequest) (*plan, *JournalRecord, error) {
	rec, err := s.Journal(ctx)
	if err != nil {
		return nil, nil, err
	}
	if rec == nil || len(rec.Edits) == 0 {
		return nil, nil, ErrNothingToUndo
	}
	cs := inverse(rec, req.Repo)
	note := ""
	published, err := s.publishedOpen(ctx, rec)
	if err != nil {
		// GitHub cannot say whether the pull request merged: undo the
		// personal part alone, and say so.
		personal := personalEdits(cs.Edits)
		if len(personal) == 0 {
			return nil, nil, err
		}
		if len(personal) < len(cs.Edits) {
			note = "Only the personal settings were undone: " + err.Error() + ". The repository part of the save stays as it is."
		}
		cs.Edits = personal
	}
	otherWorktree := ""
	if pub := rec.Publication; pub != nil && pub.Mode == RepoWorktree {
		if branch := s.branch(ctx); branch != pub.Branch {
			otherWorktree = pub.Branch
		}
	}
	extra := func(c *check) {
		if note != "" {
			c.add(SeverityWarning, nil, CodeReference, note)
		}
		for _, e := range c.edits {
			switch {
			case e.layer != config.FieldRepo:
			case otherWorktree != "":
				c.add(SeverityError, e, CodeReference, "This change was written to "+RepoFile+" in the worktree of branch "+otherWorktree+
					". Undo it from that worktree.")
			case published:
				c.add(SeverityError, e, CodePublished, "This change waits in pull request "+rec.Publication.URL+". Close the pull request to discard it.")
			}
		}
	}
	p, err := s.plan(ctx, cs, extra)
	if p != nil {
		p.note = note
	}
	if err != nil || !req.SkipConflicts || p.preview.Status != StatusConflict {
		return p, rec, err
	}
	skip := map[int]bool{}
	for _, d := range p.preview.Diagnostics {
		if d.Code == CodeConflict && d.Edit >= 0 {
			skip[d.Edit] = true
		}
	}
	var keep []Edit
	for i, e := range cs.Edits {
		if !skip[i] {
			keep = append(keep, e)
		}
	}
	if len(keep) == 0 {
		return p, rec, nil
	}
	cs.Edits = keep
	p, err = s.plan(ctx, cs, extra)
	if p != nil {
		p.note = note
	}
	return p, rec, err
}

// personalEdits are the edits of the personal layer.
func personalEdits(edits []Edit) []Edit {
	var out []Edit
	for _, e := range edits {
		if strings.HasPrefix(e.ID, string(config.FieldPersonal)+":") {
			out = append(out, e)
		}
	}
	return out
}

// inverse builds the ChangeSet that reverts rec: each edit back to its
// value before the save, newest first, expecting the value the save left.
func inverse(rec *JournalRecord, target RepoTarget) ChangeSet {
	cs := ChangeSet{Repo: target}
	if cs.Repo.Mode == "" && rec.Publication != nil {
		cs.Repo.Mode = rec.Publication.Mode
	}
	for i := len(rec.Edits) - 1; i >= 0; i-- {
		e := rec.Edits[i]
		inv := Edit{ID: e.ID, Op: OpSet, Value: e.Before, Expect: e.After}
		if len(e.Before) == 0 || bytes.Equal(bytes.TrimSpace(e.Before), []byte("null")) {
			inv.Op, inv.Value = OpUnset, nil
		}
		if len(inv.Expect) == 0 {
			inv.Expect = json.RawMessage("null")
		}
		cs.Edits = append(cs.Edits, inv)
	}
	return cs
}

// publishedOpen reports whether rec's pull request is still open, asking
// GitHub. A record without a pull request, or one GitHub says merged or
// closed, is not.
func (s *Service) publishedOpen(ctx context.Context, rec *JournalRecord) (bool, error) {
	pub := rec.Publication
	if pub == nil || pub.Mode == RepoWorktree || pub.URL == "" {
		return false, nil
	}
	if pub.State == "merged" {
		return false, nil
	}
	state, err := s.Publisher.State(ctx, s.Repo, *pub)
	if err != nil {
		return false, errors.New("cannot read the state of the saved pull request: " + safeMessage(err))
	}
	if state == "merged" || state == "closed" {
		s.refreshPublication(ctx, rec, state)
	}
	return state == "open", nil
}
