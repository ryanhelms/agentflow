package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/worktree"
)

// WorktreeSettings resolves worktree.root, worktree.go_work and worktree.checkouts.
func (a *App) WorktreeSettings() worktree.Settings {
	return worktree.Resolve(a.Cfg, a.User)
}

// taskWorktreePath is <root>/<repo>/<branch> for every task provider.
func (a *App) taskWorktreePath(ctx context.Context, branch string) string {
	return a.WorktreeSettings().Path(worktree.RepoName(ctx, a.Repo), branch)
}

// prepareWorktree records the task in a new worktree and writes its go.work.
func (a *App) prepareWorktree(ctx context.Context, res *StartResult, taskRef, base string) error {
	rec := worktree.Record{
		Issue: res.Key, TaskRef: taskRef, Branch: res.Branch, Base: base,
		Repo: worktree.RepoName(ctx, a.Repo), Created: time.Now().UTC().Truncate(time.Second),
	}
	if sha, err := a.Repo.Git(ctx, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err == nil {
		rec.BaseSHA = strings.TrimSpace(sha)
	}
	if err := worktree.WriteRecord(ctx, res.Worktree, rec); err != nil {
		return fmt.Errorf("created worktree %s but could not record it: %w", res.Worktree, err)
	}
	gw, err := worktree.WriteGoWork(ctx, res.Worktree, a.WorktreeSettings(), rec)
	if err != nil {
		return fmt.Errorf("created worktree %s but could not write go.work: %w", res.Worktree, err)
	}
	res.GoWork = gw
	res.Next = worktreeNext(res.Worktree)
	return nil
}

func worktreeNext(dir string) string {
	return "Start your agent in the worktree: cd " + shellQuote(dir)
}

// WorktreeGoWork regenerates go.work in the current worktree.
func (a *App) WorktreeGoWork(ctx context.Context) (any, error) {
	s := a.WorktreeSettings()
	if s.GoWork == nil || len(s.GoWork.Use) == 0 {
		return nil, errors.New("worktree.go_work is not configured in .agentflow/config.yml")
	}
	rec, err := worktree.ReadRecord(a.Repo.Dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		rec = worktree.Record{Branch: a.Repo.Branch(ctx), Repo: worktree.RepoName(ctx, a.Repo)}
	}
	return worktree.WriteGoWork(ctx, a.Repo.Dir, s, rec)
}

func (a *App) worktreeOptions() worktree.Options {
	return worktree.Options{Protected: a.Cfg.Policy.ProtectedBranches}
}

// FinishResult is returned by Finish.
type FinishResult struct {
	worktree.Outcome
	Next string `json:"next,omitempty"`
}

// Finish removes a merged task's worktree, its local branch and its remote
// branch. It refuses while the worktree has uncommitted or unpushed work, or the
// PR is not merged. Finishing an already finished branch succeeds and does nothing.
func (a *App) Finish(ctx context.Context, branch string, dryRun bool, token string) (any, error) {
	return a.finish(ctx, branch, dryRun, token, a.worktreeOptions())
}

func (a *App) finish(ctx context.Context, branch string, dryRun bool, token string, opt worktree.Options) (any, error) {
	plan, err := worktree.PlanFinish(ctx, a.Repo.Dir, branch, opt)
	if err != nil {
		return nil, err
	}
	res := FinishResult{Outcome: plan.Outcome, Next: "cd " + shellQuote(plan.Primary)}
	if dryRun || plan.State != worktree.StateWouldFinish {
		return res, nil
	}
	if p, err := a.gate(policy.Remote, "finish", plan.Outcome, token, false, plan.Actions); p != nil || err != nil {
		return p, err
	}
	res.Outcome, err = worktree.Apply(ctx, plan)
	a.audit(policy.Remote, "finish", plan.Outcome, err)
	return res, err
}

// SweepRequest selects what Sweep inspects.
type SweepRequest struct {
	AllRepos  bool
	DryRun    bool
	StaleDays int
	// Offline skips fetch and gh; the session-start summary uses it.
	Offline bool
}

// Sweep finishes every merged worktree and reports dirty, unpushed, stale and
// orphaned ones without deleting them.
func (a *App) Sweep(ctx context.Context, req SweepRequest, token string) (*worktree.SweepResult, *Pending, error) {
	opt := a.worktreeOptions()
	opt.StaleDays, opt.Offline = req.StaleDays, req.Offline
	repos := []string{a.Repo.Dir}
	if req.AllRepos {
		repos = append(repos, a.WorktreeSettings().Repos(ctx)...)
	}
	res, err := worktree.PlanSweep(ctx, repos, opt)
	if err != nil || req.DryRun {
		return res, nil, err
	}
	// --all-repos reports every repository but finishes only this one's worktrees.
	if req.AllRepos && len(res.Repos) > 0 {
		res.KeepRepo(res.Repos[0])
	}
	if plans := res.Plans(); len(plans) > 0 {
		summary := make([]worktree.Outcome, 0, len(plans))
		var commands []string
		for _, p := range plans {
			summary = append(summary, p.Outcome)
			commands = append(commands, p.Actions...)
		}
		if p, err := a.gate(policy.Remote, "sweep", summary, token, false, commands); p != nil || err != nil {
			return res, p, err
		}
	}
	err = worktree.ApplySweep(ctx, res, opt)
	a.audit(policy.Remote, "sweep", res.Repos, err)
	return res, nil, err
}

// SessionEnv is set for every process below an AI session's harness: its
// value is the session ID (ADR, "Phase 7 session environment"). It is one
// layer, not a boundary: a harness can unset it.
const SessionEnv = "AGENTFLOW_ACP_SESSION"

// AgentEnvironment names the environment variable that shows an AI agent is
// running this process, or "" for a person. A process inside an AI session
// (SessionEnv) counts as an agent.
func AgentEnvironment() string {
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "AGENTFLOW_CODEX_HOST_ACTIVE", SessionEnv} {
		if os.Getenv(name) != "" {
			return name
		}
	}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "CODEX_") && name != "CODEX_HOME" {
			return name
		}
	}
	return ""
}

