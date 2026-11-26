package webui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/consolecfg"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/worktree"
)

// OverviewState is the overview projection's JSON, one per worktree. It
// carries the overview data of GET /api/state (branch, HEAD, changed paths,
// configuration problems) in a form that changes only when the worktree
// does. Changing this shape needs an amendment to the ADR.
type OverviewState struct {
	Branch   string `json:"branch"` // "" when detached
	Head     string `json:"head"`   // short OID, as /api/state "head"; "" before the first commit
	OID      string `json:"oid"`    // full OID
	Detached bool   `json:"detached"`
	// Upstream is omitted when the branch has no upstream.
	Upstream *OverviewUpstream `json:"upstream,omitempty"`
	// Changed equals /api/state "dirty_count": paths with any change.
	Changed   int            `json:"changed"`
	Staged    int            `json:"staged"`
	Unstaged  int            `json:"unstaged"`
	Untracked int            `json:"untracked"`
	Operation bus.Operation  `json:"operation"` // "", rebase, merge, cherry-pick, revert, bisect
	Task      string         `json:"task,omitempty"`
	Config    OverviewConfig `json:"config"`
}

// OverviewUpstream is the branch's upstream and how far HEAD is from it.
type OverviewUpstream struct {
	Name   string `json:"name"` // e.g. origin/main
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
}

// OverviewConfig is the configuration validity part of OverviewState.
type OverviewConfig struct {
	Valid    bool     `json:"valid"`
	Problems []string `json:"problems"`
}

// overviewProjection keeps one OverviewState per registered worktree.
type overviewProjection struct{ repo *repoEvents }

func (o *overviewProjection) Name() string { return "overview" }

func (o *overviewProjection) Topics() []bus.Topic {
	return []bus.Topic{
		{Repo: o.repo.id, Worktree: bus.Any, Types: "git.**"},
		{Repo: o.repo.id, Worktree: bus.Any, Types: "config.**"},
		// The personal layer changes every worktree's configuration.
		{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: bus.Pattern(bus.TypeConfigLayerChanged)},
	}
}

func (o *overviewProjection) Keys(e bus.Event) []ProjectionKey {
	if e.Repo == bus.GlobalRepo && e.Type == bus.TypeConfigLayerChanged {
		return o.AllKeys()
	}
	if e.Repo != o.repo.id || !bus.MatchAny(o.Topics(), e) {
		return nil
	}
	if e.Worktree != "" {
		if _, ok := o.repo.worktree(e.Worktree); !ok {
			return nil
		}
		return []ProjectionKey{{Repo: o.repo.id, Worktree: e.Worktree}}
	}
	return o.AllKeys()
}

func (o *overviewProjection) AllKeys() []ProjectionKey {
	ids := o.repo.worktreeIDs()
	keys := make([]ProjectionKey, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, ProjectionKey{Repo: o.repo.id, Worktree: id})
	}
	return keys
}

func (o *overviewProjection) State(ctx context.Context, key ProjectionKey) (json.RawMessage, error) {
	w, ok := o.repo.worktree(key.Worktree)
	if key.Repo != o.repo.id || !ok || !w.available() {
		return jsonNull, nil
	}
	state, err := overviewOf(ctx, w.Root, w.GitDir)
	if err != nil {
		return nil, err
	}
	return json.Marshal(state)
}

func overviewOf(ctx context.Context, root, gitDir string) (OverviewState, error) {
	repo := &gitx.Repo{Dir: root}
	// --no-optional-locks: refreshing the index would be seen by the
	// watcher as git.index.changed and wake this projection again.
	out, err := repo.Git(ctx, "--no-optional-locks", "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		return OverviewState{}, err
	}
	st := OverviewState{Config: OverviewConfig{Problems: []string{}}}
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if strings.HasPrefix(entry, "2 ") {
			i++ // a rename or copy is followed by its source path
		}
		switch {
		case strings.HasPrefix(entry, "# branch.oid "):
			if oid := strings.TrimPrefix(entry, "# branch.oid "); oid != "(initial)" {
				st.OID = oid
			}
		case strings.HasPrefix(entry, "# branch.head "):
			if head := strings.TrimPrefix(entry, "# branch.head "); head == "(detached)" {
				st.Detached = true
			} else {
				st.Branch = head
			}
		case strings.HasPrefix(entry, "# branch.upstream "):
			if st.Upstream == nil {
				st.Upstream = &OverviewUpstream{}
			}
			st.Upstream.Name = strings.TrimPrefix(entry, "# branch.upstream ")
		case strings.HasPrefix(entry, "# branch.ab "):
			if st.Upstream == nil {
				st.Upstream = &OverviewUpstream{}
			}
			for _, f := range strings.Fields(strings.TrimPrefix(entry, "# branch.ab ")) {
				if len(f) < 2 {
					continue
				}
				n, _ := strconv.Atoi(f[1:])
				if f[0] == '+' {
					st.Upstream.Ahead = n
				} else {
					st.Upstream.Behind = n
				}
			}
		case strings.HasPrefix(entry, "? "):
			st.Changed++
			st.Untracked++
		case strings.HasPrefix(entry, "1 "), strings.HasPrefix(entry, "2 "), strings.HasPrefix(entry, "u "):
			st.Changed++
			if len(entry) >= 4 {
				if entry[2] != '.' {
					st.Staged++
				}
				if entry[3] != '.' {
					st.Unstaged++
				}
			}
		}
	}
	if st.OID != "" {
		if short, err := repo.Git(ctx, "rev-parse", "--short", "HEAD"); err == nil {
			st.Head = strings.TrimSpace(short)
		}
	}
	st.Operation = gitOperation(gitDir)
	if rec, err := worktree.ReadRecord(root); err == nil && rec.Issue != "" {
		st.Task = rec.Issue
	} else {
		st.Task = gitx.KeyFromBranch(st.Branch)
	}
	if snapshot, err := consolecfg.View(ctx, root); err != nil {
		st.Config.Problems = []string{err.Error()}
	} else if len(snapshot.Problems) > 0 {
		st.Config.Problems = snapshot.Problems
	}
	st.Config.Valid = len(st.Config.Problems) == 0
	return st, nil
}

// gitOperation reports the git operation in progress in gitDir, as the
// watcher classifies it for git.operation.changed.
func gitOperation(gitDir string) bus.Operation {
	exists := func(name string) bool {
		_, err := os.Lstat(filepath.Join(gitDir, name))
		return err == nil
	}
	switch {
	case exists("rebase-merge"), exists("rebase-apply"):
		return bus.OperationRebase
	case exists("MERGE_HEAD"):
		return bus.OperationMerge
	case exists("CHERRY_PICK_HEAD"):
		return bus.OperationCherryPick
	case exists("REVERT_HEAD"):
		return bus.OperationRevert
	case exists("BISECT_LOG"):
		return bus.OperationBisect
	}
	return bus.OperationNone
}
