package features

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/gh"
)

// The reply feature (ADR "Phase 8 features"): replies to the open review
// threads of the branch's PR. The model names a thread only by the number
// agentflow gave it; the thread ID is the gathered one.

func init() { ai.Register(replyFeature{}) }

type replyFeature struct{}

func (replyFeature) ID() ai.FeatureID { return ai.FeatureReply }

type replyAnswer struct {
	Replies []replyItem `json:"replies"`
}

type replyItem struct {
	Thread int    `json:"thread"`
	Body   string `json:"body" draft:"max=8192"`
}

type replyFixed struct {
	Thread string `json:"thread,omitempty"`
}

// replyInput is the agentflow_review_reply input of a candidate.
type replyInput struct {
	ThreadID string `json:"thread_id"`
	Body     string `json:"body"`
}

// PartCap states the caps of the feature table (ai.Caps): 4 KiB a thread,
// 8 KiB of a file's changes.
func (replyFeature) PartCap(p ai.Part) (total, perFile int) {
	switch p.Kind {
	case ai.PartThread:
		return 4 << 10, 0
	case ai.PartDiff:
		return 8 << 10, 8 << 10
	}
	return 0, 0
}

func (replyFeature) Gather(ctx context.Context, a *app.App, r ai.Request) (ai.Context, error) {
	raw, err := a.ReplyDraftContext(ctx, r.Thread, ai.MaxThreads)
	if err != nil {
		return ai.Context{}, err
	}
	c := ai.Context{Feature: ai.FeatureReply}
	onPath := map[string][]string{}
	for i, t := range raw.Threads {
		n := i + 1
		c.Targets = append(c.Targets, ai.Target{Index: n, Kind: "thread", ID: t.ID, Label: replyLabel(t)})
		c.Parts = append(c.Parts, ai.Part{Kind: ai.PartThread, Label: fmt.Sprintf("review thread %d", n), Source: "github", Text: replyThreadText(n, t)})
		onPath[t.Path] = append(onPath[t.Path], strconv.Itoa(n))
	}
	// The branch's changes to the threads' files, picked from one diff of
	// the whole branch: a reviewer's path never reaches git. A wide
	// diff.context would send whole files, so the person's settings do not
	// decide it.
	if raw.Base != "" {
		diff, err := ai.GitDiff(ctx, a, "--no-renames", "--submodule=short", "--unified=3", "--inter-hunk-context=0", raw.Base, "HEAD", "--")
		if err != nil {
			return ai.Context{}, err
		}
		sections := replySections(diff)
		for _, t := range raw.Threads {
			if text, ok := sections[t.Path]; ok {
				c.Parts = append(c.Parts, ai.Part{Kind: ai.PartDiff, Label: "branch changes to the file of thread " + strings.Join(onPath[t.Path], ", "), Source: "git", Text: text})
				delete(sections, t.Path) // one part per file
			}
		}
	}
	if commits := strings.TrimSpace(raw.Commits); commits != "" {
		c.Parts = append(c.Parts, ai.Part{Kind: ai.PartCommits, Label: "commits on the branch", Source: "git", Text: commits})
	}
	c.Fixed, err = json.Marshal(replyFixed{Thread: r.Thread})
	return c, err
}

// replySections splits a git diff into the section of each file, by path.
func replySections(diff string) map[string]string {
	out := map[string]string{}
	for _, s := range strings.Split("\n"+diff, "\ndiff --git ")[1:] {
		header, _, _ := strings.Cut(s, "\n")
		if path, ok := replyDiffPath(header); ok {
			out[path] = "diff --git " + strings.TrimSuffix(s, "\n") + "\n"
		}
	}
	return out
}

