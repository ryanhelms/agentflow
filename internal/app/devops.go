package app

import (
	"context"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gh"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/githubdevops"
)

func devOpsRepository(selection config.Selection, defaultRepo string) provider.RepositoryRef {
	repo := provider.RepositoryRef{Host: "github.com", Path: defaultRepo}
	if cfg, ok := selection.Settings.(*config.GitHubSettings); ok && cfg.Repository != nil {
		repo.Path = *cfg.Repository
	}
	return repo
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
