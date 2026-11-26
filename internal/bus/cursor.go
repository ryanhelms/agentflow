package bus

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// MaxCursorRepos caps how many repositories one cursor (and so one stream)
// can track.
const MaxCursorRepos = 64

// cursorPrefix versions the cursor's string form.
const cursorPrefix = "v1:"

// ErrInvalidCursor is returned by ParseCursor.
var ErrInvalidCursor = errors.New("invalid event cursor")

// Cursor is a Durable Subscriber's resume point: for each repository, the
// Seq of the last event the reader has seen. A missing repository means
// "nothing seen" (read from the oldest retained event). Each repository has
// its own sequence because one daemon serves many repositories, each with its
// own store.
//
// Treat a Cursor as a value: methods never modify the receiver.
type Cursor map[RepoID]uint64

// String encodes c as "v1:<repo>=<seq>,<repo>=<seq>", repositories sorted,
// entries with Seq 0 left out. The empty cursor encodes as "". The encoding
// is the SSE "id:" field and the Last-Event-ID header, and contains only
// [0-9a-z:=,], so it is safe in headers and URLs.
func (c Cursor) String() string {
	repos := make([]string, 0, len(c))
	for r, seq := range c {
		if seq > 0 {
			repos = append(repos, string(r))
		}
	}
	if len(repos) == 0 {
		return ""
	}
	sort.Strings(repos)
	var b strings.Builder
	b.WriteString(cursorPrefix)
	for i, r := range repos {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(r)
		b.WriteByte('=')
		b.WriteString(strconv.FormatUint(c[RepoID(r)], 10))
	}
	return b.String()
}

// ParseCursor decodes the String form. It accepts "" as the empty cursor
// and rejects anything else that String would not produce.
func ParseCursor(s string) (Cursor, error) {
	c := Cursor{}
	if s == "" {
		return c, nil
	}
	body, ok := strings.CutPrefix(s, cursorPrefix)
	if !ok || body == "" {
		return nil, fmt.Errorf("%w: %q", ErrInvalidCursor, s)
	}
	parts := strings.Split(body, ",")
	if len(parts) > MaxCursorRepos {
		return nil, fmt.Errorf("%w: more than %d repositories", ErrInvalidCursor, MaxCursorRepos)
	}
	prev := ""
	for _, part := range parts {
		repo, num, ok := strings.Cut(part, "=")
		seq, err := strconv.ParseUint(num, 10, 64)
		if !ok || err != nil || seq == 0 || !RepoID(repo).Valid() || repo <= prev || num != strconv.FormatUint(seq, 10) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidCursor, s)
		}
		c[RepoID(repo)] = seq
		prev = repo
	}
	return c, nil
}

// Clone returns a copy of c.
func (c Cursor) Clone() Cursor {
	out := make(Cursor, len(c))
	for r, seq := range c {
		out[r] = seq
	}
	return out
}

// Seen reports whether e is at or before c, so a reader resuming from c has
// already received it. Unstored events (Seq 0) are never seen.
func (c Cursor) Seen(e Event) bool { return e.Seq > 0 && e.Seq <= c[e.Repo] }

// Advance returns a copy of c moved forward to e. Events that are unstored
// or already seen leave the copy unchanged.
func (c Cursor) Advance(e Event) Cursor {
	out := c.Clone()
	if e.Seq > out[e.Repo] {
		out[e.Repo] = e.Seq
	}
	return out
}
