package watch

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// smallFile is the size up to which a polling Source also hashes a file's
// content. HEAD, loose refs and the *_HEAD files are a few dozen bytes, so
// a rewrite that keeps size and mtime (coarse timestamps, or no inode on
// Windows) is still seen.
const smallFile = 4 << 10

// racyWindow is how close to the previous scan a file's mtime must be for
// the polling Source to hash it again although its stat data did not
// change. It covers file systems with 1 s or 2 s timestamps, the same
// "racy" rule git applies to its index.
const racyWindow = 2 * time.Second

// PollOptions configures a polling Source.
type PollOptions struct {
	// Interval is how often directories are scanned; 0 means PollInterval.
	Interval time.Duration
	// SlowInterval is how often directories for which Slow returns true are
	// scanned; 0 means PollRefsInterval.
	SlowInterval time.Duration
	// Slow selects the directories scanned every SlowInterval (the Watcher
	// uses it for refs/). Nil means none.
	Slow func(dir string) bool
}

// fingerprint is what a polling Source remembers about one entry.
type fingerprint struct {
	dir  bool
	mod  int64
	size int64
	ino  uint64
	sum  [sha256.Size]byte
	hash bool
}

type pollDir struct {
	refs     int
	exists   bool
	entries  map[string]fingerprint
	scanned  time.Time
	slow     bool
	readFail bool
}

// pollSource is the Polling Consumer: it lists each added directory on an
// interval and compares every entry's mtime, size, inode and, for small
// files, content hash. Unlike fsnotify it accepts a directory that does not
// exist yet and reports it (and its entries) as Created when it appears.
type pollSource struct {
	opts    PollOptions
	changes chan Change
	errs    chan error
	stop    chan struct{}
	done    chan struct{}

	mu     sync.Mutex
	dirs   map[string]*pollDir
	closed bool
}

// NewPollSource returns a ModePoll Source.
func NewPollSource(opts PollOptions) Source {
	if opts.Interval <= 0 {
		opts.Interval = PollInterval
	}
	if opts.SlowInterval <= 0 {
		opts.SlowInterval = PollRefsInterval
	}
	s := &pollSource{opts: opts, changes: make(chan Change, sourceBuffer), errs: make(chan error, 8),
		stop: make(chan struct{}), done: make(chan struct{}), dirs: map[string]*pollDir{}}
	go s.loop()
	return s
}

func (s *pollSource) Mode() Mode { return ModePoll }

// Add records dir's current entries as the baseline; later scans report
// differences. Adding a directory twice needs two Removes.
func (s *pollSource) Add(dir string) error {
	dir = filepath.Clean(dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("watch: poll source closed")
	}
	if d, ok := s.dirs[dir]; ok {
		d.refs++
		return nil
	}
	d := &pollDir{refs: 1, slow: s.opts.Slow != nil && s.opts.Slow(dir)}
	now := time.Now()
	entries, exists, err := scanDir(dir, nil, time.Time{})
	if err != nil {
		return fmt.Errorf("poll %s: %w", dir, err)
	}
	d.entries, d.exists, d.scanned = entries, exists, now
	s.dirs[dir] = d
	return nil
}

func (s *pollSource) Remove(dir string) error {
	dir = filepath.Clean(dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.dirs[dir]; ok {
		if d.refs--; d.refs <= 0 {
			delete(s.dirs, dir)
		}
	}
	return nil
}

func (s *pollSource) Changes() <-chan Change { return s.changes }
func (s *pollSource) Errors() <-chan error   { return s.errs }

func (s *pollSource) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	close(s.stop)
	<-s.done
	return nil
}

func (s *pollSource) loop() {
	defer close(s.done)
	defer close(s.errs)
	defer close(s.changes)
	tick := time.NewTicker(s.opts.Interval)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-tick.C:
			s.scan(now)
		}
	}
}

// scan compares every due directory with its last listing. The lock is
// held for the whole scan so Add and Remove never race a listing.
func (s *pollSource) scan(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for dir, d := range s.dirs {
		if d.slow && now.Sub(d.scanned) < s.opts.SlowInterval {
			continue
		}
		entries, exists, err := scanDir(dir, d.entries, d.scanned)
		if err != nil {
			if !d.readFail {
				s.report(fmt.Errorf("poll %s: %w", dir, err))
			}
			d.readFail = true
			continue
		}
		d.readFail = false
		if exists && !d.exists {
			s.emit(dir, Created, now)
		}
		for name, fp := range entries {
			old, ok := d.entries[name]
			switch {
			case !ok:
				s.emit(filepath.Join(dir, name), Created, now)
			case old.dir != fp.dir:
				s.emit(filepath.Join(dir, name), Removed, now)
				s.emit(filepath.Join(dir, name), Created, now)
			case !fp.dir && old.changed(fp):
				s.emit(filepath.Join(dir, name), Modified, now)
			}
		}
		for name := range d.entries {
			if _, ok := entries[name]; !ok {
				s.emit(filepath.Join(dir, name), Removed, now)
			}
		}
		if !exists && d.exists {
			s.emit(dir, Removed, now)
		}
		d.entries, d.exists, d.scanned = entries, exists, now
	}
}

func (s *pollSource) emit(path string, kind Kind, now time.Time) {
	select {
	case s.changes <- Change{Path: path, Kind: kind, Time: now}:
	default:
		s.report(ErrOverflow)
	}
}

func (s *pollSource) report(err error) {
	select {
	case s.errs <- err:
	default:
	}
}

func (f fingerprint) changed(g fingerprint) bool {
	if f.mod != g.mod || f.size != g.size || f.ino != g.ino || f.hash != g.hash {
		return true
	}
	return f.hash && f.sum != g.sum
}

// scanDir lists dir. A missing dir is not an error: it reports
// exists=false. prev and prevScan let unchanged, settled files reuse their
// hash instead of being read again.
func scanDir(dir string, prev map[string]fingerprint, prevScan time.Time) (map[string]fingerprint, bool, error) {
	list, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]fingerprint{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	out := make(map[string]fingerprint, len(list))
	for _, e := range list {
		fi, err := e.Info()
		if err != nil {
			continue // removed while listing
		}
		fp := fingerprint{dir: fi.IsDir(), mod: fi.ModTime().UnixNano(), size: fi.Size(), ino: fileIno(fi)}
		if fp.dir || !fi.Mode().IsRegular() || fp.size > smallFile {
			out[e.Name()] = fp
			continue
		}
		old, ok := prev[e.Name()]
		settled := ok && old.hash && old.mod == fp.mod && old.size == fp.size && old.ino == fp.ino &&
			prevScan.Sub(time.Unix(0, fp.mod)) > racyWindow
		if settled {
			fp.sum, fp.hash = old.sum, true
		} else if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			fp.sum, fp.hash = sha256.Sum256(b), true
		}
		out[e.Name()] = fp
	}
	return out, true, nil
}
