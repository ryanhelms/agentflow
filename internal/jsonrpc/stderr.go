package jsonrpc

import (
	"bytes"
	"io"
	"sync"
)

// stderrLineCap is the longest stderr line passed on; the rest of a longer
// line is dropped and lineTruncated marks the cut.
const stderrLineCap = 4 << 10

var (
	lineTruncated   = []byte(" [line truncated]\n")
	stderrTruncated = []byte("[stderr truncated]\n")
)

// stderrStream passes a child's stderr to out line by line as it arrives.
// Each line is cut at stderrLineCap, and at most budget bytes are written in
// all: the line that would exceed it is replaced by one stderrTruncated
// line and everything after is dropped. The newest budget bytes, as
// written by the child, stay in tail.
type stderrStream struct {
	mu        sync.Mutex
	out       io.Writer // nil discards
	budget    int
	written   int
	exhausted bool
	line      []byte
	skipping  bool // dropping the rest of a cut line
	tail      *tailBuffer
}

func newStderrStream(out io.Writer, budget int) *stderrStream {
	return &stderrStream{out: out, budget: budget, tail: &tailBuffer{limit: budget}}
}

func (s *stderrStream) Write(b []byte) (int, error) {
	n := len(b)
	_, _ = s.tail.Write(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		chunk := b
		if i >= 0 {
			chunk = b[:i]
		}
		if !s.skipping {
			room := stderrLineCap - len(s.line)
			if len(chunk) > room {
				s.line = append(s.line, chunk[:room]...)
				s.emit(append(s.line, lineTruncated...))
				s.line, s.skipping = s.line[:0], true
			} else {
				s.line = append(s.line, chunk...)
			}
		}
		if i < 0 {
			break
		}
		if !s.skipping {
			s.emit(append(s.line, '\n'))
		}
		s.line, s.skipping = s.line[:0], false
		b = b[i+1:]
	}
	return n, nil // never fail the child's copy
}

// finish passes on a last line that had no newline.
func (s *stderrStream) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.line) > 0 && !s.skipping {
		s.emit(append(s.line, '\n'))
	}
	s.line, s.skipping = s.line[:0], false
}

func (s *stderrStream) emit(line []byte) {
	if s.out == nil || s.exhausted {
		return
	}
	if s.written+len(line) > s.budget {
		s.exhausted = true
		_, _ = s.out.Write(stderrTruncated)
		return
	}
	s.written += len(line)
	_, _ = s.out.Write(line)
}
