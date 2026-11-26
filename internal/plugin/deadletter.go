package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// deadLetterRewriteBytes is the file size at which the persistent channel
// rewrites itself with only the kept letters.
const deadLetterRewriteBytes = 1 << 20

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
			d.keep(l)
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

// Dead implements bus.DeadLetter.
func (d *fileDeadLetter) Dead(_ context.Context, l bus.Letter) {
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

// rewrite replaces the file with the kept letters, atomically.
func (d *fileDeadLetter) rewrite() {
	var buf bytes.Buffer
	for _, l := range d.letters {
		if line, err := json.Marshal(l); err == nil {
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
