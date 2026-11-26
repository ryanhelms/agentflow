package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/filelock"
)

// Tuning that is not part of the contract.
const (
	// followPoll is how often Follow looks for events that another process
	// appended. Events appended through the same Store wake it at once.
	followPoll = 100 * time.Millisecond
	// appendLockWait is how long an appender retries filelock.TryExclusive
	// before it reports ErrOwned. The owner holds the lock for its whole
	// lifetime, so the retries only absorb brief overlaps between two
	// appenders; an owned store still fails after this short wait.
	appendLockWait = 250 * time.Millisecond
	// repoIDWait is how long Open waits for another process that has just
	// created repo-id to write it.
	repoIDWait = time.Second
)

// options are the limits a store applies. Tests shrink them.
type options struct {
	now          func() time.Time
	segmentBytes int64
	segmentAge   time.Duration
	retainBytes  int64
	retainAge    time.Duration
	poll         time.Duration
	lockWait     time.Duration
}

func defaultOptions() options {
	return options{
		now:          time.Now,
		segmentBytes: SegmentMaxBytes,
		segmentAge:   SegmentMaxAge,
		retainBytes:  RetainBytes,
		retainAge:    RetainAge,
		poll:         followPoll,
		lockWait:     appendLockWait,
	}
}

// Open opens the file store of the repository whose common git dir is
// commonDir, in the store directory Path(commonDir).
//
// RoleOwner and RoleAppender create the directory and repo-id when they are
// missing. RoleOwner takes OwnerLock for the store's lifetime (ErrOwned when
// another process holds it), truncates a partial last line left by a crash,
// reads the last Seq and applies retention. RoleReader never writes: it
// fails with an error matching os.ErrNotExist when no store exists yet.
//
// The returned Store also implements Follower.
//
// Durability: the owner keeps the active segment open and does not fsync
// each line. It fsyncs a segment when it rotates and when the store closes.
// An appender closes the segment after each Append without fsync, because
// the data is already in the operating system's cache and a process exit
// does not lose it; it fsyncs on rotation. A power loss can lose the newest
// events. Events are notifications, so the next snapshot repairs that.
func Open(ctx context.Context, commonDir string, role Role) (Store, error) {
	return open(ctx, commonDir, role, defaultOptions())
}

func open(ctx context.Context, commonDir string, role Role, opt options) (*fileStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if role != RoleOwner && role != RoleAppender && role != RoleReader {
		return nil, fmt.Errorf("open event store: unknown role %d", role)
	}
	s := &fileStore{dir: Path(commonDir), role: role, opt: opt, changed: make(chan struct{})}
	if role != RoleReader {
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return nil, fmt.Errorf("create event store: %w", err)
		}
	}
	repo, err := readRepoID(ctx, s.dir, role != RoleReader)
	if err != nil {
		return nil, err
	}
	s.repo = repo
	if role != RoleOwner {
		return s, nil
	}
	lock, ok, err := lockOwner(s.dir)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrOwned
	}
	w, err := loadWriter(s.dir)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	s.lock, s.w = lock, w
	s.retain()
	return s, nil
}

