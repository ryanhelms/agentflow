package bus

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	c := Cursor{"ffffffffffffffff": 7, "0123456789abcdef": 42, GlobalRepo: 3, "aaaaaaaaaaaaaaaa": 0}
	s := c.String()
	if want := "v1:0123456789abcdef=42,ffffffffffffffff=7,global=3"; s != want {
		t.Fatalf("String() = %q, want %q", s, want)
	}
	got, err := ParseCursor(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != s || len(got) != 3 {
		t.Fatalf("round trip = %v", got)
	}
	if (Cursor{}).String() != "" {
		t.Fatal("empty cursor must encode as empty string")
	}
	if empty, err := ParseCursor(""); err != nil || len(empty) != 0 {
		t.Fatalf("ParseCursor(\"\") = %v, %v", empty, err)
	}
}

func TestParseCursorRejects(t *testing.T) {
	for _, s := range []string{
		"v1:", "v2:0123456789abcdef=1", "0123456789abcdef=1",
		"v1:0123456789abcdef=0", "v1:0123456789abcdef=01", "v1:0123456789ABCDEF=1",
		"v1:ffffffffffffffff=1,0123456789abcdef=2", // unsorted
		"v1:0123456789abcdef=1,0123456789abcdef=2", // duplicate
		"v1:0123456789abcdef=-1", "v1:0123456789abcdef", "v1:0123456789abcdef=1\n",
	} {
		if _, err := ParseCursor(s); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("ParseCursor(%q) err = %v", s, err)
		}
	}
}

func TestCursorSeenAndAdvance(t *testing.T) {
	r := RepoID("0123456789abcdef")
	c := Cursor{r: 5}
	if !c.Seen(Event{Repo: r, Seq: 5}) || c.Seen(Event{Repo: r, Seq: 6}) || c.Seen(Event{Repo: r}) {
		t.Fatal("Seen wrong")
	}
	next := c.Advance(Event{Repo: r, Seq: 9})
	if c[r] != 5 || next[r] != 9 {
		t.Fatalf("Advance mutated receiver or failed: %v %v", c, next)
	}
	if c.Advance(Event{Repo: r, Seq: 2})[r] != 5 {
		t.Fatal("Advance moved backwards")
	}
}

func TestPatternMatch(t *testing.T) {
	cases := []struct {
		p    Pattern
		t    Type
		want bool
	}{
		{"git.head.changed", "git.head.changed", true},
		{"git.head.changed", "git.head", false},
		{"git.*.changed", "git.refs.changed", true},
		{"git.*", "git.head.changed", false},
		{"git.**", "git.head.changed", true},
		{"git.**", "git", false},
		{"**", "stream.overflow", true},
		{"config.**", "git.head.changed", false},
	}
	for _, c := range cases {
		if got := c.p.Match(c.t); got != c.want {
			t.Errorf("%q.Match(%q) = %v", c.p, c.t, got)
		}
	}
	for _, bad := range []string{"", "git.", "git.**.changed", "Git.head", "git/head"} {
		if _, err := ParsePattern(bad); err == nil {
			t.Errorf("ParsePattern(%q) accepted", bad)
		}
	}
}

func TestTopic(t *testing.T) {
	topic, err := ParseTopic("0123456789abcdef/main/git.**")
	if err != nil {
		t.Fatal(err)
	}
	if topic.String() != "0123456789abcdef/main/git.**" {
		t.Fatalf("String() = %q", topic)
	}
	repo := RepoID("0123456789abcdef")
	if !topic.Match(Event{Type: TypeGitHeadChanged, Repo: repo, Worktree: "main"}) {
		t.Fatal("worktree event not matched")
	}
	if !topic.Match(Event{Type: TypeGitRefsChanged, Repo: repo}) {
		t.Fatal("repository-wide event not matched by worktree topic")
	}
	if topic.Match(Event{Type: TypeGitHeadChanged, Repo: repo, Worktree: "other-1a2b3c4d"}) {
		t.Fatal("other worktree matched")
	}
	if topic.Match(Event{Type: TypeGitHeadChanged, Repo: "ffffffffffffffff", Worktree: "main"}) {
		t.Fatal("other repo matched")
	}
	all, _ := ParseTopic("*/*/**")
	if !all.Match(Event{Type: TypeStreamOverflow}) {
		t.Fatal("wildcard topic missed transient event")
	}
	for _, bad := range []string{"*/*", "x/main/git.**", "*//git.**", "*/Main/git.**", "*/*/git."} {
		if _, err := ParseTopic(bad); err == nil {
			t.Errorf("ParseTopic(%q) accepted", bad)
		}
	}
}