// PromoteResult is returned by Promote.
type PromoteResult struct {
	Base   string `json:"base"`
	Head   string `json:"head"`
	Title  string `json:"title"`
	URL    string `json:"url,omitempty"`
	Exists bool   `json:"exists,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

const promoteBody = "Promotes the integration branch to the release branch.\n\n" +
	"**Merge with a merge commit** (not squash or rebase) so the release branch keeps the history " +
	"of the integration branch and later promotions apply cleanly.\n\nOpened by `agentflow promote`."

// Promote opens a pull request from the integration branch into main. Only a
// person may run it: it refuses inside an AI agent unless AGENTFLOW_ALLOW_PROMOTE=1.
func (a *App) Promote(ctx context.Context, from, to string, dryRun bool, token string, gh worktree.GH) (any, error) {
	if name := AgentEnvironment(); name != "" && os.Getenv("AGENTFLOW_ALLOW_PROMOTE") != "1" {
		return nil, fmt.Errorf("promote is for the developer, not an AI agent (%s is set); ask the user to run `agentflow promote`", name)
	}
	if gh == nil {
		gh = worktree.ExecGH
	}
	if to == "" {
		to = "main"
	}
	if from == "" {
		from = a.Repo.DefaultBranch(ctx)
		if from == to {
			from = "develop"
		}
	}
	if from == to {
		return nil, fmt.Errorf("cannot promote %s into itself", from)
	}
	res := PromoteResult{Base: to, Head: from, Title: fmt.Sprintf("chore(release): promote %s to %s", from, to), DryRun: dryRun}
	if out, err := gh(ctx, a.Repo.Dir, "pr", "list", "--base", to, "--head", from, "--state", "open", "--json", "url", "--jq", ".[0].url // empty"); err == nil && strings.TrimSpace(out) != "" {
		res.URL, res.Exists = strings.TrimSpace(out), true
		return res, nil
	}
	if dryRun {
		return res, nil
	}
	args := []string{"pr", "create", "--base", to, "--head", from, "--title", res.Title, "--body", promoteBody}
	if p, err := a.gate(policy.Remote, "promote", res, token, false, []string{"gh " + strings.Join(args[:6], " ")}); p != nil || err != nil {
		return p, err
	}
	out, err := gh(ctx, a.Repo.Dir, args...)
	a.audit(policy.Remote, "promote", res, err)
	if err != nil {
		return nil, err
	}
	res.URL = strings.TrimSpace(out)
	return res, nil
}

// InPrimaryCheckout reports whether the app runs in a repository's primary
// checkout rather than a linked worktree.
func (a *App) InPrimaryCheckout(ctx context.Context) bool {
	return !worktree.IsLinked(ctx, a.Repo.Dir)
}
