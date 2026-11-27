package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// deadLetterRewriteBytes is the file size at which the persistent channel
// rewrites itself with only the kept letters.
const deadLetterRewriteBytes = 1 << 20

// deadLetterKeepBytes is the most a rewrite keeps: the newest letters that
// fit. It leaves room for many appends before the next rewrite.
const deadLetterKeepBytes = deadLetterRewriteBytes / 4

// deadLetterPayloadBytes caps a stored letter's event payload. A larger
// payload is replaced by {"dropped_bytes": N}; the event's ID and type
// still name it.
const deadLetterPayloadBytes = 4 << 10

// deadLetterReasonBytes caps a stored letter's reason.
const deadLetterReasonBytes = 1 << 10

// fileDeadLetter is the host's persistent Dead Letter Channel (ADR, "Phase
// 5 Dead Letter Channel"): <home>/plugins/deadletter.jsonl, mode 0600, the
// last DeadLetterKeep letters, rewritten atomically when it passes 1 MiB.
// A write error loses the letter on disk only: it stays in memory.
type fileDeadLetter struct {
	path string

	mu      sync.Mutex
	letters []bus.Letter // oldest first, at most DeadLetterKeep
	size    int64
}

var _ bus.DeadLetter = (*fileDeadLetter)(nil)

// openDeadLetter loads the kept letters of path. A missing or unreadable
// file starts empty; unreadable lines are skipped.
func openDeadLetter(path string) *fileDeadLetter {
	d := &fileDeadLetter{path: path}
	f, err := os.Open(path)
	if err != nil {
		return d
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), bus.MaxPayloadBytes*2)
	for sc.Scan() {
		var l bus.Letter
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			d.keep(trimLetter(l))
		}
	}
	if st, err := f.Stat(); err == nil {
		d.size = st.Size()
	}
	return d
}

func (d *fileDeadLetter) keep(l bus.Letter) {
	d.letters = append(d.letters, l)
	if n := len(d.letters) - DeadLetterKeep; n > 0 {
		d.letters = append(d.letters[:0:0], d.letters[n:]...)
	}
}

// trimLetter caps l's payload and reason so one letter stays small.
func trimLetter(l bus.Letter) bus.Letter {
	if n := len(l.Event.Payload); n > deadLetterPayloadBytes {
		l.Event.Payload = json.RawMessage(fmt.Sprintf(`{"dropped_bytes":%d}`, n))
	}
	l.Reason = clip(l.Reason, deadLetterReasonBytes)
	return l
}

// Dead implements bus.DeadLetter.
func (d *fileDeadLetter) Dead(_ context.Context, l bus.Letter) {
	l = trimLetter(l)
	line, err := json.Marshal(l)
	if err != nil {
		return
	}
	line = append(line, '\n')
	d.mu.Lock()
	defer d.mu.Unlock()
	d.keep(l)
	if d.size+int64(len(line)) > deadLetterRewriteBytes {
		d.rewrite()
		return
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(d.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	n, _ := f.Write(line)
	_ = f.Close()
	d.size += int64(n)
}

// rewrite replaces the file with the newest kept letters that fit in
// deadLetterKeepBytes, atomically, and keeps only those in memory.
func (d *fileDeadLetter) rewrite() {
	lines := make([][]byte, len(d.letters))
	first, total := len(d.letters), 0
	for i := len(d.letters) - 1; i >= 0; i-- {
		line, err := json.Marshal(d.letters[i])
		if err != nil {
			continue
		}
		if total+len(line)+1 > deadLetterKeepBytes {
			break
		}
		lines[i], total, first = line, total+len(line)+1, i
	}
	d.letters = append(d.letters[:0:0], d.letters[first:]...)
	var buf bytes.Buffer
	for _, line := range lines[first:] {
		if line != nil {
			buf.Write(line)
			buf.WriteByte('\n')
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(d.path), ".deadletter-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if os.Rename(tmp.Name(), d.path) == nil {
		d.size = int64(buf.Len())
	}
}

// Letters implements bus.DeadLetter, oldest first.
func (d *fileDeadLetter) Letters() []bus.Letter {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]bus.Letter(nil), d.letters...)
}