// replyDiffPath is the file a "diff --git" header names: "a/P b/P", each
// name C-quoted when git quotes it (a non-ASCII path, by default). Renames
// are off, so both names are the same file.
func replyDiffPath(header string) (string, bool) {
	if strings.HasPrefix(header, `"`) {
		first, err := strconv.QuotedPrefix(header)
		if err != nil {
			return "", false
		}
		from, errFrom := strconv.Unquote(first)
		to, errTo := strconv.Unquote(strings.TrimPrefix(header[len(first):], " "))
		path, ok := strings.CutPrefix(from, "a/")
		return path, ok && errFrom == nil && errTo == nil && to == "b/"+path
	}
	// "a/P b/P" is twice the path and five bytes, spaces in P or not.
	n := (len(header) - 5) / 2
	if n < 1 {
		return "", false
	}
	path := header[2 : 2+n]
	return path, header == "a/"+path+" b/"+path
}

// replyLabel names a thread for the person: "src/a.go:12, alice".
func replyLabel(t gh.Thread) string {
	where := t.Path
	if t.Line > 0 {
		where += ":" + strconv.Itoa(t.Line)
	}
	return aisession.SanitizeLine(where+", "+t.Author, 256)
}

// replyThreadText is one thread as the prompt shows it.
func replyThreadText(n int, t gh.Thread) string {
	var b strings.Builder
	fmt.Fprintf(&b, "thread: %d\npath: %s\n", n, aisession.SanitizeLine(t.Path, 1024))
	if t.Line > 0 {
		fmt.Fprintf(&b, "line: %d\n", t.Line)
	}
	outdated := "no"
	if t.Outdated {
		outdated = "yes"
	}
	fmt.Fprintf(&b, "author: %s\noutdated: %s\ncomment:\n%s", aisession.SanitizeLine(t.Author, 64), outdated, strings.TrimSpace(tasksNewlines(t.Body)))
	return b.String()
}

func (replyFeature) Task() string {
	return `Draft replies to the numbered review threads of a pull request. Each thread block gives its number, file, author and first comment; diff blocks show the branch's changes to those files, and the commits block the branch's commits.
Answer {"replies": [{"thread": 1, "body": "..."}]}: at most one reply per thread, by its number. Leave out a thread you cannot answer.
A reply is short Markdown to the reviewer. Say that something was changed only when the diffs or commits show it; otherwise say what is open, or ask. Mention nobody with @ and add no links.`
}

func (replyFeature) Answer() any { return &replyAnswer{} }

func (replyFeature) Example() string {
	return `{"replies": [{"thread": 1, "body": "Thanks for pointing this out."}]}`
}

func (replyFeature) Candidates(c ai.Context, answer any) ([]ai.Candidate, []ai.Issue) {
	var out []ai.Candidate
	var issues []ai.Issue
	seen := map[int]bool{}
	for _, r := range answer.(*replyAnswer).Replies {
		switch {
		case r.Thread < 1 || r.Thread > len(c.Targets):
			// Issues hold agentflow's words and numbers, never the model's.
			issues = append(issues, ai.Issue{Code: "unknown_target", Field: "thread", Message: "The answer replies to a thread number agentflow did not give."})
			continue
		case seen[r.Thread]:
			issues = append(issues, ai.Issue{Code: "duplicate_target", Field: "thread", Message: fmt.Sprintf("The answer replies to thread %d twice; only the first reply is offered.", r.Thread)})
			continue
		}
		seen[r.Thread] = true
		target := c.Targets[r.Thread-1]
		body := strings.TrimSpace(r.Body)
		in, _ := json.Marshal(replyInput{ThreadID: target.ID, Body: body})
		// "--" first: the command line reads flags anywhere, and a drafted
		// body may start with "-".
		cand := ai.Candidate{Operation: "review.reply", Tool: "agentflow_review_reply", Input: in,
			CLI:    []string{"agentflow", "review", "reply", "--", target.ID, body},
			Target: &target, Fields: []ai.Field{{Name: "reply", Label: "Reply", Value: body}}}
		if body == "" {
			cand.Issues = []ai.Issue{{Code: "missing_field", Field: "reply", Message: "The reply is empty."}}
		}
		out = append(out, cand)
	}
	if len(out) == 0 && len(issues) == 0 {
		issues = append(issues, ai.Issue{Code: "no_reply", Message: "The answer replies to no thread."})
	}
	return out, issues
}
