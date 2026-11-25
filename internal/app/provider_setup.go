package app

import (
	"context"
	"errors"

	"github.com/nscaledev/devflow/internal/managedgh"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provision"
	"github.com/nscaledev/devflow/internal/setup"
)

// ApplyProviderSetup gates one local action against a freshly inspected plan.
func (a *App) ApplyProviderSetup(ctx context.Context, client, token string, installGH bool) (any, error) {
	return a.ApplySetup(ctx, SetupAction{Client: client, InstallGH: installGH}, token)
}

type SetupAction struct {
	Client     string
	Companion  string
	InstallGH  bool
	InstallMCP bool
}

// ApplySetup performs exactly one confirmed install or registration.
func (a *App) ApplySetup(ctx context.Context, req SetupAction, token string) (any, error) {
	if req.InstallGH && (req.Companion != "" || req.InstallMCP) || req.InstallMCP && (req.Companion == "" || req.Client != "") {
		return nil, errors.New("select one installation or client registration per approval")
	}
	if !req.InstallGH && !req.InstallMCP && req.Client == "" {
		return nil, errors.New("select a project adapter or request the managed gh installation")
	}
	plan, err := setup.PlanSetup(ctx, a.Repo.Dir, req.Client, req.Companion)
	if err != nil {
		return nil, err
	}
	var (
		action, instruction string
		summary             any = plan
		apply               func() error
	)
	switch {
	case req.InstallGH:
		if plan.InstallGH == nil {
			return plan, nil
		}
		action = "setup.install_gh"
		summary = struct {
			Workspace string                  `json:"workspace"`
			Client    string                  `json:"client,omitempty"`
			InstallGH *managedgh.Installation `json:"install_gh"`
		}{plan.Root, req.Client, plan.InstallGH}
		instruction = "Review the gh artifact and run devflow setup apply --install-gh"
		apply = func() error { return plan.InstallGH.Apply(ctx) }
	case req.Companion != "":
		if len(plan.Companions) != 1 {
			return plan, nil
		}
		s := plan.Companions[0]
		if req.InstallMCP {
			if s.Kind == provision.RemoteMCP {
				return nil, errors.New("hosted MCP uses an endpoint and has no installation action")
			}
			if s.Install == nil {
				return plan, nil
			}
			action = "setup.install_mcp"
			instruction = "Install the reviewed raw executable for " + s.ID
			apply = func() error { return s.Install.Apply(ctx) }
		} else {
			if !s.Ready || plan.Registration == nil || plan.Registration.State != "pending" {
				return plan, nil
			}
			action = "setup.register_mcp"
			instruction = plan.Registration.Detail
			apply = func() error { return setup.RegisterCompanion(plan.Root, plan.Registration, s) }
		}
	default:
		for _, requirement := range plan.Requirements {
			if !requirement.Ready && !requirement.VerifyInClient {
				return plan, nil
			}
		}
		if plan.Registration == nil || plan.Registration.State != "pending" {
			return plan, nil
		}
		action = "setup.register_client"
		instruction = "Review and merge the " + req.Client + " project adapter into " + plan.Registration.Path
		apply = func() error { return setup.RegisterProviderClient(plan.Root, req.Client) }
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
	return setup.PlanSetup(ctx, a.Repo.Dir, req.Client, req.Companion)
}
