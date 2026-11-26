package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/nscaledev/agentflow/internal/boundary"
	"github.com/nscaledev/agentflow/internal/delegate"
	"github.com/nscaledev/agentflow/internal/policy"
)

// Delegate hands a task to an agent session in another repository: a running
// tmux session there, the client's own session tools, or a new tmux session in
// that repository's checkout (worktree.checkouts in ~/.agentflow/config.yml).
// A session never changes another repository itself; see docs/repo-boundary.md.
func (a *App) Delegate(ctx context.Context, req delegate.Request, token string) (any, error) {
	name := delegate.RepoName(req.Repo)
	if name == "" {
		return nil, errors.New("delegate needs --repo NAME: the repository the work belongs to")
	}
	home, err := boundary.ResolveDir(ctx, a.Repo.Dir)
	if err != nil {
		return nil, err
	}
	checkout := a.WorktreeSettings().Checkout(name)
	target, err := boundary.ResolveDir(ctx, checkout)
	if err != nil {
		return nil, fmt.Errorf("no checkout of %s at %s: clone it there or set worktree.checkouts.%s in ~/.agentflow/config.yml", name, checkout, name)
	}
	if boundary.Same(home, target) {
		return nil, fmt.Errorf("%s is this session's own repository; do the work here", target.Label())
	}
	req.Agent = delegate.DefaultAgent(req.Agent, a.Client)
	plan, err := delegate.Build(ctx, home, target, checkout, req, delegate.NativeSessions(), boundary.GitResolver(ctx))
	if err != nil || req.DryRun || plan.Status == delegate.StatusNative {
		return plan, err
	}
	if p, err := a.gate(policy.Local, "delegate", plan, token, false, plan.Commands); p != nil || err != nil {
		return p, err
	}
	res, err := delegate.Apply(ctx, plan)
	a.audit(policy.Local, "delegate", plan, err)
	return res, err
}