// readRepoID reads repo-id, creating it first when create is set.
func readRepoID(ctx context.Context, dir string, create bool) (bus.RepoID, error) {
	path := filepath.Join(dir, RepoIDFile)
	if create {
		var b [8]byte
		_, _ = rand.Read(b[:]) // Since Go 1.24, crypto/rand.Read never returns an error.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		switch {
		case err == nil:
			_, werr := f.WriteString(hex.EncodeToString(b[:]))
			serr := f.Sync()
			if err := errors.Join(werr, serr, f.Close()); err != nil {
				return "", fmt.Errorf("write event store repo-id: %w", err)
			}
		case !errors.Is(err, os.ErrExist):
			return "", fmt.Errorf("create event store repo-id: %w", err)
		}
	}
	// Another process may have created the file and not written it yet.
	deadline := time.Now().Add(repoIDWait)
	for {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read event store repo-id: %w", err)
		}
		repo := bus.RepoID(strings.TrimSpace(string(data)))
		if repo.Valid() && repo != bus.GlobalRepo {
			return repo, nil
		}
		if len(data) != 0 || time.Now().After(deadline) {
			return "", fmt.Errorf("event store repo-id %q is not 16 hex characters", data)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// lockOwner opens OwnerLock and tries to lock it once. The lock is held
// until the returned file is closed.
func lockOwner(dir string) (*os.File, bool, error) {
	f, err := os.OpenFile(filepath.Join(dir, OwnerLock), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open event store lock: %w", err)
	}
	ok, err := filelock.TryExclusive(f)
	if err != nil || !ok {
		_ = f.Close()
		if err != nil {
			return nil, false, fmt.Errorf("lock event store: %w", err)
		}
		return nil, false, nil
	}
	return f, true, nil
}

// fileStore is the file-backed Store and Follower of one repository.
type fileStore struct {
	dir  string
	repo bus.RepoID
	role Role
	opt  options

	mu      sync.Mutex
	closed  bool
	changed chan struct{} // closed and replaced on each Append and on Close
	lock    *os.File      // RoleOwner: OwnerLock, held until Close
	w       *writer       // RoleOwner: the active segment
}

var (
	_ Store    = (*fileStore)(nil)
	_ Follower = (*fileStore)(nil)
)

// Repo implements Store.
func (s *fileStore) Repo() bus.RepoID { return s.repo }

// Append implements Store.
func (s *fileStore) Append(ctx context.Context, e bus.Event) (bus.Event, error) {
	if s.role == RoleReader {
		return bus.Event{}, ErrReadOnly
	}
	if info, ok := bus.Lookup(e.Type); ok && info.Transient {
		return bus.Event{}, ErrTransient
	}
	if e.Repo != s.repo {
		return bus.Event{}, ErrWrongRepo
	}
	if e.Seq != 0 {
		return bus.Event{}, bus.ErrSequenced
	}
	if err := e.Validate(); err != nil {
		return bus.Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return bus.Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return bus.Event{}, bus.ErrClosed
	}
	var err error
	if s.role == RoleOwner {
		e, err = s.appendTo(s.w, e)
	} else {
		e, err = s.appendLocked(ctx, e)
	}
	if err != nil {
		return bus.Event{}, err
	}
	close(s.changed)
	s.changed = make(chan struct{})
	return e, nil
}

// appendLocked is an appender's Append: take OwnerLock, load the active
// segment, append, release.
func (s *fileStore) appendLocked(ctx context.Context, e bus.Event) (bus.Event, error) {
	deadline := time.Now().Add(s.opt.lockWait)
	var lock *os.File
	for wait := time.Millisecond; ; wait = min(2*wait, 20*time.Millisecond) {
		f, ok, err := lockOwner(s.dir)
		if err != nil {
			return bus.Event{}, err
		}
		if ok {
			lock = f
			break
		}
		if time.Now().After(deadline) {
			return bus.Event{}, ErrOwned
		}
		select {
		case <-ctx.Done():
			return bus.Event{}, ctx.Err()
		case <-time.After(wait):
		}
	}
	defer func() { _ = lock.Close() }()
	w, err := loadWriter(s.dir)
	if err != nil {
		return bus.Event{}, err
	}
	e, err = s.appendTo(w, e)
	if cerr := w.close(false); err == nil && cerr != nil {
		return bus.Event{}, cerr
	}
	return e, err
}

// appendTo rotates w when needed, appends e with the next Seq, and applies
// retention after a rotation. The caller is the single writer.
func (s *fileStore) appendTo(w *writer, e bus.Event) (bus.Event, error) {
	rotated := false
	if w.full(s.opt.now(), s.opt.segmentBytes, s.opt.segmentAge) {
		if err := w.close(true); err != nil {
			return bus.Event{}, err
		}
		rotated = true
	}
	e.Seq = w.last + 1
	line, err := json.Marshal(e)
	if err != nil {
		return bus.Event{}, fmt.Errorf("encode event: %w", err)
	}
	if err := w.write(s.dir, append(line, '\n'), e); err != nil {
		return bus.Event{}, err
	}
	if rotated {
		s.retain()
	}
	return e, nil
}

// retain deletes the oldest closed segments while the store is larger than
// retainBytes, and closed segments whose newest event is older than
// retainAge. It stops at the first segment it keeps, so the retained
// segments stay contiguous. The newest segment is never deleted. Retention
// is best effort: a segment it cannot remove (on Windows, one a reader has
// open) stays until the next pass.
func (s *fileStore) retain() {
	segs, err := listSegments(s.dir)
	if err != nil || len(segs) < 2 {
		return
	}
	sizes := make([]int64, len(segs))
	var total int64
	for i, seg := range segs {
		if info, err := os.Stat(seg.path); err == nil {
			sizes[i] = info.Size()
			total += sizes[i]
		}
	}
	now := s.opt.now()
	for i, seg := range segs[:len(segs)-1] {
		if total <= s.opt.retainBytes {
			_, newest, err := segmentTimes(seg.path)
			if err != nil || newest.IsZero() || now.Sub(newest) <= s.opt.retainAge {
				return
			}
		}
		if os.Remove(seg.path) != nil {
			return
		}
		total -= sizes[i]
	}
}

// Head implements Reader.
func (s *fileStore) Head(ctx context.Context) (bus.Cursor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.w != nil && !s.closed {
		last := s.w.last
		s.mu.Unlock()
		return s.cursor(last), nil
	}
	s.mu.Unlock()
	last, err := lastSeq(s.dir)
	if err != nil {
		return nil, err
	}
	return s.cursor(last), nil
}

func (s *fileStore) cursor(last uint64) bus.Cursor {
	c := bus.Cursor{}
	if last > 0 {
		c[s.repo] = last
	}
	return c
}

// lastSeq reads the Seq of the newest complete line on disk.
func lastSeq(dir string) (uint64, error) {
	segs, err := listSegments(dir)
	if err != nil || len(segs) == 0 {
		return 0, err
	}
	seg := segs[len(segs)-1]
	f, err := os.Open(seg.path)
	if err != nil {
		return 0, fmt.Errorf("open event segment: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("open event segment: %w", err)
	}
	_, line, err := lastLine(f, info.Size())
	if err != nil {
		return 0, err
	}
	if line == nil {
		return seg.first - 1, nil
	}
	e, err := decode(line, seg.path)
	return e.Seq, err
}

// ReadFrom implements Reader.
func (s *fileStore) ReadFrom(ctx context.Context, from bus.Cursor) iter.Seq2[bus.Event, error] {
	return s.read(ctx, from, false)
}

// Follow implements Follower. It stops when ctx is done or the store is
// closed.
func (s *fileStore) Follow(ctx context.Context, from bus.Cursor) iter.Seq2[bus.Event, error] {
	return s.read(ctx, from, true)
}

// read yields the events after from. With follow it keeps the position it
// reached and polls for more.
func (s *fileStore) read(ctx context.Context, from bus.Cursor, follow bool) iter.Seq2[bus.Event, error] {
	return func(yield func(bus.Event, error) bool) {
		seen := from[s.repo]
		var pos *position
		for {
			s.mu.Lock()
			changed, closed := s.changed, s.closed
			s.mu.Unlock()
			if follow && closed {
				return
			}
			segs, err := listSegments(s.dir)
			if err != nil {
				yield(bus.Event{}, err)
				return
			}
			if pos == nil && len(segs) > 0 {
				// A reader that has seen something must have seen
				// everything before it; one that has seen nothing starts
				// at the oldest retained event.
				if seen > 0 && seen+1 < segs[0].first {
					yield(bus.Event{}, ErrCursorExpired)
					return
				}
				i := 0
				for i+1 < len(segs) && segs[i+1].first <= seen+1 {
					i++
				}
				pos = &position{seg: segs[i]}
			}
			if pos != nil {
				done, lost := s.readSegments(ctx, segs, pos, &seen, yield)
				if done {
					return
				}
				if lost {
					// Retention deleted the segment before it was read;
					// look for the position again.
					pos = nil
					continue
				}
			}
			if !follow {
				return
			}
			timer := time.NewTimer(s.opt.poll)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-changed:
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}

// position is where a reader stopped: a segment and the offset just past
// the last complete line it read there.
type position struct {
	seg    segment
	offset int64
}

// readSegments reads from pos through the newest segment in segs. done
// reports that iteration must stop (the consumer stopped or an error was
// yielded). lost reports that pos's segment no longer exists.
func (s *fileStore) readSegments(ctx context.Context, segs []segment, pos *position, seen *uint64, yield func(bus.Event, error) bool) (done, lost bool) {
	start := -1
	for i, seg := range segs {
		if seg.first == pos.seg.first {
			start = i
			break
		}
	}
	if start < 0 {
		return false, true
	}
	for i := start; i < len(segs); i++ {
		if i > start {
			*pos = position{seg: segs[i]}
		}
		done, err := readSegment(ctx, pos, seen, yield)
		if errors.Is(err, os.ErrNotExist) {
			return false, true
		}
		if err != nil {
			yield(bus.Event{}, err)
			return true, false
		}
		if done {
			return true, false
		}
	}
	return false, false
}

// readSegment yields the complete lines of pos's segment from pos.offset on
// whose Seq is after *seen, and moves pos and *seen forward. A partial last
// line is left for the next read.
func readSegment(ctx context.Context, pos *position, seen *uint64, yield func(bus.Event, error) bool) (bool, error) {
	f, err := os.Open(pos.seg.path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	r := newLineReader(f, pos.offset)
	for {
		line, err := r.next()
		if err != nil {
			return false, err
		}
		if line == nil {
			return false, nil
		}
		pos.offset += int64(len(line)) + 1
		e, err := decode(line, pos.seg.path)
		if err != nil {
			return false, err
		}
		if e.Seq <= *seen {
			continue
		}
		if err := ctx.Err(); err != nil {
			yield(bus.Event{}, err)
			return true, nil
		}
		*seen = e.Seq
		if !yield(e, nil) {
			return true, nil
		}
	}
}

// Close implements Store. For RoleOwner it fsyncs and closes the active
// segment and releases OwnerLock.
func (s *fileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.changed)
	s.changed = make(chan struct{})
	var errs []error
	if s.w != nil {
		errs = append(errs, s.w.close(true))
	}
	if s.lock != nil {
		errs = append(errs, s.lock.Close())
	}
	return errors.Join(errs...)
}
