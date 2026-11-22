package legacy

import (
	"context"
	"fmt"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/provider"
)

// ConfluenceClient is the search API exposed by the legacy Atlassian client.
type ConfluenceClient interface {
	SearchConfluence(ctx context.Context, text, space string, limit int) ([]atlassian.Page, error)
}

// Confluence searches pages at a single site. References use the space key and page ID.
type Confluence struct {
	instance string
	client   ConfluenceClient
}

func NewConfluence(instance string, client ConfluenceClient) *Confluence {
	return &Confluence{instance: instance, client: client}
}

var (
	_ ConfluenceClient   = (*atlassian.Client)(nil)
	_ provider.Knowledge = (*Confluence)(nil)
)

// Get returns ErrUnsupported because the client exposes only search excerpts.
func (c *Confluence) Get(context.Context, provider.Ref) (provider.Document, error) {
	return provider.Document{}, provider.Unsupported(c.instance, "read a confluence page by id")
}

// Find returns search excerpts in Document.Body.
func (c *Confluence) Find(ctx context.Context, query provider.DocumentQuery) ([]provider.Document, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 10
	}
	pages, err := c.client.SearchConfluence(ctx, query.Text, query.Scope, limit)
	if err != nil {
		return nil, fmt.Errorf("search confluence pages: %w", atlassianError(err))
	}
	docs := make([]provider.Document, 0, len(pages))
	for _, page := range pages {
		space := page.Space
		if space == "" {
			space = query.Scope
		}
		ref := provider.Ref{Instance: c.instance, Scope: space, ID: page.ID}
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("invalid confluence search hit %q: %w", page.Title, err)
		}
		docs = append(docs, provider.Document{Ref: ref, Title: page.Title, Body: page.Excerpt, URL: page.URL})
	}
	return docs, nil
}

// Publish returns ErrUnsupported because the client has no Confluence write API.
func (c *Confluence) Publish(context.Context, provider.Ref, provider.DocumentDraft, provider.WriteOptions) (provider.WriteResult, error) {
	return provider.WriteResult{}, provider.Unsupported(c.instance, "publish a confluence page")
}
