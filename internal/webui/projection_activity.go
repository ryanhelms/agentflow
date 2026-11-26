package webui

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/watch"
	"github.com/ryanhelms/agentflow/internal/worktree"
)

// activityWindow is how many of the newest stored events the activity
// projection scans for ActivityState.Recent. The scan never reads the whole
// store, so a busy repository costs the same as a quiet one.
const activityWindow = store.MaxReplay

// activityProjection keeps one ActivityState per repository. GitHub data
// comes from the poller's cache, never from GitHub itself.
type activityProjection struct {
	repo  *repoEvents
	cache func() watch.GitHubCache // nil result: no poller
}

func (a *activityProjection) Name() string { return ProjectionActivity }

func (a *activityProjection) Topics() []bus.Topic {
	return []bus.Topic{
		{Repo: a.repo.id, Worktree: bus.Any, Types: "gh.**"},
		{Repo: a.repo.id, Worktree: bus.Any, Types: bus.Pattern(bus.TypeCommitCreated)},
		{Repo: a.repo.id, Worktree: bus.Any, Types: bus.Pattern(bus.TypeHookRan)},
		{Repo: a.repo.id, Worktree: bus.Any, Types: bus.Pattern(bus.TypeGitHeadChanged)},
		{Repo: a.repo.id, Worktree: bus.Any, Types: bus.Pattern(bus.TypeGitWorktreesChanged)},
	}
}

func (a *activityProjection) Keys(e bus.Event) []ProjectionKey {
	if e.Repo != a.repo.id || !bus.MatchAny(a.Topics(), e) {
		return nil
	}
	// A passed hook never enters Recent; pre-bash would otherwise
	// recompute the state for every shell command.
	if e.Type == bus.TypeHookRan && recentHook(e) == nil {
		return nil
	}
	return a.AllKeys()
}

func (a *activityProjection) AllKeys() []ProjectionKey {
	return []ProjectionKey{{Repo: a.repo.id}}
}

func (a *activityProjection) State(ctx context.Context, key ProjectionKey) (json.RawMessage, error) {
	if key.Repo != a.repo.id || key.Worktree != "" {
		return jsonNull, nil
	}
	var gh watch.GitHubState
	known := false
	if a.cache != nil {
		if c := a.cache(); c != nil {
			gh, known = c.GitHubState(a.repo.id)
		}
	}
	recent, err := a.recent(ctx)
	if err != nil {
		return nil, err
	}
	return json.Marshal(activityOf(gh, known, a.worktreeBranches(ctx), recent))
}

// worktreeLinks are what a served worktree contributes to the activity
// state: its branch and its bound GitHub issue.
type worktreeLinks struct {
	branches map[string]bus.WorktreeID
	issues   map[int]bus.WorktreeID
}

func (a *activityProjection) worktreeBranches(ctx context.Context) worktreeLinks {
	links := worktreeLinks{branches: map[string]bus.WorktreeID{}, issues: map[int]bus.WorktreeID{}}
	for _, id := range a.repo.worktreeIDs() {
		w, ok := a.repo.worktree(id)
		if !ok || !w.available() {
			continue
		}
		if branch := (&gitx.Repo{Dir: w.Root}).Branch(ctx); branch != "" {
			if _, taken := links.branches[branch]; !taken {
				links.branches[branch] = id
			}
		}
		if rec, err := worktree.ReadRecord(w.Root); err == nil {
			if n := issueNumber(rec.Issue); n > 0 {
				if _, taken := links.issues[n]; !taken {
					links.issues[n] = id
				}
			}
		}
	}
	return links
}

// issueNumber is the number of a bound GitHub issue: "owner/name#12",
// "#12" or "12". Anything else is 0.
func issueNumber(issue string) int {
	if i := strings.LastIndexByte(issue, '#'); i >= 0 {
		issue = issue[i+1:]
	}
	n, err := strconv.Atoi(issue)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// recent scans the newest activityWindow stored events of the repository.
func (a *activityProjection) recent(ctx context.Context) ([]ActivityEntry, error) {
	s := a.repo.store
	if s == nil {
		return []ActivityEntry{}, nil
	}
	head, err := s.Head(ctx)
	if err != nil {
		return nil, err
	}
	from := bus.Cursor{}
	if newest := head[a.repo.id]; newest > activityWindow {
		from[a.repo.id] = newest - activityWindow
	}
	out := []ActivityEntry{}
	read := func(from bus.Cursor) error {
		for e, err := range s.ReadFrom(ctx, from) {
			if err != nil {
				return err
			}
			if entry, ok := activityEntry(e); ok {
				out = append(out, entry)
			}
		}
		return nil
	}
	err = read(from)
	if errors.Is(err, store.ErrCursorExpired) {
		// Fewer events are retained than the window: read them all.
		out = out[:0]
		err = read(bus.Cursor{})
	}
	if err != nil {
		return nil, err
	}
	// Stored order is oldest first; Recent is newest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > MaxActivityRecent {
		out = out[:MaxActivityRecent]
	}
	return out, nil
}

