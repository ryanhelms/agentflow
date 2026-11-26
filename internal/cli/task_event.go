package cli

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strconv"

	"github.com/ryanhelms/agentflow/internal/event"
	"github.com/ryanhelms/agentflow/internal/provider"
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
	host := "github.com"
	if server := os.Getenv("GITHUB_SERVER_URL"); server != "" {
		u, err := url.Parse(server)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("expected an HTTPS GITHUB_SERVER_URL")
		}
		host = u.Host
	}
	metadata := event.GitHubActionsContext{
		Repository:    provider.RepositoryRef{Host: host, Path: os.Getenv("GITHUB_REPOSITORY")},
		RepositoryID:  repoID,
		DefaultBranch: os.Getenv("AGENTFLOW_DEFAULT_BRANCH"),
		EventName:     os.Getenv("GITHUB_EVENT_NAME"),
		EventID:       os.Getenv("GITHUB_RUN_ID"),
		ApprovedIssue: os.Getenv("AGENTFLOW_APPROVED_ISSUE"),
	}
	result, err := a.ApplyGitHubTaskEvent(ctx, metadata, os.Getenv("GITHUB_EVENT_PATH"), dryRun)
	if err != nil {
		return err
	}
	return printJSON(result)
}
