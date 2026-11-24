package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/gh"
	"github.com/nscaledev/agentflow/internal/gitx"
	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/devopsfixture"
	"github.com/nscaledev/agentflow/internal/provider/githubdevops"
)

func devOpsRepository(selection config.Selection, defaultRepo string) provider.RepositoryRef {
	repo := provider.RepositoryRef{Host: "github.com", Path: defaultRepo}
	if cfg, ok := selection.Settings.(*config.GitHubSettings); ok && cfg.Repository != nil {
		repo.Path = *cfg.Repository
	}
	return repo
}

func (a *App) devOpsGitHubRepository(ctx context.Context, role string, selection config.Selection) (provider.RepositoryRef, error) {
	repo := devOpsRepository(selection, "")
	cfg, ok := selection.Settings.(*config.GitHubSettings)
	if !ok || cfg.Repository == nil {
		d, err := a.detectForge(ctx)
		if err != nil {
			return provider.RepositoryRef{}, fmt.Errorf("detect %s repository: %w", role, err)
		}
		if d.OK() && d.Forge == gitx.ForgeGitHub && strings.HasPrefix(d.Target.ID(), "github.com/") && githubDotComPort(d.Target.URL) {
			repo.Path = d.Target.URL.Path
		}
	}
	if err := gitx.ValidateGitHubSlug(repo.Path); err != nil {
		return provider.RepositoryRef{}, fmt.Errorf("configure %s.settings.repository or select a GitHub.com target: %w", role, err)
	}
	return repo, nil
}

func (a *App) shipDeployments(ctx context.Context, selection config.Selection, revision string) (any, error) {
	var deployments provider.Deployments
	var repo provider.RepositoryRef
	switch selection.Provider {
	case "", "github":
		var err error
		repo, err = a.devOpsGitHubRepository(ctx, "devops.deployments", selection)
		if err != nil {
			return nil, err
		}
		client, err := a.GitHub(ctx)
		if err != nil {
			return nil, err
		}
		deployments = githubdevops.New(client)
	case "fixture":
		settings := selection.Settings.(*config.DevOpsFixtureSettings)
		fixture, err := devopsfixture.LoadDeployments(a.Repo.Dir, settings.Path)
		if err != nil {
			return nil, err
		}
		deployments = fixture
	default:
		return nil, provider.Unsupported(selection.Provider, "deployments")
	}
	records, err := deployments.Deployments(ctx, repo, revision)
	if err != nil {
		return nil, err
	}
	latest := map[string]provider.Deployment{}
	for _, record := range records {
		if _, seen := latest[record.Environment]; !seen && record.Revision == revision {
			latest[record.Environment] = record
		}
	}
	if selection.Provider == "fixture" {
		return latest, nil
	}
	// GitHub status consumers already use sha, numeric id and the original ref.
	legacy := make(map[string]gh.Deployment, len(latest))
	for env, record := range latest {
		id, _ := strconv.ParseInt(record.ID, 10, 64)
		legacy[env] = gh.Deployment{
			Environment: record.Environment, Ref: record.Ref, SHA: record.Revision,
			CreatedAt: record.CreatedAt, State: record.State, ID: id,
		}
	}
	return legacy, nil
}

func (a *App) changeChecks(ctx context.Context, client *gh.Client, repo, revision string) ([]provider.CheckRun, error) {
	selection := a.Cfg.Providers.DevOps.Checks
	switch selection.Provider {
	case "", "github":
		var checks provider.Checks = githubdevops.New(client)
		return checks.Checks(ctx, devOpsRepository(selection, repo), revision)
	default:
		return nil, provider.Unsupported(selection.Provider, "checks")
	}
}