// recentHook is e's payload when it is a hook.ran that belongs in Recent.
func recentHook(e bus.Event) *bus.HookRan {
	var h bus.HookRan
	if json.Unmarshal(e.Payload, &h) != nil || (h.Outcome != bus.HookBlocked && h.Outcome != bus.HookFailed) {
		return nil
	}
	return &h
}

func activityEntry(e bus.Event) (ActivityEntry, bool) {
	entry := ActivityEntry{EventID: e.ID, Type: e.Type, Time: e.Time, Worktree: e.Worktree}
	switch e.Type {
	case bus.TypeCommitCreated:
		var c bus.CommitCreated
		if json.Unmarshal(e.Payload, &c) != nil {
			return ActivityEntry{}, false
		}
		entry.Commit = &c
	case bus.TypeHookRan:
		if entry.Hook = recentHook(e); entry.Hook == nil {
			return ActivityEntry{}, false
		}
	default:
		return ActivityEntry{}, false
	}
	return entry, true
}

// activityOf builds the state from the poller's view. known is false while
// the poller has no state for the repository (or no poller runs).
func activityOf(gh watch.GitHubState, known bool, links worktreeLinks, recent []ActivityEntry) ActivityState {
	st := ActivityState{Pulls: []ActivityPull{}, Issues: []ActivityIssue{}, Recent: recent}
	if st.Recent == nil {
		st.Recent = []ActivityEntry{}
	}
	if !known {
		st.GitHub.State = watch.GitHubUnavailable
	} else {
		st.GitHub.State = gh.Health
		if !gh.RetryAt.IsZero() {
			at := gh.RetryAt.UTC()
			st.GitHub.RetryAt = &at
		}
	}
	heads := map[string]int{}
	for _, p := range gh.Pulls {
		if p.State == bus.PullOpen {
			if _, seen := heads[p.HeadRef]; !seen {
				heads[p.HeadRef] = p.Number
			}
		}
	}
	for _, p := range gh.Pulls {
		if p.State != bus.PullOpen {
			continue
		}
		pull := ActivityPull{GHPullUpdated: p, Worktree: links.branches[p.HeadRef]}
		if ci, ok := gh.CI[p.HeadSHA]; ok {
			pull.CI, pull.Failed = ci.State, ci.Failed
		}
		if base, ok := heads[p.BaseRef]; ok && base != p.Number {
			pull.DependsOn = base
		}
		st.Pulls = append(st.Pulls, pull)
	}
	sort.SliceStable(st.Pulls, func(i, j int) bool {
		ri, rj := readyToMerge(st.Pulls[i].GHPullUpdated), readyToMerge(st.Pulls[j].GHPullUpdated)
		if ri != rj {
			return ri
		}
		return st.Pulls[i].UpdatedAt.After(st.Pulls[j].UpdatedAt)
	})
	for n, issue := range gh.Issues {
		if issue.Number == 0 {
			issue.Number = n
		}
		st.Issues = append(st.Issues, ActivityIssue{GHIssueUpdated: issue, Worktree: links.issues[issue.Number]})
	}
	sort.Slice(st.Issues, func(i, j int) bool { return st.Issues[i].Number < st.Issues[j].Number })
	prs := make([]pull, 0, len(st.Pulls))
	for _, p := range st.Pulls {
		prs = append(prs, pull{Title: p.Title, URL: p.URL, HeadRefName: p.HeadRef, BaseRefName: p.BaseRef,
			ReviewDecision: strings.ToUpper(p.ReviewDecision), MergeStateStatus: strings.ToUpper(p.MergeState), Number: p.Number, IsDraft: p.Draft})
	}
	st.Handoff = handoffNote(pullItems(prs))
	return st
}

func readyToMerge(p bus.GHPullUpdated) bool {
	return strings.EqualFold(p.MergeState, "clean") && !p.Draft
}
