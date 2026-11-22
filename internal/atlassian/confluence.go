package atlassian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
)

// PageVersion identifies a published Confluence page revision.
type PageVersion struct {
	Number  int64  `json:"number"`
	Message string `json:"message"`
}

type CurrentPage struct {
	ID, SpaceID, Space string
	Title, Text, URL   string
	Version            PageVersion
}

type confluencePageResponse struct {
	ID      string      `json:"id"`
	SpaceID string      `json:"spaceId"`
	Status  string      `json:"status"`
	Title   string      `json:"title"`
	Version PageVersion `json:"version"`
	Body    struct {
		ADF struct {
			Value string `json:"value"`
		} `json:"atlas_doc_format"`
	} `json:"body"`
}

func pagePath(id string) (string, error) {
	if !ValidConfluenceID(id) {
		return "", errors.New("expected a positive decimal Confluence page ID")
	}
	return "/wiki/api/v2/pages/" + id, nil
}

// ValidConfluenceID recognizes the decimal IDs used by the v2 page and space APIs.
func ValidConfluenceID(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == s
}

// Bind page operations to the configured site and endpoint, including on 307/308.
func (c Client) pageRequest(ctx context.Context, method, path string, in, out any) error {
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http = &client
	return c.do(ctx, method, path, in, out)
}

func (c *Client) GetConfluencePage(ctx context.Context, id string) (CurrentPage, error) {
	var zero CurrentPage
	path, err := pagePath(id)
	if err != nil {
		return zero, err
	}
	var page confluencePageResponse
	if err := c.pageRequest(ctx, "GET", path+"?body-format=atlas_doc_format", nil, &page); err != nil {
		return zero, err
	}
	if page.ID != id || page.Status != "current" || page.Version.Number < 1 || strings.TrimSpace(page.Title) == "" {
		return zero, errors.New("invalid current Confluence page in response")
	}
	space, err := c.confluenceSpaceKey(ctx, page.SpaceID)
	if err != nil {
		return zero, err
	}
	return CurrentPage{ID: id, SpaceID: page.SpaceID, Space: space, Title: page.Title, Version: page.Version,
		Text: ADFText(json.RawMessage(page.Body.ADF.Value)),
		URL:  c.Base + "/wiki/pages/viewpage.action?pageId=" + id}, nil
}

func (c *Client) confluenceSpaceKey(ctx context.Context, id string) (string, error) {
	if !ValidConfluenceID(id) {
		return "", errors.New("invalid Confluence space ID")
	}
	var space struct{ ID, Key string }
	if err := c.pageRequest(ctx, "GET", "/wiki/api/v2/spaces/"+id, nil, &space); err != nil {
		return "", err
	}
	if space.ID != id || space.Key == "" {
		return "", errors.New("invalid Confluence space in response")
	}
	return space.Key, nil
}

func (c *Client) GetConfluenceVersion(ctx context.Context, id string, number int64) (PageVersion, error) {
	var version PageVersion
	path, err := pagePath(id)
	if err != nil {
		return version, err
	}
	err = c.pageRequest(ctx, "GET", path+"/versions/"+strconv.FormatInt(number, 10), nil, &version)
	return version, err
}

// ReplaceConfluencePage sends only the fields required by the v2 update API.
// Treat Markdown as literal text inside Confluence storage markup.
func (c *Client) ReplaceConfluencePage(ctx context.Context, page CurrentPage, title, body string, version PageVersion) error {
	path, err := pagePath(page.ID)
	if err != nil {
		return err
	}
	storage := "<p>" + strings.ReplaceAll(html.EscapeString(body), "\n", "<br />") + "</p>"
	in := map[string]any{
		"id": page.ID, "status": "current", "title": title, "version": version,
		"body": map[string]string{"representation": "storage", "value": storage},
	}
	var out confluencePageResponse
	if err := c.pageRequest(ctx, "PUT", path, in, &out); err != nil {
		return err
	}
	if out.ID != page.ID || out.SpaceID != page.SpaceID || out.Status != "current" || out.Title != title || out.Version != version {
		return fmt.Errorf("could not confirm Confluence page %s version %d, retry the same operation to verify", page.ID, version.Number)
	}
	return nil
}
