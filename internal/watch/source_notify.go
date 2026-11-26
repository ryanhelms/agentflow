package watch

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ErrOverflow arrives on Source.Errors when a Source lost changes: the
// kernel queue overflowed (inotify IN_Q_OVERFLOW) or the Source's own
// buffer was full. The Watcher then rescans every Target.
var ErrOverflow = errors.New("watch: change queue overflowed")

// sourceBuffer is the Changes buffer of both built-in Sources. A full
// buffer drops the change and reports ErrOverflow.
const sourceBuffer = 1024

// notifySource is the Event-Driven Consumer: a Source over fsnotify
// (inotify on Linux, kqueue on macOS and BSD, ReadDirectoryChangesW on
// Windows). It watches directories, never single files, because git
// replaces files by renaming a lock file over them.
type notifySource struct {
	w       *fsnotify.Watcher
	changes chan Change
	errs    chan error
	done    chan struct{}

	closeOnce sync.Once
}

// NewNotifySource returns a ModeNotify Source. It fails when the platform
// has no notification backend or the process cannot create one (for
// example the inotify instance limit); the caller then polls.
func NewNotifySource() (Source, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("start file notifications: %w: %w", ErrUnsupported, err)
	}
	s := &notifySource{w: w, changes: make(chan Change, sourceBuffer), errs: make(chan error, 8), done: make(chan struct{})}
	go s.pump()
	return s, nil
}

func (s *notifySource) Mode() Mode { return ModeNotify }

// Add watches dir. A missing dir returns an error wrapping fs.ErrNotExist;
// every other failure (ENOSPC or EMFILE watch limits, EINVAL on a file
// system without notifications, kqueue running out of descriptors) wraps
// ErrUnsupported so the Watcher falls back to polling.
func (s *notifySource) Add(dir string) error {
	err := s.w.Add(dir)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("watch %s: %w", dir, err)
	default:
		// kqueue may have opened descriptors for some entries before it
		// failed; give them back.
		_ = s.w.Remove(dir)
		return fmt.Errorf("watch %s: %w: %w", dir, ErrUnsupported, err)
	}
}

func (s *notifySource) Remove(dir string) error {
	err := s.w.Remove(dir)
	if err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
		return fmt.Errorf("unwatch %s: %w", dir, err)
	}
	return nil
}

func (s *notifySource) Changes() <-chan Change { return s.changes }
func (s *notifySource) Errors() <-chan error   { return s.errs }

func (s *notifySource) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.w.Close()
		<-s.done
	})
	return err
}

// pump converts fsnotify events to Changes. It never blocks on a slow
// reader: a full buffer drops the change and reports ErrOverflow once.
func (s *notifySource) pump() {
	defer close(s.done)
	defer close(s.errs)
	defer close(s.changes)
	for {
		select {
		case ev, ok := <-s.w.Events:
			if !ok {
				return
			}
			kind, ok := notifyKind(ev.Op)
			if !ok {
				continue
			}
			c := Change{Path: filepath.Clean(ev.Name), Kind: kind, Time: time.Now()}
			select {
			case s.changes <- c:
			default:
				s.report(ErrOverflow)
			}
		case err, ok := <-s.w.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				err = fmt.Errorf("%w: %w", ErrOverflow, err)
			}
			s.report(err)
		}
	}
}

func (s *notifySource) report(err error) {
	select {
	case s.errs <- err:
	default: // an error is already waiting; the Watcher rescans anyway
	}
}

// notifyKind maps an fsnotify operation to a Kind. Chmod is ignored: git
// never signals state through permissions, and kqueue reports attribute
// changes as Chmod.
func notifyKind(op fsnotify.Op) (Kind, bool) {
	switch {
	case op.Has(fsnotify.Create):
		return Created, true
	case op.Has(fsnotify.Remove), op.Has(fsnotify.Rename):
		return Removed, true
	case op.Has(fsnotify.Write):
		return Modified, true
	}
	return 0, false
}
