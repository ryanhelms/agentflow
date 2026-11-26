package bus

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidTopic is returned by ParsePattern and ParseTopic.
var ErrInvalidTopic = errors.New("invalid topic")

// Pattern matches event types segment by segment. "*" matches exactly one
// segment. "**" matches one or more segments and is allowed only as the last
// segment; a lone "**" matches every type. Examples: "git.head.changed",
// "git.*.changed", "git.**".
type Pattern string

var segmentRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// ParsePattern validates p.
func ParsePattern(p string) (Pattern, error) {
	segs := strings.Split(p, ".")
	for i, s := range segs {
		switch {
		case s == "**" && i == len(segs)-1:
		case s == "*":
		case segmentRe.MatchString(s):
		default:
			return "", fmt.Errorf("%w: pattern %q", ErrInvalidTopic, p)
		}
	}
	return Pattern(p), nil
}

// Match reports whether t matches p. An invalid pattern matches nothing.
func (p Pattern) Match(t Type) bool {
	ps := strings.Split(string(p), ".")
	ts := strings.Split(string(t), ".")
	for i, s := range ps {
		if s == "**" && i == len(ps)-1 {
			return len(ts) > i
		}
		if i >= len(ts) || (s != "*" && s != ts[i]) {
			return false
		}
	}
	return len(ps) == len(ts)
}

// Any is the wildcard for Topic.Repo and Topic.Worktree.
const Any = "*"

// Topic selects events by repository, worktree and type: a Message Filter
// on one subscription. Its string form is "<repo>/<worktree>/<pattern>",
// where repo and worktree may be "*". A topic for one worktree also matches
// the repository-wide events (empty Worktree) of its repository, because
// they affect every worktree.
//
// One SSE connection carries many topics, so one browser stream follows
// many worktrees (topic multiplexing).
type Topic struct {
	Repo     RepoID     // a RepoID, or Any
	Worktree WorktreeID // a WorktreeID, or Any
	Types    Pattern
}

// String encodes t as "<repo>/<worktree>/<pattern>".
func (t Topic) String() string {
	return string(t.Repo) + "/" + string(t.Worktree) + "/" + string(t.Types)
}

// ParseTopic decodes the String form.
func ParseTopic(s string) (Topic, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return Topic{}, fmt.Errorf("%w: %q", ErrInvalidTopic, s)
	}
	t := Topic{Repo: RepoID(parts[0]), Worktree: WorktreeID(parts[1])}
	if (t.Repo != Any && !t.Repo.Valid()) || t.Worktree == "" || (t.Worktree != Any && !t.Worktree.Valid()) {
		return Topic{}, fmt.Errorf("%w: %q", ErrInvalidTopic, s)
	}
	p, err := ParsePattern(parts[2])
	if err != nil {
		return Topic{}, err
	}
	t.Types = p
	return t, nil
}

// Match reports whether e is selected by t. Transient events with no Repo
// (stream.overflow) match any repository.
func (t Topic) Match(e Event) bool {
	if t.Repo != Any && e.Repo != "" && e.Repo != t.Repo {
		return false
	}
	if t.Worktree != Any && e.Worktree != "" && e.Worktree != t.Worktree {
		return false
	}
	return t.Types.Match(e.Type)
}

// MatchAny reports whether any topic selects e.
func MatchAny(topics []Topic, e Event) bool {
	for _, t := range topics {
		if t.Match(e) {
			return true
		}
	}
	return false
}
