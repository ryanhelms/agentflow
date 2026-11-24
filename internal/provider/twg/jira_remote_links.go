package twg

import (
	"context"

	"github.com/nscaledev/agentflow/internal/atlassian"
	"github.com/nscaledev/agentflow/internal/provider"
)

// RemoteLinks lists the issue's web links, excluding its Jira parent.
func (j *Jira) RemoteLinks(ctx context.Context, ref provider.Ref) ([]atlassian.Page, error) {
	task, err := j.GetWithRemoteLinks(ctx, ref)
	if err != nil {
		return nil, err
	}
	pages := make([]atlassian.Page, 0, len(task.Links))
	for _, link := range task.Links {
		if link.Ref == nil {
			pages = append(pages, atlassian.Page{Title: link.Title, URL: link.URL})
		}
	}
	return pages, nil
}
