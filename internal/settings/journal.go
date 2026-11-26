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
	return writeAtomic(s.JournalPath(ctx), string(data)+"\n", 0o600)
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
	pending := rec.Publication != nil && rec.Publication.Mode != RepoWorktree && openStates[rec.Publication.State]
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
