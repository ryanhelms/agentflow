package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/bustest"
)

// clock is a settable time source for rotation and retention tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func testOptions() options {
	opt := defaultOptions()
	opt.poll = 5 * time.Millisecond
	return opt
}

func mustOpen(t testing.TB, dir string, role Role, opt options) *fileStore {
	t.Helper()
	s, err := open(context.Background(), dir, role, opt)
	if err != nil {
		t.Fatalf("open %v: %v", role, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func event(repo bus.RepoID, now time.Time) bus.Event {
	return bus.Event{Type: bus.TypeGitRefsChanged, Source: "test", Repo: repo}.Normalize(now)
}

func appendN(t testing.TB, s *fileStore, n int) {
	t.Helper()
	for range n {
		if _, err := s.Append(context.Background(), event(s.Repo(), s.opt.now())); err != nil {
			t.Fatal(err)
		}
	}
}

func seqs(t testing.TB, r Reader, from bus.Cursor) []uint64 {
	t.Helper()
	var out []uint64
	for e, err := range r.ReadFrom(context.Background(), from) {
		if err != nil {
			t.Fatalf("ReadFrom(%v): %v", from, err)
		}
		out = append(out, e.Seq)
	}
	return out
}

// wantRange fails unless got is exactly lo, lo+1, ..., hi.
func wantRange(t testing.TB, got []uint64, lo, hi uint64) {
	t.Helper()
	if uint64(len(got)) != hi-lo+1 {
		t.Fatalf("got %d events %v, want %d..%d", len(got), short(got), lo, hi)
	}
	for i, seq := range got {
		if seq != lo+uint64(i) {
			t.Fatalf("event %d has seq %d, want %d (%v)", i, seq, lo+uint64(i), short(got))
		}
	}
}

func short(s []uint64) string {
	if len(s) > 12 {
		return fmt.Sprint(s[:6], "...", s[len(s)-6:])
	}
	return fmt.Sprint(s)
}

func segmentNames(t testing.TB, s *fileStore) []string {
	t.Helper()
	segs, err := listSegments(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(segs))
	for i, seg := range segs {
		names[i] = filepath.Base(seg.path)
	}
	return names
}

func TestOpenCreatesLayoutAndResumes(t *testing.T) {
	dir := t.TempDir()
	owner := mustOpen(t, dir, RoleOwner, testOptions())
	if !owner.Repo().Valid() || owner.Repo() == bus.GlobalRepo {
		t.Fatalf("repo id %q", owner.Repo())
	}
	data, err := os.ReadFile(filepath.Join(Path(dir), RepoIDFile))
	if err != nil || string(data) != string(owner.Repo()) {
		t.Fatalf("repo-id = %q, %v", data, err)
	}
	appendN(t, owner, 3)
	if names := segmentNames(t, owner); len(names) != 1 || names[0] != "00000000000000000001.jsonl" {
		t.Fatalf("segments = %v", names)
	}
	wantRange(t, seqs(t, owner, bus.Cursor{owner.Repo(): 1}), 2, 3)
	if head, _ := owner.Head(context.Background()); head.String() != "v1:"+string(owner.Repo())+"=3" {
		t.Fatalf("owner Head = %v", head)
	}

	reader := mustOpen(t, dir, RoleReader, testOptions())
	if reader.Repo() != owner.Repo() {
		t.Fatalf("reader repo %q, owner %q", reader.Repo(), owner.Repo())
	}
	wantRange(t, seqs(t, reader, bus.Cursor{}), 1, 3)
	if head, _ := reader.Head(context.Background()); head[owner.Repo()] != 3 {
		t.Fatalf("reader Head = %v", head)
	}
	if _, err := reader.Append(context.Background(), event(reader.Repo(), time.Now())); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("reader Append err = %v", err)
	}

	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	again := mustOpen(t, dir, RoleOwner, testOptions())
	if again.Repo() != owner.Repo() {
		t.Fatal("repo id changed on reopen")
	}
	appendN(t, again, 1)
	wantRange(t, seqs(t, again, bus.Cursor{}), 1, 4)
}

func TestOpenedStoreIsAFollower(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir(), RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, ok := s.(Follower); !ok {
		t.Fatal("Open result does not implement Follower")
	}
}

func TestReaderOfMissingStore(t *testing.T) {
	_, err := Open(context.Background(), t.TempDir(), RoleReader)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reader of missing store err = %v", err)
	}
	if _, err := Open(context.Background(), t.TempDir(), Role(0)); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestAppendRejectsInvalidEvents(t *testing.T) {
	s := mustOpen(t, t.TempDir(), RoleOwner, testOptions())
	ctx := context.Background()
	overflow := bus.Event{Type: bus.TypeStreamOverflow, Source: "bus", Repo: s.Repo()}.Normalize(time.Now())
	other := event("ffffffffffffffff", time.Now())
	sequenced := event(s.Repo(), time.Now())
	sequenced.Seq = 9
	invalid := event(s.Repo(), time.Now())
	invalid.Source = ""
	for _, tc := range []struct {
		name string
		e    bus.Event
		want error
	}{
		{"transient", overflow, ErrTransient},
		{"other repo", other, ErrWrongRepo},
		{"sequenced", sequenced, bus.ErrSequenced},
		{"invalid", invalid, bus.ErrInvalidEnvelope},
	} {
		if _, err := s.Append(ctx, tc.e); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if head, _ := s.Head(ctx); len(head) != 0 {
		t.Fatalf("rejected events were stored: %v", head)
	}
	_ = s.Close()
	if _, err := s.Append(ctx, event(s.Repo(), time.Now())); !errors.Is(err, bus.ErrClosed) {
		t.Fatalf("Append after Close err = %v", err)
	}
}

func TestSingleWriter(t *testing.T) {
	dir := t.TempDir()
	opt := testOptions()
	opt.lockWait = 20 * time.Millisecond
	owner := mustOpen(t, dir, RoleOwner, opt)
	appendN(t, owner, 2)

	if _, err := open(context.Background(), dir, RoleOwner, opt); !errors.Is(err, ErrOwned) {
		t.Fatalf("second owner err = %v", err)
	}
	appender := mustOpen(t, dir, RoleAppender, opt)
	if _, err := appender.Append(context.Background(), event(appender.Repo(), time.Now())); !errors.Is(err, ErrOwned) {
		t.Fatalf("appender while owned err = %v", err)
	}
	wantRange(t, seqs(t, mustOpen(t, dir, RoleReader, opt), bus.Cursor{}), 1, 2)

	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	e, err := appender.Append(context.Background(), event(appender.Repo(), time.Now()))
	if err != nil || e.Seq != 3 {
		t.Fatalf("appender after owner closed = seq %d, %v", e.Seq, err)
	}
	if head, _ := appender.Head(context.Background()); head[appender.Repo()] != 3 {
		t.Fatalf("appender Head = %v", head)
	}
}

func TestRecoverTruncatedLine(t *testing.T) {
	dir := t.TempDir()
	owner := mustOpen(t, dir, RoleOwner, testOptions())
	appendN(t, owner, 3)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(Path(dir), segmentName(1))
	before, _ := os.Stat(seg)
	f, err := os.OpenFile(seg, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"0190","type":"git.refs`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	reader := mustOpen(t, dir, RoleReader, testOptions())
	wantRange(t, seqs(t, reader, bus.Cursor{}), 1, 3)
	if head, _ := reader.Head(context.Background()); head[reader.Repo()] != 3 {
		t.Fatalf("Head with a partial line = %v", head)
	}

	owner = mustOpen(t, dir, RoleOwner, testOptions())
	if after, _ := os.Stat(seg); after.Size() != before.Size() {
		t.Fatalf("partial line not truncated: %d bytes, want %d", after.Size(), before.Size())
	}
	appendN(t, owner, 1)
	wantRange(t, seqs(t, reader, bus.Cursor{}), 1, 4)
}

func TestRecoverEmptySegment(t *testing.T) {
	dir := t.TempDir()
	owner := mustOpen(t, dir, RoleOwner, testOptions())
	appendN(t, owner, 2)
	_ = owner.Close()
	// A crash right after a rotation created the next segment.
	if err := os.WriteFile(filepath.Join(Path(dir), segmentName(3)), []byte(`{"partial`), 0o600); err != nil {
		t.Fatal(err)
	}
	owner = mustOpen(t, dir, RoleOwner, testOptions())
	if head, _ := owner.Head(context.Background()); head[owner.Repo()] != 2 {
		t.Fatalf("Head = %v, want seq 2", head)
	}
	appendN(t, owner, 1)
	wantRange(t, seqs(t, owner, bus.Cursor{}), 1, 3)
	if names := segmentNames(t, owner); len(names) != 2 {
		t.Fatalf("segments = %v", names)
	}
}

func TestRotationBySize(t *testing.T) {
	opt := testOptions()
	opt.segmentBytes = 1024
	s := mustOpen(t, t.TempDir(), RoleOwner, opt)
	appendN(t, s, 40)
	names := segmentNames(t, s)
	if len(names) < 3 {
		t.Fatalf("segments = %v, want several", names)
	}
	segs, _ := listSegments(s.dir)
	for i, seg := range segs[:len(segs)-1] {
		info, _ := os.Stat(seg.path)
		if info.Size() < opt.segmentBytes || info.Size() > opt.segmentBytes+512 {
			t.Errorf("closed segment %s is %d bytes", names[i], info.Size())
		}
		if got := seqs(t, s, bus.Cursor{s.Repo(): segs[i+1].first - 1}); got[0] != segs[i+1].first {
			t.Errorf("segment %s does not start at its name", names[i+1])
		}
	}
	wantRange(t, seqs(t, s, bus.Cursor{}), 1, 40)
	wantRange(t, seqs(t, s, bus.Cursor{s.Repo(): 17}), 18, 40)
}

func TestRotationByAge(t *testing.T) {
	dir := t.TempDir()
	c := newClock()
	opt := testOptions()
	opt.now = c.now
	s := mustOpen(t, dir, RoleOwner, opt)
	appendN(t, s, 2)
	c.advance(SegmentMaxAge - time.Second)
	appendN(t, s, 1)
	if names := segmentNames(t, s); len(names) != 1 {
		t.Fatalf("rotated early: %v", names)
	}
	c.advance(time.Second)
	appendN(t, s, 1)
	if names := segmentNames(t, s); len(names) != 2 || names[1] != segmentName(4) {
		t.Fatalf("segments = %v, want a new one at seq 4", names)
	}
	// An appender reads the segment's age from its first line.
	_ = s.Close()
	c.advance(SegmentMaxAge)
	a := mustOpen(t, dir, RoleAppender, opt)
	appendN(t, a, 1)
	if names := segmentNames(t, a); len(names) != 3 || names[2] != segmentName(5) {
		t.Fatalf("appender segments = %v", names)
	}
}

func TestRetentionBySize(t *testing.T) {
	dir := t.TempDir()
	opt := testOptions()
	opt.segmentBytes = 1024
	opt.retainBytes = 4096
	s := mustOpen(t, dir, RoleOwner, opt)
	appendN(t, s, 200)
	segs, _ := listSegments(s.dir)
	var total int64
	for _, seg := range segs {
		info, _ := os.Stat(seg.path)
		total += info.Size()
	}
	if total > opt.retainBytes+opt.segmentBytes+512 {
		t.Fatalf("store is %d bytes after retention", total)
	}
	oldest := segs[0].first
	if oldest == 1 {
		t.Fatal("retention deleted nothing")
	}
	if _, err := Replay(context.Background(), s, bus.Cursor{s.Repo(): 1}); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("expired cursor err = %v", err)
	}
	wantRange(t, seqs(t, s, bus.Cursor{}), oldest, 200)
	wantRange(t, seqs(t, s, bus.Cursor{s.Repo(): oldest - 1}), oldest, 200)

	// The active segment survives even when it alone exceeds the limit.
	opt.retainBytes = 1
	_ = s.Close()
	s = mustOpen(t, dir, RoleOwner, opt)
	if names := segmentNames(t, s); len(names) != 1 {
		t.Fatalf("segments after open = %v, want only the active one", names)
	}
	if head, _ := s.Head(context.Background()); head[s.Repo()] != 200 {
		t.Fatalf("Head = %v", head)
	}
}

func TestRetentionByAge(t *testing.T) {
	dir := t.TempDir()
	c := newClock()
	opt := testOptions()
	opt.now = c.now
	s := mustOpen(t, dir, RoleOwner, opt)
	appendN(t, s, 3) // segment 1
	c.advance(25 * time.Hour)
	appendN(t, s, 3) // segment 4
	c.advance(25 * time.Hour)
	appendN(t, s, 3) // segment 7, active
	if names := segmentNames(t, s); len(names) != 3 {
		t.Fatalf("segments = %v", names)
	}
	_ = s.Close()
	// Segment 1's newest event is now 7 days and 2 hours old; segment 4's
	// is 6 days and 1 hour old.
	c.advance(RetainAge - 48*time.Hour + 2*time.Hour)
	s = mustOpen(t, dir, RoleOwner, opt)
	if names := segmentNames(t, s); len(names) != 2 || names[0] != segmentName(4) {
		t.Fatalf("segments after open = %v, want 4 and 7", names)
	}
	c.advance(24 * time.Hour)
	appendN(t, s, 1) // the active segment is old, so it rotates and retention runs
	if names := segmentNames(t, s); len(names) != 2 || names[0] != segmentName(7) || names[1] != segmentName(10) {
		t.Fatalf("segments after rotation = %v, want 7 and 10", names)
	}
	wantRange(t, seqs(t, s, bus.Cursor{}), 7, 10)
}

func TestReadFromCursorExpiredAndStops(t *testing.T) {
	opt := testOptions()
	opt.segmentBytes = 512
	opt.retainBytes = 1024
	s := mustOpen(t, t.TempDir(), RoleOwner, opt)
	appendN(t, s, 50)
	n := 0
	for _, err := range s.ReadFrom(context.Background(), bus.Cursor{s.Repo(): 2}) {
		n++
		if n == 1 && !errors.Is(err, ErrCursorExpired) {
			t.Fatalf("first yield err = %v", err)
		}
	}
	if n != 1 {
		t.Fatalf("yielded %d times after expiry", n)
	}
}

func TestReadFromStopsEarlyAndHonorsContext(t *testing.T) {
	s := mustOpen(t, t.TempDir(), RoleOwner, testOptions())
	appendN(t, s, 5)
	n := 0
	for _, err := range s.ReadFrom(context.Background(), bus.Cursor{}) {
		if err != nil {
			t.Fatal(err)
		}
		n++
		if n == 2 {
			break
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var gotErr error
	for _, err := range s.ReadFrom(ctx, bus.Cursor{}) {
		gotErr = err
	}
	if !errors.Is(gotErr, context.Canceled) {
		t.Fatalf("canceled ReadFrom err = %v", gotErr)
	}
}

func TestReplayLimit(t *testing.T) {
	s := mustOpen(t, t.TempDir(), RoleOwner, testOptions())
	appendN(t, s, MaxReplay+1)
	if _, err := Replay(context.Background(), s, bus.Cursor{}); !errors.Is(err, ErrReplayLimit) {
		t.Fatalf("Replay of %d err = %v", MaxReplay+1, err)
	}
	got, err := Replay(context.Background(), s, bus.Cursor{s.Repo(): 1})
	if err != nil || len(got) != MaxReplay || got[0].Seq != 2 {
		t.Fatalf("Replay of %d = %d events, %v", MaxReplay, len(got), err)
	}
}

// follow collects n events from f, failing after a timeout.
func follow(t *testing.T, f Follower, from bus.Cursor, n int) <-chan []uint64 {
	t.Helper()
	out := make(chan []uint64, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		var got []uint64
		for e, err := range f.Follow(ctx, from) {
			if err != nil {
				break
			}
			got = append(got, e.Seq)
			if len(got) == n {
				break
			}
		}
		out <- got
	}()
	return out
}

func TestFollowWhileAppending(t *testing.T) {
	dir := t.TempDir()
	opt := testOptions()
	opt.segmentBytes = 2048
	owner := mustOpen(t, dir, RoleOwner, opt)
	appendN(t, owner, 5)
	reader := mustOpen(t, dir, RoleReader, opt)

	const total = 300
	fromReader := follow(t, reader, bus.Cursor{reader.Repo(): 2}, total-2) // polls the files
	fromOwner := follow(t, owner, bus.Cursor{}, total)                     // woken by Append
	for range total - 5 {
		appendN(t, owner, 1)
	}
	wantRange(t, <-fromReader, 3, total)
	wantRange(t, <-fromOwner, 1, total)
	if names := segmentNames(t, owner); len(names) < 3 {
		t.Fatalf("follow test did not cross segments: %v", names)
	}
}

func TestFollowStopsOnClose(t *testing.T) {
	s := mustOpen(t, t.TempDir(), RoleOwner, testOptions())
	done := make(chan struct{})
	go func() {
		for _, err := range s.Follow(context.Background(), bus.Cursor{}) {
			if err != nil {
				break
			}
		}
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	_ = s.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Follow did not stop on Close")
	}
}

func TestConcurrentAppendersAcrossGoroutines(t *testing.T) {
	dir := t.TempDir()
	opt := testOptions()
	opt.lockWait = 30 * time.Second
	opt.segmentBytes = 4096
	shared := mustOpen(t, dir, RoleAppender, opt)
	const workers, each = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for w := range workers {
		s := shared
		if w%2 == 1 { // half share one Store, half have their own
			s = mustOpen(t, dir, RoleAppender, opt)
		}
		wg.Go(func() {
			for range each {
				if _, err := s.Append(context.Background(), event(s.Repo(), time.Now())); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	wantRange(t, seqs(t, shared, bus.Cursor{}), 1, workers*each)
}

// TestHelperProcess is the child of the cross-process tests. It does
// nothing unless STORE_HELPER is set.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("STORE_HELPER")
	if mode == "" {
		t.Skip("helper process only")
	}
	dir := os.Getenv("STORE_DIR")
	ctx := context.Background()
	switch mode {
	case "append":
		n, _ := strconv.Atoi(os.Getenv("STORE_COUNT"))
		opt := defaultOptions()
		opt.segmentBytes = 8192
		s, err := open(ctx, dir, RoleAppender, opt)
		if err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		owned := 0
		deadline := time.Now().Add(60 * time.Second)
		for done := 0; done < n; {
			_, err := s.Append(ctx, event(s.Repo(), time.Now()))
			switch {
			case err == nil:
				done++
			case errors.Is(err, ErrOwned) && time.Now().Before(deadline):
				owned++ // another appender held the lock for longer than lockWait
			default:
				fmt.Println("error", err)
				os.Exit(2)
			}
		}
		fmt.Println("ok", owned)
	case "own":
		_, err := Open(ctx, dir, RoleOwner)
		fmt.Println("owner", errors.Is(err, ErrOwned))
		s, err := Open(ctx, dir, RoleAppender)
		if err != nil {
			fmt.Println("error", err)
			os.Exit(2)
		}
		_, err = s.Append(ctx, event(s.Repo(), time.Now()))
		fmt.Println("appender", errors.Is(err, ErrOwned))
	}
	os.Exit(0)
}

func helper(t *testing.T, mode, dir string, env ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), append([]string{"STORE_HELPER=" + mode, "STORE_DIR=" + dir}, env...)...)
	return cmd
}

func TestConcurrentAppendersAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("starts child processes")
	}
	dir := t.TempDir()
	const procs, each = 4, 60
	var wg sync.WaitGroup
	outs := make([]string, procs)
	errs := make([]error, procs)
	for i := range procs {
		cmd := helper(t, "append", dir, "STORE_COUNT="+strconv.Itoa(each))
		wg.Go(func() {
			out, err := cmd.CombinedOutput()
			outs[i], errs[i] = string(out), err
		})
	}
	wg.Wait()
	for i := range procs {
		if errs[i] != nil || !strings.HasPrefix(outs[i], "ok ") {
			t.Fatalf("child %d: %v\n%s", i, errs[i], outs[i])
		}
	}
	reader := mustOpen(t, dir, RoleReader, testOptions())
	wantRange(t, seqs(t, reader, bus.Cursor{}), 1, procs*each)
	ids := map[string]bool{}
	for e, err := range reader.ReadFrom(context.Background(), bus.Cursor{}) {
		if err != nil || ids[e.ID] {
			t.Fatalf("event %d: duplicate id or %v", e.Seq, err)
		}
		ids[e.ID] = true
	}
}

func TestOwnerExcludesOtherProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("starts child processes")
	}
	dir := t.TempDir()
	owner := mustOpen(t, dir, RoleOwner, testOptions())
	appendN(t, owner, 1)
	out, err := helper(t, "own", dir).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "owner true") || !strings.Contains(string(out), "appender true") {
		t.Fatalf("child while owned: %v\n%s", err, out)
	}
	wantRange(t, seqs(t, owner, bus.Cursor{}), 1, 1)
}

func TestInterceptorWithFileStore(t *testing.T) {
	s := mustOpen(t, t.TempDir(), RoleOwner, testOptions())
	b := bustest.New(bus.Options{Interceptors: []bus.Interceptor{Interceptor(func(r bus.RepoID) (Store, bool) {
		return s, r == s.Repo()
	})}})
	defer func() { _ = b.Close() }()
	for want := uint64(1); want <= 3; want++ {
		e, err := b.Publish(context.Background(), bus.Event{Type: bus.TypeGitRefsChanged, Source: "test", Repo: s.Repo()})
		if err != nil || e.Seq != want {
			t.Fatalf("Publish = seq %d, %v; want %d", e.Seq, err, want)
		}
	}
	wantRange(t, seqs(t, s, bus.Cursor{}), 1, 3)
}

func BenchmarkOwnerAppend(b *testing.B) {
	s := mustOpen(b, b.TempDir(), RoleOwner, defaultOptions())
	e := event(s.Repo(), time.Now())
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.Append(ctx, e); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAppenderAppend(b *testing.B) {
	s := mustOpen(b, b.TempDir(), RoleAppender, defaultOptions())
	e := event(s.Repo(), time.Now())
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.Append(ctx, e); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadFrom(b *testing.B) {
	s := mustOpen(b, b.TempDir(), RoleOwner, defaultOptions())
	appendN(b, s, 10000)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		n := 0
		for _, err := range s.ReadFrom(ctx, bus.Cursor{}) {
			if err != nil {
				b.Fatal(err)
			}
			n++
		}
		if n != 10000 {
			b.Fatalf("read %d events", n)
		}
	}
}
