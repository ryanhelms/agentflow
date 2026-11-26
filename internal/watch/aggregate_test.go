package watch_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/watch"
)

func TestAggregatorQuietAndMaxDelay(t *testing.T) {
	a := watch.NewAggregator(0, 0)
	t0 := time.Unix(1000, 0)
	main := watch.Target{Repo: testRepo, Worktree: "main"}
	other := watch.Target{Repo: testRepo, Worktree: "other"}
	if _, ok := a.Next(); ok {
		t.Fatal("empty aggregator has a deadline")
	}

	// A burst every 50 ms keeps the batch open until MaxDelay.
	for i := range 8 { // 0 ms to 350 ms
		a.Add(main, watch.Change{Path: "/x", Kind: watch.Modified}, t0.Add(time.Duration(i)*50*time.Millisecond))
	}
	a.Add(other, watch.Change{Path: "/y", Kind: watch.Created}, t0.Add(10*time.Millisecond))
	if next, _ := a.Next(); !next.Equal(t0.Add(110 * time.Millisecond)) {
		t.Fatalf("next = %v", next.Sub(t0))
	}
	if got := a.Due(t0.Add(109 * time.Millisecond)); len(got) != 0 {
		t.Fatalf("due early: %v", got)
	}
	got := a.Due(t0.Add(110 * time.Millisecond))
	if len(got) != 1 || got[0].Target.Worktree != "other" {
		t.Fatalf("quiet batch = %+v", got)
	}
	if next, _ := a.Next(); !next.Equal(t0.Add(watch.MaxDelay)) {
		t.Fatalf("max deadline = %v", next.Sub(t0))
	}
	got = a.Due(t0.Add(watch.MaxDelay))
	if len(got) != 1 || len(got[0].Changes) != 8 {
		t.Fatalf("max batch = %+v", got)
	}
	a.Add(main, watch.Change{Path: "/x", Kind: watch.Modified}, t0.Add(450*time.Millisecond))
	if next, _ := a.Next(); !next.Equal(t0.Add(550 * time.Millisecond)) {
		t.Fatalf("new batch deadline = %v", next.Sub(t0))
	}
	a.Drop(watch.KeyOf(main))
	if _, ok := a.Next(); ok {
		t.Fatal("Drop left a batch")
	}
}

func TestPollSourceSeesSameSizeRewrites(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "later")
	head := filepath.Join(dir, "HEAD")
	writeFile(t, head, "ref: refs/heads/aaaa\n")
	s := watch.NewPollSource(watch.PollOptions{Interval: 20 * time.Millisecond})
	defer func() { _ = s.Close() }()
	if s.Mode() != watch.ModePoll {
		t.Fatal("mode")
	}
	for _, d := range []string{dir, missing} {
		if err := s.Add(d); err != nil {
			t.Fatal(err)
		}
	}

	fi, err := os.Stat(head)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(head, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("ref: refs/heads/bbbb\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Chtimes(head, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	expect(t, s, head, watch.Modified)

	writeFile(t, filepath.Join(missing, "x.yml"), "x")
	expect(t, s, missing, watch.Created)
	expect(t, s, filepath.Join(missing, "x.yml"), watch.Created)

	if err := os.RemoveAll(missing); err != nil {
		t.Fatal(err)
	}
	expect(t, s, filepath.Join(missing, "x.yml"), watch.Removed)
}

func expect(t *testing.T, s watch.Source, path string, kind watch.Kind) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case c := <-s.Changes():
			if c.Path == path && c.Kind == kind {
				return
			}
		case <-deadline:
			t.Fatalf("no change %d for %s", kind, path)
		}
	}
}
