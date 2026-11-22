package githubdevops

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/nscaledev/devflow/internal/gh"
	"github.com/nscaledev/devflow/internal/provider"
)

// Deployments lists deployments and their latest status in GitHub's order.
// A nonempty revision filters by the deployed commit SHA.
func (a *Adapter) Deployments(ctx context.Context, repo provider.RepositoryRef, revision string) ([]provider.Deployment, error) {
	base, err := repositoryPath(repo)
	if err != nil {
		return nil, err
	}
	base += "/deployments"
	query := url.Values{"per_page": {strconv.Itoa(pageSize)}}
	if revision != "" {
		query.Set("sha", revision)
	}
	var deployments []provider.Deployment
	for page := 1; ; page++ {
		query.Set("page", strconv.Itoa(page))
		path := base + "?" + query.Encode()
		var response []struct {
			gh.Deployment
			URL string `json:"url"`
		}
		if err := a.client.Do(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, fmt.Errorf("list github deployments: %w", githubReadError(err, path))
		}
		for _, deployment := range response {
			if revision != "" && deployment.SHA != revision {
				continue
			}
			fact := provider.Deployment{
				Environment: deployment.Environment, Revision: deployment.SHA, URL: deployment.URL,
				ID: strconv.FormatInt(deployment.ID, 10), Ref: deployment.Ref, CreatedAt: deployment.CreatedAt,
			}
			if err := a.deploymentStatus(ctx, base, deployment.ID, &fact); err != nil {
				return nil, err
			}
			deployments = append(deployments, fact)
		}
		if len(response) < pageSize {
			return deployments, nil
		}
	}
}

func (a *Adapter) deploymentStatus(ctx context.Context, base string, id int64, deployment *provider.Deployment) error {
	path := fmt.Sprintf("%s/%d/statuses?per_page=1", base, id)
	var statuses []struct {
		State          string `json:"state"`
		LogURL         string `json:"log_url"`
		TargetURL      string `json:"target_url"`
		EnvironmentURL string `json:"environment_url"`
	}
	if err := a.client.Do(ctx, http.MethodGet, path, nil, &statuses); err != nil {
		return fmt.Errorf("read github deployment %d status: %w", id, githubReadError(err, path))
	}
	if len(statuses) > 0 {
		status := statuses[0]
		deployment.State = status.State
		deployment.URL = cmp.Or(status.LogURL, status.TargetURL, status.EnvironmentURL, deployment.URL)
	}
	return nil
}
