package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// Worktree states reported by Finish and Sweep.
const (
	StateFinished        = "finished"
	StateWouldFinish     = "would_finish"
	StateAlreadyFinished = "already_finished"
	StateDirty           = "dirty"
	StateUnpushed        = "unpushed"
	StateUnmerged        = "unmerged"
	StateStale           = "stale"
	StateOrphaned        = "orphaned"
	StateActive          = "active"
	// StateOtherRepo is a merged worktree of another repository, which a sweep
	// reports but leaves to a session in that repository.
	StateOtherRepo = "other_repo"
	StateDetached  = "detached"
	StateProtected = "protected"
)

// GH runs the GitHub CLI in dir and returns stdout.
type GH func(ctx context.Context, dir string, args ...string) (string, error)

// ExecGH runs the gh on PATH.
func ExecGH(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Options control Finish and Sweep.
type Options struct {
	// Offline skips git fetch and gh. Merged then means an ancestor of origin/<default>.
	Offline bool
	// Protected branches are never finished.
	Protected []string
	GH        GH
	// StaleDays reports worktrees with no commit for longer. Default 7.
	StaleDays int
	Now       func() time.Time
}

func (o Options) gh() GH {
	if o.GH != nil {
		return o.GH
	}
	return ExecGH
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Outcome describes one worktree after Finish or Sweep.
type Outcome struct {
	Repo    string   `json:"repo"`
	Branch  string   `json:"branch"`
	Path    string   `json:"path,omitempty"`
	State   string   `json:"state"`
	Detail  string   `json:"detail,omitempty"`
	Actions []string `json:"actions,omitempty"`
	Primary string   `json:"primary"`
}

// Plan is a checked finish that Apply carries out.
type Plan struct {
	Outcome
	removeWorktree bool
	deleteLocal    bool
	deleteRemote   bool
	// localTip and remoteTip are the commits the checks accepted. Apply deletes
	// a branch only while it is still at that commit.
	localTip  string
	remoteTip string
}

// RefusedError explains why a worktree cannot be finished yet.
type RefusedError struct {
	Outcome Outcome
	// merged is set when the PR or branch was merged but finish still keeps it,
	// so sweep reports why.
	merged bool
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("not finishing %s: %s (%s)", e.Outcome.Branch, e.Outcome.Detail, e.Outcome.State)
}

// pullRequest is the subset of `gh pr view --json` that Finish uses.
type pullRequest struct {
	State       string `json:"state"`
	MergedAt    string `json:"mergedAt"`
	BaseRefName string `json:"baseRefName"`
	HeadRefOid  string `json:"headRefOid"`
}

type checker struct {
	opt     Options
	primary *gitx.Repo
	name    string
	def     string
	fetched bool
}

func newChecker(ctx context.Context, dir string, opt Options) (*checker, []Entry, error) {
	entries, err := List(ctx, dir)
	if err != nil {
		return nil, nil, err
	}
	primary := &gitx.Repo{Dir: entries[0].Path}
	return &checker{opt: opt, primary: primary, name: RepoName(ctx, primary), def: primary.DefaultBranch(ctx)}, entries, nil
}

func (c *checker) fetch(ctx context.Context) {
	if c.opt.Offline || c.fetched {
		return
	}
	c.fetched = true
	_, _ = c.primary.Git(ctx, "fetch", "--quiet", "--prune", "origin")
}

func (c *checker) git(ctx context.Context, args ...string) (string, error) {
	return c.primary.Git(ctx, args...)
}

func (c *checker) hasRef(ctx context.Context, ref string) bool {
	_, err := c.git(ctx, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// PlanFinish checks that branch (or the worktree at dir when branch is empty)
// can be finished and returns the steps without running them.
func PlanFinish(ctx context.Context, dir, branch string, opt Options) (*Plan, error) {
	c, entries, err := newChecker(ctx, dir, opt)
	if err != nil {
		return nil, err
	}
	var entry *Entry
	if branch == "" {
		top, err := (&gitx.Repo{Dir: dir}).Git(ctx, "rev-parse", "--show-toplevel")
		if err != nil {
			return nil, err
		}
		top = realPath(strings.TrimSpace(top))
		for i := range entries {
			if realPath(entries[i].Path) == top {
				entry = &entries[i]
			}
		}
		if entry == nil || entry.Primary {
			return nil, errors.New("this is the primary checkout; run finish in the task's worktree or pass its branch")
		}
		if entry.Branch == "" {
			return nil, errors.New("this worktree has a detached HEAD; pass the branch to finish")
		}
		branch = entry.Branch
	} else {
		for i := range entries {
			if entries[i].Branch == branch {
				entry = &entries[i]
			}
		}
	}
	return c.plan(ctx, branch, entry)
}

func (c *checker) plan(ctx context.Context, branch string, entry *Entry) (*Plan, error) {
	p := &Plan{Outcome: Outcome{Repo: c.name, Branch: branch, Primary: c.primary.Dir}}
	refuse := func(state, detail string) (*Plan, error) {
		p.State, p.Detail = state, detail
		return p, &RefusedError{Outcome: p.Outcome}
	}
	if branch == c.def || policy.IsProtected(branch, c.opt.Protected) {
		return refuse(StateProtected, "protected branch")
	}
	if entry != nil && entry.Primary {
		return refuse(StateProtected, "checked out in the primary checkout")
	}
	if entry != nil {
		p.Path = entry.Path
		if isDir(entry.Path) {
			if out, err := (&gitx.Repo{Dir: entry.Path}).Git(ctx, "status", "--porcelain"); err != nil {
				return nil, err
			} else if strings.TrimSpace(out) != "" {
				return refuse(StateDirty, "uncommitted changes")
			}
		}
	}
	localTip := c.revParse(ctx, "refs/heads/"+branch)
	local := localTip != ""
	c.fetch(ctx)
	remoteTip := c.remoteTip(ctx, branch)
	remote := remoteTip != ""
	if entry == nil && !local && !remote {
		p.State, p.Detail = StateAlreadyFinished, "no worktree, local branch or remote branch"
		return p, nil
	}
	pr := c.pullRequest(ctx, branch)
	merged, how, into := c.merged(ctx, branch, local, pr)
	if !merged {
		p.State, p.Detail = StateUnmerged, how
		return p, &RefusedError{Outcome: p.Outcome, merged: pr != nil && pr.HeadRefOid == "" && (strings.EqualFold(pr.State, "MERGED") || pr.MergedAt != "")}
	}
	if local {
		if n := c.unpushed(ctx, branch, pr); n > 0 {
			return refuse(StateUnpushed, fmt.Sprintf("%d commit(s) not on origin", n))
		}
	}
	// Every tip that finish deletes must hold only merged work: commits pushed
	// after the merge, or from another machine, live only on the branch.
	for _, tip := range []struct{ where, sha string }{{"local branch", localTip}, {"origin/" + branch, remoteTip}} {
		if tip.sha != "" && !c.contains(ctx, into, tip.sha) {
			p.State, p.Detail = StateUnmerged, beyondDetail(tip.where, into, pr)
			return p, &RefusedError{Outcome: p.Outcome, merged: true}
		}
	}
	p.localTip, p.remoteTip = localTip, remoteTip
	p.Detail = how
	if entry != nil {
		p.removeWorktree = true
		if isDir(entry.Path) {
			p.Actions = append(p.Actions, "git worktree remove "+entry.Path)
		}
	}
	if local {
		p.deleteLocal = true
		p.Actions = append(p.Actions, "git branch -D "+branch)
	}
	p.Actions = append(p.Actions, "git worktree prune")
	if remote {
		p.deleteRemote = true
		p.Actions = append(p.Actions, "git push --force-with-lease=refs/heads/"+branch+":"+remoteTip+" origin :refs/heads/"+branch)
	}
	p.State = StateWouldFinish
	return p, nil
}

// remoteTip returns the commit of origin's branch, or "" when it has none. When
// origin cannot be read it falls back to the last fetched origin/<branch>; Apply
// then deletes the branch only if origin still has that commit.
func (c *checker) remoteTip(ctx context.Context, branch string) string {
	if !c.opt.Offline {
		ref := "refs/heads/" + branch
		if out, err := c.git(ctx, "ls-remote", "--heads", "origin", ref); err == nil {
			for _, line := range strings.Split(out, "\n") {
				if f := strings.Fields(line); len(f) == 2 && f[1] == ref {
					return f[0]
				}
			}
			return ""
		}
	}
	return c.revParse(ctx, "refs/remotes/origin/"+branch)
}

func (c *checker) revParse(ctx context.Context, ref string) string {
	out, err := c.git(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (c *checker) pullRequest(ctx context.Context, branch string) *pullRequest {
	if c.opt.Offline {
		return nil
	}
	out, err := c.opt.gh()(ctx, c.primary.Dir, "pr", "view", branch, "--json", "state,mergedAt,baseRefName,headRefOid")
	if err != nil {
		return nil
	}
	var pr pullRequest
	if json.Unmarshal([]byte(out), &pr) != nil {
		return nil
	}
	return &pr
}

// merged reports whether the branch's work is in its base: a merged PR, or a
// branch that advanced and is an ancestor of origin/<default> (or the PR base).
// into is what every branch tip must be contained in: the merged PR's head
// commit, or the base the branch was found in.
func (c *checker) merged(ctx context.Context, branch string, local bool, pr *pullRequest) (ok bool, how, into string) {
	if pr != nil && (strings.EqualFold(pr.State, "MERGED") || pr.MergedAt != "") {
		if pr.HeadRefOid == "" {
			return false, "the merged PR reports no head commit to compare the branch with", ""
		}
		base := pr.BaseRefName
		if base == "" {
			base = c.def
		}
		return true, "PR merged into " + base, pr.HeadRefOid
	}
	if !local {
		if pr != nil {
			return false, "PR is " + strings.ToLower(pr.State), ""
		}
		return false, "no merged PR found and no local branch to compare", ""
	}
	bases := []string{"origin/" + c.def}
	if pr != nil && pr.BaseRefName != "" && pr.BaseRefName != c.def {
		bases = append(bases, "origin/"+pr.BaseRefName)
	}
	for _, base := range bases {
		if !c.hasRef(ctx, base) {
			continue
		}
		if _, err := c.git(ctx, "merge-base", "--is-ancestor", "refs/heads/"+branch, base); err == nil && c.advanced(ctx, branch) {
			return true, "merged into " + base, base
		}
	}
	if pr != nil {
		return false, "PR is " + strings.ToLower(pr.State), ""
	}
	return false, "no merged PR and not merged into origin/" + c.def, ""
}

// contains reports whether tip is into or one of its ancestors. A commit that
// is not available locally is never contained.
func (c *checker) contains(ctx context.Context, into, tip string) bool {
	if tip == into {
		return true
	}
	_, err := c.git(ctx, "merge-base", "--is-ancestor", tip, into)
	return err == nil
}

func beyondDetail(where, into string, pr *pullRequest) string {
	if pr != nil && into == pr.HeadRefOid {
		return where + " has commits after the merged PR head " + short(into)
	}
	return where + " has commits that are not in " + into
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// advanced reports whether the branch ever moved past the commit it was created
// at, so a new, empty branch is never mistaken for merged work.
func (c *checker) advanced(ctx context.Context, branch string) bool {
	out, err := c.git(ctx, "reflog", "show", "--format=%gs", "refs/heads/"+branch, "--")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" && !strings.HasPrefix(line, "branch: Created") {
			return true
		}
	}
	return false
}

// unpushed counts local commits that are on no origin branch and not part of the merged PR head.
func (c *checker) unpushed(ctx context.Context, branch string, pr *pullRequest) int {
	args := []string{"rev-list", "--count", "refs/heads/" + branch, "--not", "--remotes=origin"}
	if pr != nil && pr.HeadRefOid != "" && c.hasRef(ctx, pr.HeadRefOid+"^{commit}") {
		args = append(args, pr.HeadRefOid)
	}
	out, err := c.git(ctx, args...)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// Apply runs a plan from PlanFinish. Running it again after success does nothing.
func Apply(ctx context.Context, p *Plan) (Outcome, error) {
	out := p.Outcome
	if out.State != StateWouldFinish {
		return out, nil
	}
	repo := &gitx.Repo{Dir: p.Primary}
	// Refuse before deleting anything when the branch moved since the plan: a
	// commit made in the worktree meanwhile lives only on the local branch.
	if p.deleteLocal {
		if now, err := repo.Git(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+p.Branch); err == nil && strings.TrimSpace(now) != p.localTip {
			return out, fmt.Errorf("%s moved to %s after the check; run finish again", p.Branch, short(strings.TrimSpace(now)))
		}
	}
	if p.removeWorktree && isDir(p.Path) {
		if _, err := repo.Git(ctx, "worktree", "remove", p.Path); err != nil {
			return out, err
		}
	}
	if p.deleteLocal {
		// git branch -D refuses a branch another worktree uses, also while it
		// rebases or bisects there with a detached HEAD.
		if now, err := repo.Git(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+p.Branch); err == nil {
			if strings.TrimSpace(now) != p.localTip {
				return out, fmt.Errorf("%s moved after the check; run finish again", p.Branch)
			}
			if _, err := repo.Git(ctx, "branch", "-D", p.Branch); err != nil {
				return out, err
			}
		}
	}
	if _, err := repo.Git(ctx, "worktree", "prune"); err != nil {
		return out, err
	}
	if p.deleteRemote {
		// Delete origin's branch only while it is still at the checked commit, so
		// work pushed after the check, or since the last fetch, is kept.
		ref := "refs/heads/" + p.Branch
		if _, err := repo.Git(ctx, "push", "--quiet", "--force-with-lease="+ref+":"+p.remoteTip, "origin", ":"+ref); err != nil {
			if remaining, lsErr := repo.Git(ctx, "ls-remote", "--heads", "origin", ref); lsErr != nil || strings.TrimSpace(remaining) != "" {
				return out, err
			}
		}
	}
	out.State = StateFinished
	return out, nil
}

// SweepResult lists every worktree Sweep looked at.
type SweepResult struct {
	Worktrees []Outcome `json:"worktrees"`
	Repos     []string  `json:"repos"`
	plans     []*Plan
}

// Plans returns the finishes a sweep would apply.
func (r *SweepResult) Plans() []*Plan { return r.plans }

// PlanSweep inspects the worktrees of each repository (primary checkout paths)
// and plans a finish for every merged one. It never changes anything.
func PlanSweep(ctx context.Context, repos []string, opt Options) (*SweepResult, error) {
	res := &SweepResult{Worktrees: []Outcome{}}
	seen := map[string]bool{}
	for _, dir := range repos {
		c, entries, err := newChecker(ctx, dir, opt)
		if err != nil {
			continue // not a repository any more
		}
		key := realPath(c.primary.GitDir(ctx))
		if seen[key] {
			continue
		}
		seen[key] = true
		res.Repos = append(res.Repos, c.primary.Dir)
		for i := range entries {
			e := entries[i]
			if e.Primary || e.Bare {
				continue
			}
			res.Worktrees = append(res.Worktrees, c.inspect(ctx, &e, res))
		}
	}
	return res, nil
}

func (c *checker) inspect(ctx context.Context, e *Entry, res *SweepResult) Outcome {
	o := Outcome{Repo: c.name, Branch: e.Branch, Path: e.Path, Primary: c.primary.Dir}
	switch {
	case e.Prunable || !isDir(e.Path):
		o.State, o.Detail = StateOrphaned, "directory is missing; git worktree prune clears it"
		return o
	case e.Detached || e.Branch == "":
		o.State, o.Detail = StateDetached, "detached HEAD"
		return o
	}
	p, err := c.plan(ctx, e.Branch, e)
	var refused *RefusedError
	switch {
	case err == nil:
		res.plans = append(res.plans, p)
		return p.Outcome
	case !errors.As(err, &refused):
		o.State, o.Detail = StateActive, err.Error()
		return o
	}
	o = refused.Outcome
	if o.State == StateUnmerged {
		o.State = StateActive
		if !refused.merged {
			o.Detail = ""
		}
		if n := c.unpushed(ctx, e.Branch, nil); n > 0 {
			o.State, o.Detail = StateUnpushed, fmt.Sprintf("%d commit(s) not on origin", n)
		}
	}
	if o.State == StateActive || o.State == StateUnpushed {
		if days := c.idleDays(ctx, e.Branch); days > c.staleDays() {
			detail := fmt.Sprintf("no commits for %d days", days)
			if o.State == StateUnpushed {
				detail = o.Detail + "; " + detail
			}
			o.State, o.Detail = StateStale, detail
		}
	}
	return o
}

func (c *checker) staleDays() int {
	if c.opt.StaleDays > 0 {
		return c.opt.StaleDays
	}
	return 7
}

func (c *checker) idleDays(ctx context.Context, branch string) int {
	out, err := c.git(ctx, "log", "-1", "--format=%ct", "refs/heads/"+branch, "--")
	if err != nil {
		return 0
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0
	}
	return int(c.opt.now().Sub(time.Unix(sec, 0)).Hours() / 24)
}

// KeepRepo limits a sweep to the repository whose primary checkout is primary.
// Merged worktrees of other repositories become StateOtherRepo, with the
// command to run there: a session changes only its own repository.
func (r *SweepResult) KeepRepo(primary string) {
	keep := r.plans[:0]
	for _, p := range r.plans {
		if p.Primary == primary {
			keep = append(keep, p)
			continue
		}
		for i := range r.Worktrees {
			o := &r.Worktrees[i]
			if o.Primary == p.Primary && o.Branch == p.Branch {
				o.State = StateOtherRepo
				o.Detail = "merged; another repository, so finish it from a session there (agentflow delegate --repo " + o.Repo + " \"agentflow sweep\")"
				o.Actions = nil
			}
		}
	}
	r.plans = keep
	r.Repos = slices.DeleteFunc(r.Repos, func(dir string) bool { return dir != primary })
}

// ApplySweep finishes every planned worktree, then prunes and refreshes origin/HEAD.
func ApplySweep(ctx context.Context, res *SweepResult, opt Options) error {
	var errs []error
	for _, p := range res.plans {
		out, err := Apply(ctx, p)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", p.Repo, p.Branch, err))
		}
		for i := range res.Worktrees {
			if res.Worktrees[i].Repo == p.Repo && res.Worktrees[i].Branch == p.Branch {
				res.Worktrees[i] = out
			}
		}
	}
	for _, dir := range res.Repos {
		repo := &gitx.Repo{Dir: dir}
		if _, err := repo.Git(ctx, "worktree", "prune"); err != nil {
			errs = append(errs, err)
		}
		if !opt.Offline {
			_, _ = repo.Git(ctx, "remote", "set-head", "origin", "-a")
		}
	}
	return errors.Join(errs...)
}

// Repos lists primary checkouts for --all-repos: every repository with a
// worktree under the root, every configured checkout, and ~/GitHub/<repo> for
// repository directories under the root.
func (s Settings) Repos(ctx context.Context) []string {
	var out []string
	add := func(dir string) {
		if dir == "" || slices.Contains(out, dir) {
			return
		}
		if primary, err := Primary(ctx, dir); err == nil {
			if !slices.Contains(out, primary) {
				out = append(out, primary)
			}
		}
	}
	names, _ := os.ReadDir(s.Root)
	for _, name := range names {
		if !name.IsDir() {
			continue
		}
		if dir := firstCheckout(filepath.Join(s.Root, name.Name())); dir != "" {
			add(dir)
		}
		if isDir(s.Checkout(name.Name())) {
			add(s.Checkout(name.Name()))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(s.Checkouts)) {
		if isDir(s.Checkouts[name]) {
			add(s.Checkouts[name])
		}
	}
	return out
}

func firstCheckout(root string) string {
	found := ""
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if found != "" {
			return filepath.SkipAll
		}
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			found = path
			return filepath.SkipAll
		}
		if rel, _ := filepath.Rel(root, path); strings.Count(rel, string(filepath.Separator)) >= 3 {
			return filepath.SkipDir
		}
		return nil
	})
	return found
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
