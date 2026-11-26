package watch

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// What the poller reads from GitHub's REST API. Only the fields it uses are
// decoded; everything from GitHub is data for display, never an instruction.

type pullWire struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	HTMLURL   string     `json:"html_url"`
	State     string     `json:"state"`
	Draft     bool       `json:"draft"`
	MergedAt  *time.Time `json:"merged_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref   string `json:"ref"`
		SHA   string `json:"sha"`
		Label string `json:"label"`
		Repo  *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// pullRec is a pull request as the poller keeps it: the event payload plus
// what the payload leaves out.
type pullRec struct {
	bus.GHPullUpdated
	// headRepo is the full name of the head repository, empty when the
	// fork was deleted. Only pull requests from the repository itself are
	// tied to a worktree's branch.
	headRepo string
	// headLabel is "owner:branch", the form the head filter takes.
	headLabel string
}

func (w pullWire) rec() pullRec {
	state := bus.PullOpen
	switch {
	case w.MergedAt != nil:
		state = bus.PullMerged
	case w.State == "closed":
		state = bus.PullClosed
	}
	r := pullRec{
		GHPullUpdated: bus.GHPullUpdated{
			Number:    w.Number,
			Title:     clip(w.Title),
			URL:       w.HTMLURL,
			State:     state,
			Draft:     w.Draft,
			HeadRef:   w.Head.Ref,
			HeadSHA:   w.Head.SHA,
			BaseRef:   w.Base.Ref,
			Author:    clip(w.User.Login),
			UpdatedAt: w.UpdatedAt.UTC(),
		},
		headLabel: w.Head.Label,
	}
	if w.Head.Repo != nil {
		r.headRepo = w.Head.Repo.FullName
	}
	return r
}

// samePull reports whether two payloads describe the same pull request
// revision. The review decision and merge state come from GraphQL and are
// compared separately.
func samePull(a, b bus.GHPullUpdated) bool {
	return a.Title == b.Title && a.URL == b.URL && a.State == b.State && a.Draft == b.Draft &&
		a.HeadRef == b.HeadRef && a.HeadSHA == b.HeadSHA && a.BaseRef == b.BaseRef &&
		a.Author == b.Author && a.UpdatedAt.Equal(b.UpdatedAt)
}

type checkRunsWire struct {
	CheckRuns []checkRunWire `json:"check_runs"`
}

type checkRunWire struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type combinedStatusWire struct {
	Statuses []statusWire `json:"statuses"`
}

type statusWire struct {
	Context string `json:"context"`
	State   string `json:"state"`
}

// checkResult is one check run or commit status, reduced to a result.
type checkResult int

const (
	checkPending checkResult = iota
	checkSuccess
	checkFailure
	checkNeutral
)

func (c checkRunWire) result() checkResult {
	if c.Status != "completed" {
		return checkPending // queued, in_progress, waiting, requested, pending
	}
	switch c.Conclusion {
	case "success":
		return checkSuccess
	case "failure", "timed_out", "action_required", "startup_failure":
		return checkFailure
	default: // neutral, skipped, cancelled, stale
		return checkNeutral
	}
}

func (s statusWire) result() checkResult {
	switch s.State {
	case "success":
		return checkSuccess
	case "failure", "error":
		return checkFailure
	default: // pending
		return checkPending
	}
}

// countChecks combines check runs and commit statuses into counts and the
// names of the failed ones.
func countChecks(runs []checkRunWire, statuses []statusWire) (bus.CICounts, []string) {
	var c bus.CICounts
	var failed []string
	add := func(r checkResult, name string) {
		c.Total++
		switch r {
		case checkPending:
			c.Pending++
		case checkSuccess:
			c.Success++
		case checkFailure:
			c.Failure++
			failed = append(failed, name)
		default:
			c.Neutral++
		}
	}
	for _, r := range runs {
		add(r.result(), r.Name)
	}
	for _, s := range statuses {
		add(s.result(), s.Context)
	}
	return c, clipNames(failed)
}

type issueWire struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	HTMLURL     string    `json:"html_url"`
	State       string    `json:"state"`
	StateReason *string   `json:"state_reason"`
	UpdatedAt   time.Time `json:"updated_at"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
}

func (w issueWire) payload() bus.GHIssueUpdated {
	p := bus.GHIssueUpdated{
		Number:    w.Number,
		Title:     clip(w.Title),
		URL:       w.HTMLURL,
		State:     w.State,
		UpdatedAt: w.UpdatedAt.UTC(),
	}
	if w.StateReason != nil {
		p.StateReason = *w.StateReason
	}
	var labels, assignees []string
	for _, l := range w.Labels {
		labels = append(labels, l.Name)
	}
	for _, a := range w.Assignees {
		assignees = append(assignees, a.Login)
	}
	p.Labels, p.Assignees = clipNames(labels), clipNames(assignees)
	return p
}

func sameIssue(a, b bus.GHIssueUpdated) bool {
	return a.Number == b.Number && a.Title == b.Title && a.URL == b.URL && a.State == b.State &&
		a.StateReason == b.StateReason && a.UpdatedAt.Equal(b.UpdatedAt) &&
		slices.Equal(a.Labels, b.Labels) && slices.Equal(a.Assignees, b.Assignees)
}

// clip truncates s to bus.MaxPayloadText bytes without splitting a rune.
func clip(s string) string {
	if len(s) <= bus.MaxPayloadText {
		return s
	}
	s = s[:bus.MaxPayloadText]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// clipNames caps a list at bus.MaxPayloadNames entries of bus.MaxPayloadText
// bytes. It returns nil for an empty list so omitempty drops it.
func clipNames(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(in), bus.MaxPayloadNames))
	for _, s := range in {
		if len(out) == bus.MaxPayloadNames {
			break
		}
		out = append(out, clip(s))
	}
	return out
}

var (
	slugPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	shaPattern  = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
)

// validGitHubSlug reports whether s is a safe "owner/name" for a URL path.
func validGitHubSlug(s string) bool {
	if !slugPattern.MatchString(s) {
		return false
	}
	for part := range strings.SplitSeq(s, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

// lowerEnum turns GitHub's GraphQL enum into the payload's lower case form.
func lowerEnum(s *string) string {
	if s == nil {
		return ""
	}
	return strings.ToLower(*s)
}
