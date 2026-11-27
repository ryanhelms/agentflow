package plugin

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// MaxLogLine caps one log line's message.
const MaxLogLine = 4 << 10

// pluginLog is <data>/plugin.log: the only place a plugin's own text is
// written. It rotates at LogFileBytes, keeping one old file (.1).
type pluginLog struct {
	path string
	now  func() time.Time

	mu   sync.Mutex
	size int64
}

func newPluginLog(path string, now func() time.Time) *pluginLog {
	l := &pluginLog{path: path, now: now}
	if st, err := os.Stat(path); err == nil {
		l.size = st.Size()
	}
	return l
}

// Line writes one line: the time, the level and message, with control
// characters replaced and the message cut to MaxLogLine.
func (l *pluginLog) Line(level, message string) {
	if l == nil {
		return
	}
	message = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, message)
	_, _ = fmt.Fprintf(l, "%s %s %s\n", l.now().UTC().Format(time.RFC3339), oneLine(level, 8), oneLine(message, MaxLogLine))
}

// Write appends p (stderr of a process plugin goes here too).
func (l *pluginLog) Write(p []byte) (int, error) {
	if l == nil {
		return len(p), nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size+int64(len(p)) > LogFileBytes {
		_ = os.Rename(l.path, l.path+".1")
		l.size = 0
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := f.Write(p)
	l.size += int64(n)
	return n, err
}
