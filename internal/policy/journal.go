package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Read-only access to the approval journal, for the event watcher and the
// console's approvals projection (ADR "Phase 2 watch sources"). Nothing here
// takes the journal lock or changes a record: a record is replaced as a
// whole by an atomic rename, so a reader sees the old or the new file.

// maxJournalRecord bounds one record file. Real records are under 2 KiB.
const maxJournalRecord = 64 << 10

var journalNameRe = regexp.MustCompile(`^([0-9a-f]{32})-(op_[0-9a-f]{32})\.json$`)

// JournalRecord is the public view of one approval record: the fields a
// reader may show. It never decodes the fingerprint, the related keys, the
// proposal key or nonce, so none of them can leak through this type.
type JournalRecord struct {
	Approval
	IssuedAt time.Time `json:"issued_at"`
	// Source names the policy rule that asked for approval.
	Source string `json:"source,omitempty"`
}

// ReadJournalRecord reads the record file at path. The file must be named
// "<ScopeKey>-<operation ID>.json", be a regular file (never a symbolic
// link) of at most 64 KiB, and carry the operation ID of its name. A
// missing file returns an error wrapping fs.ErrNotExist.
func ReadJournalRecord(path string) (JournalRecord, error) {
	var r JournalRecord
	m := journalNameRe.FindStringSubmatch(filepath.Base(path))
	if m == nil {
		return r, fmt.Errorf("approval record %q: unexpected file name", filepath.Base(path))
	}
	info, err := os.Lstat(path)
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxJournalRecord {
		return r, fmt.Errorf("approval record %q: not a regular file of at most %d bytes", m[2], maxJournalRecord)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return JournalRecord{}, fmt.Errorf("approval record %q: %w", m[2], err)
	}
	if r.OperationID != m[2] {
		return JournalRecord{}, fmt.Errorf("approval record %q: operation ID does not match its name", m[2])
	}
	return r, nil
}

// ListJournal lists the records of the repository with scope key scopeKey
// in dir (ApprovalsDir) whose files changed at or after since, oldest
// issued first. Operations prepared under an auto policy never asked a
// person and are left out, and so is any file that cannot be read.
func ListJournal(dir, scopeKey string, since time.Time) ([]JournalRecord, error) {
	list, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []JournalRecord
	for _, e := range list {
		m := journalNameRe.FindStringSubmatch(e.Name())
		if m == nil || m[1] != scopeKey {
			continue
		}
		if info, err := e.Info(); err != nil || info.ModTime().Before(since) {
			continue
		}
		r, err := ReadJournalRecord(filepath.Join(dir, e.Name()))
		if err != nil || r.State == prepared {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].IssuedAt.Equal(out[j].IssuedAt) {
			return out[i].IssuedAt.Before(out[j].IssuedAt)
		}
		return out[i].OperationID < out[j].OperationID
	})
	return out, nil
}
