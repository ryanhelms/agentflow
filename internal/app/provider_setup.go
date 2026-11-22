package app

import (
	"context"
	"errors"

	"github.com/nscaledev/devflow/internal/managedgh"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/setup"
)

// ApplyProviderSetup gates one local action against a freshly inspected plan.
func (a *App) ApplyProviderSetup(ctx context.Context, client, token string, installGH bool) (any, error) {
	if !installGH && client == "" {
		return nil, errors.New("select a project adapter or request the managed gh installation")
	}
	plan, err := setup.PlanProviderSetup(ctx, a.Repo.Dir, client)
	if err != nil {
		return nil, err
	}
	var (
		action, instruction string
		summary             any = plan
		apply               func() error
	)
	if installGH {
		if plan.InstallGH == nil {
			return plan, nil
		}
		action = "setup.install_gh"
		summary = struct {
			Workspace string                  `json:"workspace"`
			Client    string                  `json:"client,omitempty"`
			InstallGH *managedgh.Installation `json:"install_gh"`
		}{plan.Root, client, plan.InstallGH}
		instruction = "Review the gh artifact and run devflow setup apply --install-gh"
		apply = func() error { return plan.InstallGH.Apply(ctx) }
	} else {
		for _, requirement := range plan.Requirements {
			if !requirement.Ready {
				return plan, nil
			}
		}
		if plan.Registration == nil || plan.Registration.State != "pending" {
			return plan, nil
		}
		action = "setup.register_client"
		instruction = "Review and merge the " + client + " project adapter into " + plan.Registration.Path
		apply = func() error { return setup.RegisterProviderClient(plan.Root, client) }
	}
	pending, err := a.gate(policy.Local, action, summary, token, true, []string{instruction})
	if err != nil || pending != nil {
		return pending, err
	}
	err = apply()
	a.audit(policy.Local, action, summary, err)
	if err != nil {
		return nil, err
	}
	return setup.PlanProviderSetup(ctx, a.Repo.Dir, client)
}
