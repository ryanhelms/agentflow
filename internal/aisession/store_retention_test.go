package aisession

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// retentionStore is a store whose clock is now.
func retentionStore(t *testing.T, now time.Time) *fileStore {
	t.Helper()
	s, err := openStore(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// addSession writes a session record of state, created at created, ended
// at ended for an ended one, with a file of size bytes beside it (sparse:
// the store counts apparent sizes).
func addSession(t *testing.T, s *fileStore, state State, created, ended time.Time, size int64) ID {
	t.Helper()
	id := NewID()
	if err := os.MkdirAll(s.Dir(id), 0o700); err != nil {
		t.Fatal(err)
	}
	sess := Session{ID: id, Harness: "demo", State: state, CreatedAt: created}
	if state == StateEnded {
		sess.Code = CodeEndTurn
		e := ended
		sess.EndedAt = &e
	} else {
		sess.Host = HostInfo{Mode: HostDetached, StartedAt: created, Heartbeat: s.now()}
	}
	if err := s.write(sess); err != nil {
		t.Fatal(err)
	}
	if size > 0 {
		f, err := os.Create(filepath.Join(s.Dir(id), "pad"))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	return id
}

func exists(s *fileStore, id ID) bool {
	_, err := os.Stat(s.Dir(id))
	return err == nil
}

func TestPruneKeepsSevenDays(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s := retentionStore(t, now)
	old := addSession(t, s, StateEnded, now.Add(-9*24*time.Hour), now.Add(-8*24*time.Hour), 0)
	recent := addSession(t, s, StateEnded, now.Add(-7*24*time.Hour), now.Add(-6*24*time.Hour), 0)
	// A running session older than any limit, beating now.
	running := addSession(t, s, StateRunning, now.Add(-30*24*time.Hour), time.Time{}, 0)
	if err := s.Prune(DefaultKeepDays, true); err != nil {
		t.Fatal(err)
	}
	if exists(s, old) || !exists(s, recent) || !exists(s, running) {
		t.Errorf("kept: old %v, recent %v, running %v", exists(s, old), exists(s, recent), exists(s, running))
	}
	// keep_days 1 drops the six-day-old one too; 0 means the default.
	if err := s.Prune(1, true); err != nil {
		t.Fatal(err)
	}
	if exists(s, recent) || !exists(s, running) {
		t.Errorf("keep_days 1: recent %v, running %v", exists(s, recent), exists(s, running))
	}
}

func TestPruneKeepsFiftyEndedSessions(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s := retentionStore(t, now)
	var ids []ID // oldest first
	for i := range MaxEndedKept + 5 {
		at := now.Add(-time.Duration(MaxEndedKept+5-i) * time.Minute)
		ids = append(ids, addSession(t, s, StateEnded, at, at.Add(time.Second), 0))
	}
	running := addSession(t, s, StateWaiting, now.Add(-2*time.Hour), time.Time{}, 0)
	if err := s.Prune(DefaultKeepDays, true); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if want := i >= 5; exists(s, id) != want {
			t.Errorf("session %d (oldest first): kept %v, want %v", i, exists(s, id), want)
		}
	}
	if !exists(s, running) {
		t.Error("a waiting session was deleted")
	}
	if MaxEndedKept != 50 {
		t.Errorf("MaxEndedKept = %d, the ADR says 50", MaxEndedKept)
	}
}

func TestPruneKeeps200MiB(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s := retentionStore(t, now)
	const mib = 1 << 20
	// Newest first: 90 + 90 fit in 200 MiB; the third does not.
	newest := addSession(t, s, StateEnded, now.Add(-1*time.Hour), now.Add(-50*time.Minute), 90*mib)
	second := addSession(t, s, StateEnded, now.Add(-2*time.Hour), now.Add(-110*time.Minute), 90*mib)
	third := addSession(t, s, StateEnded, now.Add(-3*time.Hour), now.Add(-170*time.Minute), 90*mib)
	// A running session past the limit on its own is never deleted.
	running := addSession(t, s, StateRunning, now.Add(-4*time.Hour), time.Time{}, 300*mib)
	if err := s.Prune(DefaultKeepDays, true); err != nil {
		t.Fatal(err)
	}
	if !exists(s, newest) || !exists(s, second) || exists(s, third) || !exists(s, running) {
		t.Errorf("kept: newest %v, second %v, third %v, running %v",
			exists(s, newest), exists(s, second), exists(s, third), exists(s, running))
	}
	if MaxStoreBytes != 200*mib {
		t.Errorf("MaxStoreBytes = %d, the ADR says 200 MiB", MaxStoreBytes)
	}
}

func TestPruneWithoutTranscriptsKeepsOnlyTheRecord(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s := retentionStore(t, now)
	id := addSession(t, s, StateEnded, now.Add(-time.Hour), now.Add(-time.Minute), 0)
	for _, n := range []string{transcriptFile, hostLogFile, answerKeyFile} {
		if err := writePrivate(s.path(id, n), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Prune(DefaultKeepDays, false); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{transcriptFile, hostLogFile, answerKeyFile} {
		if _, err := os.Stat(s.path(id, n)); err == nil {
			t.Errorf("%s kept with keep_transcripts off", n)
		}
	}
	if _, err := os.Stat(s.path(id, sessionFile)); err != nil {
		t.Errorf("session.json: %v", err)
	}
}
