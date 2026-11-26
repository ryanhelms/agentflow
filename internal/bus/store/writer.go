package store

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// writer appends to the active segment. Only the single writer (the owner,
// or an appender holding OwnerLock) has one.
type writer struct {
	f     *os.File  // the active segment; nil before its first line
	first uint64    // the active segment's first Seq
	last  uint64    // the newest stored Seq, 0 when the store is empty
	size  int64     // bytes in the active segment, all complete lines
	start time.Time // Time of the active segment's first event
}

// loadWriter opens the newest segment for appending. It truncates a
// partial last line that a crashed writer left and reads the last Seq.
func loadWriter(dir string) (*writer, error) {
	segs, err := listSegments(dir)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return &writer{}, nil
	}
	seg := segs[len(segs)-1]
	f, err := os.OpenFile(seg.path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return nil, fmt.Errorf("open event segment: %w", err)
	}
	w, err := recoverSegment(f, seg)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return w, nil
}

func recoverSegment(f *os.File, seg segment) (*writer, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("open event segment: %w", err)
	}
	end, line, err := lastLine(f, info.Size())
	if err != nil {
		return nil, err
	}
	if end < info.Size() {
		if err := f.Truncate(end); err != nil {
			return nil, fmt.Errorf("truncate partial event line: %w", err)
		}
	}
	w := &writer{f: f, first: seg.first, last: seg.first - 1, size: end}
	if line == nil {
		return w, nil
	}
	e, err := decode(line, seg.path)
	if err != nil {
		return nil, err
	}
	w.last = e.Seq
	first, err := firstLine(f)
	if err != nil {
		return nil, err
	}
	if first != nil {
		e, err := decode(first, seg.path)
		if err != nil {
			return nil, err
		}
		w.start = e.Time
	}
	return w, nil
}

// full reports whether the active segment must rotate before the next
// line: it holds maxBytes or more, or its first event is maxAge old.
func (w *writer) full(now time.Time, maxBytes int64, maxAge time.Duration) bool {
	if w.f == nil || w.size == 0 {
		return false
	}
	return w.size >= maxBytes || (!w.start.IsZero() && now.Sub(w.start) >= maxAge)
}

// write appends line, which holds e, starting a new segment named e.Seq
// when there is no active one. A failed write is cut back off the file.
func (w *writer) write(dir string, line []byte, e bus.Event) error {
	if w.f == nil {
		f, err := os.OpenFile(filepath.Join(dir, segmentName(e.Seq)), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("create event segment: %w", err)
		}
		w.f, w.first, w.size, w.start = f, e.Seq, 0, time.Time{}
	}
	if _, err := w.f.Write(line); err != nil {
		_ = w.f.Truncate(w.size)
		return fmt.Errorf("append event: %w", err)
	}
	if w.size == 0 {
		w.start = e.Time
	}
	w.size += int64(len(line))
	w.last = e.Seq
	return nil
}

// close closes the active segment, fsyncing it first when sync is set.
func (w *writer) close(sync bool) error {
	if w.f == nil {
		return nil
	}
	var serr error
	if sync {
		serr = w.f.Sync()
	}
	err := errors.Join(serr, w.f.Close())
	w.f = nil
	if err != nil {
		return fmt.Errorf("close event segment: %w", err)
	}
	return nil
}

// lineReader reads complete lines from an offset of a file.
type lineReader struct{ r *bufio.Reader }

func newLineReader(f *os.File, offset int64) *lineReader {
	return &lineReader{r: bufio.NewReaderSize(io.NewSectionReader(f, offset, 1<<62), 64<<10)}
}

// next returns the next complete line without its newline, or nil at the
// end of the file. A partial last line counts as the end.
func (l *lineReader) next() ([]byte, error) {
	line, err := l.r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		long := append([]byte(nil), line...)
		for errors.Is(err, bufio.ErrBufferFull) {
			line, err = l.r.ReadSlice('\n')
			long = append(long, line...)
		}
		line = long
	}
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read event segment: %w", err)
	}
	return line[:len(line)-1], nil
}
