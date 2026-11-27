package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// maxJournal bounds a journal file.
const maxJournal = 4 << 20

// JournalPath is the undo journal file of the scope s works in:
// <home>/settings/<policy.ScopeKey(scope)>.json, the scope approvals use, or
// <home>/settings/personal.json outside a repository.
func (s *Service) JournalPath(ctx context.Context) string {
	name := "personal.json"
	if s.Repo != nil {
		scope := s.Repo.GitDir(ctx)
		if resolved, err := filepath.EvalSymlinks(scope); err == nil {
			scope = resolved
		}
		name = policy.ScopeKey(scope) + ".json"
	}
	return filepath.Join(s.Home, JournalDir, name)
}

// Journal returns the scope's last save, or nil when there is none.
func (s *Service) Journal(ctx context.Context) (*JournalRecord, error) {
	if err := s.homeErr(); err != nil {
		return nil, err
	}
	path := s.JournalPath(ctx)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxJournal {
		return nil, errors.New("the settings journal cannot be read")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("the settings journal cannot be read")
	}
	var rec JournalRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&rec); err != nil || rec.Version != 1 {
		return nil, errors.New("the settings journal is not valid")
	}
	return &rec, nil
}

// writeJournal replaces the scope's record: directory 0700, file 0600.
func (s *Service) writeJournal(ctx context.Context, rec JournalRecord) error {
	if err := s.homeErr(); err != nil {
		return err
	}
	dir := filepath.Join(s.Home, JournalDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a directory needs its search bit; 0700 keeps it to the user
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeJournalFile(s.JournalPath(ctx), string(data)+"\n")
}

// writeJournalFile writes a journal file; tests replace it to fail.
var writeJournalFile = func(path, content string) error { return writeAtomic(path, content, 0o600) }

// journalWritable checks that a new journal record can be written.
func (s *Service) journalWritable() error {
	if err := s.homeErr(); err != nil {
		return err
	}
	dir := filepath.Join(s.Home, JournalDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".agentflow-settings-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// openStates are publication states whose pull request may still be open.
var openStates = map[string]bool{"draft": true, "open": true, "auto_merge_requested": true}

// Summary is the record as the settings projection shows it: IDs, no
// values. undoable is false while the repository part waits in an open pull
// request, or when every field changed since.
func (s *Service) Summary(ctx context.Context, rec *JournalRecord) *SaveSummary {
	if rec == nil {
		return nil
	}
	out := &SaveSummary{Operation: rec.Operation, Time: rec.Time, Client: rec.Client, Publication: rec.Publication, UndoOf: rec.UndoOf, IDs: []string{}}
	for _, e := range rec.Edits {
		out.IDs = append(out.IDs, e.ID)
	}
	f, err := s.read()
	if err != nil {
		return out
	}
	if pub := rec.Publication; pub != nil && pub.Mode != RepoWorktree && openStates[pub.State] && s.PullState != nil && pub.Number > 0 {
		if state, ok := s.PullState(pub.Number); ok && state != "open" {
			cp := *pub
			cp.State = state
			out.Publication = &cp
			s.refreshPublication(ctx, rec, state)
		}
	}
	pending := out.Publication != nil && out.Publication.Mode != RepoWorktree && openStates[out.Publication.State]
	for _, e := range rec.Edits {
		l, keys, err := fieldKeys(e.ID)
		if err != nil {
			continue
		}
		if l == config.FieldRepo && pending {
			continue
		}
		if journalHolds(*f.of(l), keys, e.After) {
			out.Undoable = true
			break
		}
	}
	return out
}

// journalHolds reports whether the layer file still has the value a save
// left at keys.
func journalHolds(l layer, keys []string, after json.RawMessage) bool {
	got, set, err := consolecfg.LayerValue(l.Content, keys)
	if err != nil {
		return false
	}
	wantSet := !bytes.Equal(bytes.TrimSpace(after), []byte("null"))
	return set == wantSet && (!set || bytes.Equal(canonical(got), canonical(after)))
}

// refreshPublication records a pull request's newer state in the journal,
// so the record shows it after the daemon restarts. It only replaces the
// record it read: a newer save is never overwritten. Best effort: the
// record stays as it was when the write fails.
func (s *Service) refreshPublication(ctx context.Context, rec *JournalRecord, state string) {
	if rec.Publication == nil || rec.Publication.State == state {
		return
	}
	unlock, err := s.lock()
	if err != nil {
		return
	}
	defer unlock()
	now, err := s.Journal(ctx)
	if err != nil || now == nil || now.Operation != rec.Operation || now.Publication == nil || now.Publication.URL != rec.Publication.URL {
		return
	}
	now.Publication.State = state
	_ = s.writeJournal(ctx, *now)
}
