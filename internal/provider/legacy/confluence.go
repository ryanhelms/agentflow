package legacy

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/atlassian"
	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/confluence"
)

// ConfluenceClient is the search API exposed by the legacy Atlassian client.
type ConfluenceClient interface {
	SearchConfluence(ctx context.Context, text, space string, limit int) ([]atlassian.Page, error)
}

// Existing search-only clients can still be passed to NewConfluence.
type confluencePages interface {
	GetConfluencePage(context.Context, string) (atlassian.CurrentPage, error)
	GetConfluenceVersion(context.Context, string, int64) (atlassian.PageVersion, error)
	ReplaceConfluencePage(context.Context, atlassian.CurrentPage, string, string, atlassian.PageVersion) error
}

// Confluence reads and updates pages at a single site. References use the space key and page ID.
type Confluence struct {
	instance string
	client   ConfluenceClient
}

func NewConfluence(instance string, client ConfluenceClient) *Confluence {
	return &Confluence{instance: instance, client: client}
}

var (
	_ confluencePages    = (*atlassian.Client)(nil)
	_ ConfluenceClient   = (*atlassian.Client)(nil)
	_ provider.Knowledge = (*Confluence)(nil)
)

func (c *Confluence) page(ctx context.Context, ref provider.Ref) (confluencePages, atlassian.CurrentPage, error) {
	var zero atlassian.CurrentPage
	if err := ref.Validate(); err != nil {
		return nil, zero, err
	}
	if ref.Instance != c.instance {
		return nil, zero, fmt.Errorf("reference instance %q does not match %q", ref.Instance, c.instance)
	}
	api, ok := c.client.(confluencePages)
	if !ok {
		return nil, zero, provider.Unsupported(c.instance, "read a confluence page by id")
	}
	page, err := api.GetConfluencePage(ctx, ref.ID)
	if err != nil {
		return nil, zero, atlassianError(err)
	}
	if page.Space != ref.Scope {
		return nil, zero, fmt.Errorf("%w: page %s is outside space %q", provider.ErrNotFound, ref.ID, ref.Scope)
	}
	return api, page, nil
}

func (c *Confluence) Get(ctx context.Context, ref provider.Ref) (provider.Document, error) {
	_, page, err := c.page(ctx, ref)
	if err != nil {
		return provider.Document{}, err
	}
	return provider.Document{Ref: ref, Title: page.Title, Body: page.Text, URL: page.URL,
		Revision: strconv.FormatInt(page.Version.Number, 10)}, nil
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
		docs = append(docs, provider.Document{Ref: ref, Title: page.Title, Body: page.Excerpt, URL: page.URL, Revision: page.Revision})
	}
	return docs, nil
}

// ConfluenceReplacementVersion checks the destination and supported write
// options before approval and for direct Knowledge.Publish callers.
func ConfluenceReplacementVersion(ref provider.Ref, revision string, links []provider.Link) (int64, error) {
	return confluence.ReplacementVersion(ref, revision, links)
}

// Publish replaces one published revision. Its successor's version message
// is the receipt, even if another author has since published a later edit.
func (c *Confluence) Publish(ctx context.Context, ref provider.Ref, draft provider.DocumentDraft, opts provider.WriteOptions) (provider.WriteResult, error) {
	var zero provider.WriteResult
	_, err := ConfluenceReplacementVersion(ref, opts.ExpectedRevision, draft.Links)
	if err != nil {
		return zero, err
	}
	if strings.TrimSpace(opts.OperationID) == "" || strings.TrimSpace(draft.Title) == "" {
		return zero, errors.New("document title and stable operation ID are required")
	}
	api, page, err := c.page(ctx, ref)
	if err != nil {
		return zero, err
	}
	replacement, err := confluence.NewReplacement(page.URL, ref, draft, opts)
	if err != nil {
		return zero, err
	}
	out, err := replacement.Replay(page.Version, true, func(number int64) (atlassian.PageVersion, error) {
		version, err := api.GetConfluenceVersion(ctx, ref.ID, number)
		return version, atlassianError(err)
	})
	if out.Status != "" || err != nil {
		return out, err
	}
	if err := api.ReplaceConfluencePage(ctx, page, draft.Title, draft.Body, replacement.Successor); err != nil {
		return zero, fmt.Errorf("replace Confluence page: %w", atlassianError(err))
	}
	return replacement.Result(provider.Applied), nil
}