func TestNewID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	t0 := time.UnixMilli(1_800_000_000_000)
	a, b := NewID(t0), NewID(t0.Add(time.Millisecond))
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Fatalf("not UUIDv7: %s %s", a, b)
	}
	if a >= b {
		t.Fatalf("IDs do not sort by time: %s >= %s", a, b)
	}
	if c, d := NewID(t0), NewID(t0); c == d {
		t.Fatal("IDs repeat")
	}
}

func valid() Event {
	return Event{Type: TypeGitHeadChanged, Source: "watch.git", Repo: "0123456789abcdef", Worktree: "main",
		Payload: json.RawMessage(`{"ref":"refs/heads/main"}`)}.Normalize(time.Now())
}

func TestValidate(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	mutate := map[string]func(*Event){
		"unknown type":       func(e *Event) { e.Type = "op.applied" },
		"no source":          func(e *Event) { e.Source = "" },
		"bad repo":           func(e *Event) { e.Repo = "nope" },
		"missing worktree":   func(e *Event) { e.Worktree = "" },
		"repo-wide worktree": func(e *Event) { e.Type = TypeGitRefsChanged },
		"array payload":      func(e *Event) { e.Payload = json.RawMessage(`[]`) },
		"bad payload":        func(e *Event) { e.Payload = json.RawMessage(`{`) },
		"large payload": func(e *Event) {
			e.Payload = json.RawMessage(`{"x":"` + strings.Repeat("a", MaxPayloadBytes) + `"}`)
		},
	}
	for name, m := range mutate {
		e := valid()
		m(&e)
		if err := e.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	overflow := Event{Type: TypeStreamOverflow, Source: "bus"}.Normalize(time.Now())
	if err := overflow.Validate(); err != nil {
		t.Fatalf("transient event without repo rejected: %v", err)
	}
}

func TestEventJSONShape(t *testing.T) {
	e := Event{ID: "0190d6a2-0000-7000-8000-000000000000", Type: TypeGitRefsChanged, Source: "watch.git",
		Repo: "0123456789abcdef", Seq: 3, Time: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC),
		SchemaVersion: 1, Payload: json.RawMessage(`{"refs":[]}`)}
	b, _ := json.Marshal(e)
	want := `{"id":"0190d6a2-0000-7000-8000-000000000000","type":"git.refs.changed","source":"watch.git","repo":"0123456789abcdef","seq":3,"time":"2026-11-26T10:00:00Z","schema_version":1,"payload":{"refs":[]}}`
	if string(b) != want {
		t.Fatalf("JSON =\n%s\nwant\n%s", b, want)
	}
}

type fakeSub struct{ ch chan Event }

func (f fakeSub) Events() <-chan Event { return f.ch }
func (fakeSub) Dropped() uint64        { return 0 }
func (fakeSub) Close() error           { return nil }

type letters struct{ got []Letter }

func (l *letters) Dead(_ context.Context, x Letter) { l.got = append(l.got, x) }
func (l *letters) Letters() []Letter                { return l.got }

func TestHandleSendsFailuresToDeadLetter(t *testing.T) {
	ch := make(chan Event, 3)
	ch <- Event{ID: "a"}
	ch <- Event{ID: "b"}
	ch <- Event{ID: "c"}
	close(ch)
	dl := &letters{}
	var seen []string
	err := Handle(context.Background(), fakeSub{ch}, "test", func(_ context.Context, e Event) error {
		seen = append(seen, e.ID)
		switch e.ID {
		case "a":
			return errors.New("boom")
		case "b":
			panic("bad")
		}
		return nil
	}, dl)
	if err != nil || strings.Join(seen, "") != "abc" {
		t.Fatalf("Handle = %v, seen %v", err, seen)
	}
	if len(dl.got) != 2 || dl.got[0].Reason != "boom" || !strings.Contains(dl.got[1].Reason, "panicked") || dl.got[1].Consumer != "test" {
		t.Fatalf("letters = %+v", dl.got)
	}
}
