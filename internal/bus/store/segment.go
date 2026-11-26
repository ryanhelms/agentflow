package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// segment is one segment file: its first Seq and its path.
type segment struct {
	first uint64
	path  string
}

// segmentDigits is the width of a segment's zero-padded first Seq.
const segmentDigits = 20

// segmentName returns the file name of the segment that starts at first.
func segmentName(first uint64) string {
	return fmt.Sprintf("%0*d%s", segmentDigits, first, SegmentExt)
}

// parseSegmentName returns the first Seq of a segment file name, and false
// for any other file in the store directory.
func parseSegmentName(name string) (uint64, bool) {
	digits, ok := strings.CutSuffix(name, SegmentExt)
	if !ok || len(digits) != segmentDigits {
		return 0, false
	}
	first, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || first == 0 {
		return 0, false
	}
	return first, true
}

// listSegments returns the segments in dir, oldest first. A missing
// directory has no segments.
func listSegments(dir string) ([]segment, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list event segments: %w", err)
	}
	var segs []segment
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if first, ok := parseSegmentName(entry.Name()); ok {
			segs = append(segs, segment{first: first, path: filepath.Join(dir, entry.Name())})
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].first < segs[j].first })
	return segs, nil
}

// tailChunk is how far lastLine reads backwards at a time.
const tailChunk = 64 << 10

// lastLine returns the offset just past the last complete line of f, which
// is size bytes long, and that line without its newline. A file without a
// complete line returns (0, nil). Bytes after end are a partial line.
func lastLine(f *os.File, size int64) (end int64, line []byte, err error) {
	var buf []byte // the bytes [pos, size) of f
	pos := size
	newline := -1 // index in buf of the last '\n'
	for pos > 0 {
		n := min(int64(tailChunk), pos)
		pos -= n
		chunk := make([]byte, n, int64(len(buf))+n)
		if _, err := f.ReadAt(chunk, pos); err != nil {
			return 0, nil, fmt.Errorf("read event segment: %w", err)
		}
		buf = append(chunk, buf...)
		if newline < 0 {
			newline = bytes.LastIndexByte(buf, '\n')
			if newline < 0 {
				continue
			}
		} else {
			newline += int(n)
		}
		if start := bytes.LastIndexByte(buf[:newline], '\n'); start >= 0 {
			return pos + int64(newline) + 1, buf[start+1 : newline], nil
		}
	}
	if newline < 0 {
		return 0, nil, nil
	}
	return int64(newline) + 1, buf[:newline], nil
}

// firstLine returns the first complete line of f without its newline, or
// nil when f has none.
func firstLine(f *os.File) ([]byte, error) {
	line, err := bufio.NewReader(io.NewSectionReader(f, 0, 1<<62)).ReadBytes('\n')
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read event segment: %w", err)
	}
	return line[:len(line)-1], nil
}

// decode parses one store line.
func decode(line []byte, path string) (bus.Event, error) {
	var e bus.Event
	if err := json.Unmarshal(line, &e); err != nil {
		return bus.Event{}, fmt.Errorf("corrupt event in %s: %w", filepath.Base(path), err)
	}
	if e.Seq == 0 {
		return bus.Event{}, fmt.Errorf("corrupt event in %s: no seq", filepath.Base(path))
	}
	return e, nil
}

// segmentTimes reads what retention and rotation need from a segment: its
// oldest and its newest event's time. Times are zero for an
// empty segment.
func segmentTimes(path string) (oldest, newest time.Time, err error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	_, last, err := lastLine(f, info.Size())
	if err != nil || last == nil {
		return time.Time{}, time.Time{}, err
	}
	e, err := decode(last, path)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	newest = e.Time
	first, err := firstLine(f)
	if err != nil || first == nil {
		return time.Time{}, newest, err
	}
	e, err = decode(first, path)
	if err != nil {
		return time.Time{}, newest, err
	}
	return e.Time, newest, nil
}
