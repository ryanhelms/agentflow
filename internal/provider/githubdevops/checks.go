package githubdevops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ryanhelms/agentflow/internal/provider"
)

const pageSize = 100

// Checks lists the latest check runs for a commit SHA, branch or tag.
func (a *Adapter) Checks(ctx context.Context, repo provider.RepositoryRef, revision string) ([]provider.CheckRun, error) {
	base, err := repositoryPath(repo)
	if err != nil {
		return nil, err
	}
	if !validSegment(revision) {
		return nil, errors.New("github checks require a revision")
	}
	base += "/commits/" + url.PathEscape(revision) + "/check-runs"
	var runs []provider.CheckRun
	for page := 1; ; page++ {
		var response struct {
			Total int `json:"total_count"`
			Runs  []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
				DetailsURL string `json:"details_url"`
			} `json:"check_runs"`
		}
		path := fmt.Sprintf("%s?filter=latest&per_page=%d&page=%d", base, pageSize, page)
		if err := a.client.Do(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, fmt.Errorf("list github check runs: %w", githubReadError(err, path))
		}
		for _, run := range response.Runs {
			link := run.HTMLURL
			if link == "" {
				link = run.DetailsURL
			}
			runs = append(runs, provider.CheckRun{
				Name: run.Name, State: run.Status, Conclusion: run.Conclusion, URL: link,
			})
		}
		if len(response.Runs) == 0 || len(runs) >= response.Total {
			return runs, nil
		}
	}
}
