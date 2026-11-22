package app

import (
	"context"
	"errors"

	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/setup"
)

// ApplyProviderSetup has just one supported write: registering the selected
// project adapter. All prerequisites are checked again on a confirmed retry.
func (a *App) ApplyProviderSetup(ctx context.Context, client, token string) (any, error) {
	if client == "" {
		return nil, errors.New("select the project adapter to register with client")
	}
	plan, err := setup.PlanProviderSetup(ctx, a.Repo.Dir, client)
	if err != nil {
		return nil, err
	}
	for _, requirement := range plan.Requirements {
		if !requirement.Ready {
			return plan, nil
		}
	}
	if plan.Registration == nil || plan.Registration.State != "pending" {
		return plan, nil
	}
	const action = "setup.register_client"
	pending, err := a.gate(policy.Local, action, plan, token, true, []string{"Review and merge the " + client + " project adapter into " + plan.Registration.Path})
	if err != nil || pending != nil {
		return pending, err
	}
	err = setup.RegisterProviderClient(plan.Root, client)
	a.audit(policy.Local, action, plan, err)
	if err != nil {
		return nil, err
	}
	return setup.PlanProviderSetup(ctx, a.Repo.Dir, client)
}
