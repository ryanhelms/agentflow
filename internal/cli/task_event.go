package cli

import (
	"context"
	"errors"
	"os"
	"strconv"

	"github.com/nscaledev/devflow/internal/event"
	"github.com/nscaledev/devflow/internal/provider"
)

func runGitHubTaskEvent(ctx context.Context, dryRun bool) error {
	repoID, err := strconv.ParseInt(os.Getenv("GITHUB_REPOSITORY_ID"), 10, 64)
	if err != nil || repoID <= 0 {
		return errors.New("github task events require GITHUB_REPOSITORY_ID")
	}
	a, err := open(ctx)
	if err != nil {
		return err
	}
	metadata := event.GitHubActionsContext{
		Repository:    provider.RepositoryRef{Host: "github.com", Path: os.Getenv("GITHUB_REPOSITORY")},
		RepositoryID:  repoID,
		DefaultBranch: os.Getenv("DEVFLOW_DEFAULT_BRANCH"),
		EventName:     os.Getenv("GITHUB_EVENT_NAME"),
		EventID:       os.Getenv("GITHUB_RUN_ID"),
	}
	result, err := a.ApplyGitHubTaskEvent(ctx, metadata, os.Getenv("GITHUB_EVENT_PATH"), dryRun)
	if err != nil {
		return err
	}
	return printJSON(result)
}
