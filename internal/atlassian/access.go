package atlassian

import (
	"context"
	"errors"
	"net/http"
)

// CheckJiraAccess verifies the Jira identity using a read-only, site-bound request.
func (c *Client) CheckJiraAccess(ctx context.Context) error {
	var user User
	if err := c.siteRequest(ctx, http.MethodGet, "/rest/api/3/myself", nil, &user); err != nil {
		return err
	}
	if user.AccountID == "" {
		return errors.New("invalid Jira identity response")
	}
	return nil
}

// CheckConfluenceAccess verifies access to the Confluence space listing.
func (c *Client) CheckConfluenceAccess(ctx context.Context) error {
	var spaces struct {
		Results []struct{} `json:"results"`
	}
	if err := c.siteRequest(ctx, http.MethodGet, "/wiki/api/v2/spaces?limit=1", nil, &spaces); err != nil {
		return err
	}
	if spaces.Results == nil {
		return errors.New("invalid Confluence spaces response")
	}
	return nil
}
