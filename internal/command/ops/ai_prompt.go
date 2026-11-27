package ops

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/aisession"
)

// The terminal side of a foreground session. Agent text is printed with its
// own line breaks, so it can look like a prompt. Two rules keep a person's
// answer on the request they saw:
//
//   - Only a line begun after a prompt is printed answers it. Anything
//     entered earlier, or meant for an earlier prompt, is dropped.
//   - While a prompt waits, transcript items are held and printed when it
//     ends, so nothing appears below the real prompt.
//
// A prompt ends when it is answered, when its request is resolved another
// way (an answer item, its expiry), or when the session ends.

// maxAnswerLine bounds what one input line keeps.
const maxAnswerLine = 4096

type lineResult struct {
	text string
	eof  bool
}

// lineSource reads its input all the time and hands a line only to the
// prompt that was on screen when the line's first byte was read. It never
// queues lines.
type lineSource struct {
	mu     sync.Mutex
	epoch  uint64
	want   uint64
	waiter chan lineResult
	eof    bool
}

func newLineSource(r io.Reader) *lineSource {
	l := &lineSource{}
	go l.read(r)
	return l
}

func (l *lineSource) read(r io.Reader) {
	buf := make([]byte, 4096)
	var line []byte
	var first uint64
	started := false
	for {
		n, err := r.Read(buf)
		l.mu.Lock()
		stamp := l.epoch
		l.mu.Unlock()
		for _, b := range buf[:n] {
			if !started {
				first, started = stamp, true
			}
			if b == '\n' {
				l.deliver(string(line), first)
				line, started = line[:0], false
				continue
			}
			if len(line) < maxAnswerLine {
				line = append(line, b)
			}
		}
		if err != nil {
			// A line without its newline at the end of input is not an answer.
			l.mu.Lock()
			l.eof = true
			if l.waiter != nil {
				l.waiter <- lineResult{eof: true}
				l.waiter = nil
			}
			l.mu.Unlock()
			return
		}
	}
}

func (l *lineSource) deliver(text string, stamp uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.waiter != nil && stamp == l.want {
		l.waiter <- lineResult{text: strings.TrimSpace(text)}
		l.waiter = nil
	}
}

// next starts a prompt: only a line begun from now on answers it. stop
// withdraws the prompt.
func (l *lineSource) next() (lines <-chan lineResult, stop func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.epoch++
	ch := make(chan lineResult, 1)
	if l.eof {
		ch <- lineResult{eof: true}
		return ch, func() {}
	}
	l.want, l.waiter = l.epoch, ch
	return ch, func() {
		l.mu.Lock()
		if l.waiter == ch {
			l.waiter = nil
		}
		l.mu.Unlock()
	}
}

// foregroundPrompt prints a foreground session's items and prompts for its
// requests one at a time.
type foregroundPrompt struct {
	lines   *lineSource
	out     io.Writer
	errOut  io.Writer
	respond func(aisession.AskRecord, aisession.Decision) error
	printer *aiPrinter
	// shown, for tests, is called once a prompt waits for its line.
	shown func(id string)

	mu       sync.Mutex
	queue    []aisession.AskRecord
	resolved map[string]bool
	active   string
	gone     chan struct{} // closed when the active request is resolved
	held     []aisession.Item
	wake     chan struct{}
}

func newForegroundPrompt(in io.Reader, out, errOut io.Writer, respond func(aisession.AskRecord, aisession.Decision) error) *foregroundPrompt {
	return &foregroundPrompt{
		lines: newLineSource(in), out: out, errOut: errOut, respond: respond,
		printer: &aiPrinter{w: out}, resolved: map[string]bool{}, wake: make(chan struct{}, 1),
	}
}

// onItems is HostOptions.OnItems. It never waits for input.
func (p *foregroundPrompt) onItems(items []aisession.Item) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, it := range items {
		if id := answeredAsk(it); id != "" {
			p.resolved[id] = true
			if id == p.active && p.gone != nil {
				close(p.gone)
				p.gone = nil
			}
		}
		if p.active != "" {
			p.held = append(p.held, it)
			continue
		}
		p.printer.item(it)
	}
}

