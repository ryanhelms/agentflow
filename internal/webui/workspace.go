package webui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/consolecfg"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/setup"
	"github.com/nscaledev/devflow/internal/version"
)

type item struct {
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	URL     string `json:"url,omitempty"`
	Run     string `json:"run_id,omitempty"`
	Request string `json:"request_id,omitempty"`
}

func (s *server) state(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	snapshot, err := consolecfg.View(ctx, s.repo.Dir)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	branch := s.repo.Branch(ctx)
	head, _ := s.repo.Git(ctx, "rev-parse", "--short", "HEAD")
	head = strings.TrimSpace(head)
	changes, err := s.repo.Changes(ctx)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	attention := []item{}
	for _, issue := range snapshot.Problems {
		attention = append(attention, item{Title: "Configuration needs attention", Detail: issue})
	}
	if len(changes) > 0 {
		attention = append(attention, item{Title: "Uncommitted work", Detail: fmt.Sprintf("%d changed paths in this worktree. Publication uses only the reviewed configuration file.", len(changes))})
	}
	pin := ""
	dir, err := os.OpenRoot(s.repo.Dir)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	defer dir.Close()
	const pinLimit = 512 << 10
	if st, e := dir.Lstat(setup.CallerWorkflow); e == nil && st.Mode().IsRegular() && st.Size() <= pinLimit {
		if f, e := dir.Open(setup.CallerWorkflow); e == nil {
			defer f.Close()
			if raw, e := io.ReadAll(io.LimitReader(f, pinLimit+1)); e == nil && len(raw) <= pinLimit {
				pin = setup.PinnedVersion(raw)
			}
		}
	}
	if pin != "" && version.Release() && setup.VersionLess(pin, version.Version) {
		attention = append(attention, item{Title: "Workflow pin behind installed release", Detail: pin + " is pinned. Use devflow pin to review the caller update."})
	}
	var note strings.Builder
	fmt.Fprintf(&note, "%s\nWorktree: %s\nBranch: %s\nCommit: %s\nChanged paths: %d\n", filepath.Base(s.repo.Dir), s.repo.Dir, branch, head, len(changes))
	if snapshot.Providers != nil {
		for _, role := range snapshot.Providers.Roles {
			fmt.Fprintf(&note, "%s: %s\n", role.Role, role.Selected)
		}
	}
	for _, c := range changes {
		fmt.Fprintf(&note, "%s %s\n", c.Status, c.Path)
	}
	reply(w, 200, map[string]any{"root": s.repo.Dir, "repository": filepath.Base(s.repo.Dir), "branch": branch, "head": head, "version": version.Version, "dirty": len(changes) > 0, "dirty_count": len(changes), "pin": pin, "config": snapshot, "attention": attention, "handoff": note.String(), "completions": s.checkoutCompletions(ctx)})
}

// Offer locally known names, including when the workstation is offline.
func (s *server) checkoutCompletions(ctx context.Context) map[string][]string {
	branches, _ := s.repo.Git(ctx, "for-each-ref", "--sort=-committerdate", "--count=100", "--format=%(refname:lstrip=2)", "refs/heads")
	remotes, _ := s.repo.Git(ctx, "remote")
	hints := map[string][]string{"branches": strings.Fields(branches), "remotes": strings.Fields(remotes)}
	if repo := s.repo.RemoteRepo(ctx); gitx.ValidateGitHubSlug(repo) == nil {
		hints["repositories"] = []string{repo}
	}
	return hints
}

type pull struct {
	Title, URL, HeadRefName, BaseRefName, ReviewDecision, MergeStateStatus string
	Number                                                                 int
	IsDraft                                                                bool
}

func (s *server) pullQueue(ctx context.Context) ([]item, error) {
	snapshot, err := consolecfg.View(ctx, s.repo.Dir)
	if err != nil {
		return nil, err
	}
	cfg := snapshot.Repository()
	if cfg == nil {
		return nil, errors.New("resolve the repository settings to inspect GitHub activity")
	}
	a := &app.App{Repo: s.repo, Cfg: cfg}
	repo, err := a.GitHubRepo(ctx)
	if err != nil {
		return nil, err
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || strings.Contains(name, "/") {
		return nil, errors.New("unknown GitHub repository")
	}
	client, err := a.GitHub(ctx)
	if err != nil {
		return nil, err
	}
	var data struct {
		Repository struct{ PullRequests struct{ Nodes []pull } }
	}
	const query = `query($owner:String!,$name:String!){repository(owner:$owner,name:$name){pullRequests(states:OPEN,first:50,orderBy:{field:UPDATED_AT,direction:DESC}){nodes{number title url headRefName baseRefName isDraft reviewDecision mergeStateStatus}}}}`
	if err := client.GraphQL(ctx, query, map[string]any{"owner": owner, "name": name}, &data); err != nil {
		return nil, errors.New("verify GitHub repository access and sign-in")
	}
	prs := data.Repository.PullRequests.Nodes
	sort.SliceStable(prs, func(i, j int) bool {
		return prs[i].MergeStateStatus == "CLEAN" && !prs[i].IsDraft && (prs[j].MergeStateStatus != "CLEAN" || prs[j].IsDraft)
	})
	out := make([]item, 0, len(prs))
	for _, pr := range prs {
		status := strings.ReplaceAll(strings.ToLower(pr.MergeStateStatus), "_", " ")
		if pr.IsDraft {
			status = "draft"
		}
		detail := fmt.Sprintf("%s into %s · %s", pr.HeadRefName, pr.BaseRefName, status)
		if pr.ReviewDecision != "" {
			detail += " · " + strings.ReplaceAll(strings.ToLower(pr.ReviewDecision), "_", " ")
		}
		for _, base := range prs {
			if pr.BaseRefName == base.HeadRefName {
				detail += fmt.Sprintf(" · depends on #%d", base.Number)
				break
			}
		}
		out = append(out, item{Title: fmt.Sprintf("#%d %s", pr.Number, pr.Title), Detail: detail, URL: pr.URL})
	}
	return out, nil
}

func (s *server) activity(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	// Source failures are independent; a missing GitHub connection should not hide a worker question.
	items, available, err := commanderActivity(ctx, s.repo.Dir)
	if err != nil {
		items = append(items, item{Title: "Agent Commander unavailable", Detail: err.Error()})
	}
	prs, err := s.pullQueue(ctx)
	if err != nil {
		items = append(items, item{Title: "GitHub activity unavailable", Detail: err.Error()})
	} else {
		items = append(items, prs...)
	}
	var note strings.Builder
	for _, it := range items {
		fmt.Fprintf(&note, "%s — %s %s\n", it.Title, it.Detail, it.URL)
	}
	reply(w, 200, map[string]any{"attention": items, "handoff": note.String(), "commander_connected": available})
}