// onAsk is HostOptions.OnAsk.
func (p *foregroundPrompt) onAsk(r aisession.AskRecord) {
	p.mu.Lock()
	if !p.resolved[r.ID] && len(p.queue) < aisession.MaxAsksWaiting {
		p.queue = append(p.queue, r)
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// run prompts until done is closed, then prints what is held.
func (p *foregroundPrompt) run(done <-chan struct{}) {
	defer p.finish()
	for {
		ask, lines, stop, gone, ok := p.show(done)
		if !ok {
			return
		}
		p.wait(ask, lines, stop, gone, done)
	}
}

// show takes the next unresolved request and prints it with its prompt.
func (p *foregroundPrompt) show(done <-chan struct{}) (aisession.AskRecord, <-chan lineResult, func(), <-chan struct{}, bool) {
	for {
		select {
		case <-done:
			return aisession.AskRecord{}, nil, nil, nil, false
		default:
		}
		p.mu.Lock()
		for len(p.queue) > 0 {
			ask := p.queue[0]
			p.queue = p.queue[1:]
			if p.resolved[ask.ID] || !time.Now().Before(ask.ExpiresAt) {
				continue
			}
			p.active, p.gone = ask.ID, make(chan struct{})
			gone := p.gone
			printAsk(p.out, ask)
			if ask.CanAllow {
				fmt.Fprintf(p.out, "Allow once? [y/N] (denied at %s): ", ask.ExpiresAt.Local().Format("15:04:05"))
			} else {
				fmt.Fprint(p.out, "Press Enter to reject it: ")
			}
			lines, stop := p.lines.next()
			p.mu.Unlock()
			if p.shown != nil {
				p.shown(ask.ID)
			}
			return ask, lines, stop, gone, true
		}
		p.mu.Unlock()
		select {
		case <-p.wake:
		case <-done:
			return aisession.AskRecord{}, nil, nil, nil, false
		}
	}
}

// wait ends the prompt on its line, the request being resolved, its expiry
// or the end of the session, and answers only in the first case.
func (p *foregroundPrompt) wait(ask aisession.AskRecord, lines <-chan lineResult, stop func(), gone <-chan struct{}, done <-chan struct{}) {
	expiry := time.NewTimer(time.Until(ask.ExpiresAt))
	defer expiry.Stop()
	var line lineResult
	got, ended := false, false
	select {
	case line = <-lines:
		got = true
	case <-gone:
	case <-expiry.C:
	case <-done:
		ended = true
	}
	stop()
	p.mu.Lock()
	answer := got && !p.resolved[ask.ID]
	p.resolved[ask.ID] = true
	if !answer && !ended {
		fmt.Fprintf(p.out, "\nRequest %s is no longer waiting.\n", ask.ID)
	}
	if ended {
		fmt.Fprintln(p.out)
	}
	p.active, p.gone = "", nil
	p.flushLocked()
	p.mu.Unlock()
	if !answer {
		return
	}
	d := aisession.Deny
	if ask.CanAllow && !line.eof && strings.HasPrefix(strings.ToLower(line.text), "y") {
		d = aisession.Allow
	}
	if err := p.respond(ask, d); err != nil {
		fmt.Fprintf(p.errOut, "agentflow: %v\n", err)
	}
}

func (p *foregroundPrompt) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active = ""
	p.flushLocked()
}

func (p *foregroundPrompt) flushLocked() {
	for _, it := range p.held {
		p.printer.item(it)
	}
	p.held = nil
}

// answeredAsk is the request an answer item resolves, or "".
func answeredAsk(it aisession.Item) string {
	if it.Kind != aisession.ItemAnswer {
		return ""
	}
	var v struct {
		Ask string `json:"ask"`
	}
	if json.Unmarshal(it.Data, &v) != nil {
		return ""
	}
	return v.Ask
}
