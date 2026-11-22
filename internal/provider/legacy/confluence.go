package legacy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/provider"
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
	if revision == "" {
		return 0, provider.Unsupported(ref.Instance, "create a Confluence page; replacement requires an expected revision")
	}
	if !atlassian.ValidConfluenceID(ref.ID) {
		return 0, errors.New("expected a positive decimal Confluence page ID")
	}
	expected, err := strconv.ParseInt(revision, 10, 64)
	if err != nil || expected < 1 || expected == math.MaxInt64 || strconv.FormatInt(expected, 10) != revision {
		return 0, errors.New("expected a positive decimal Confluence page revision with a successor")
	}
	if len(links) > 0 {
		return 0, provider.Unsupported(ref.Instance, "publish structured Confluence document links")
	}
	return expected, nil
}

// Publish replaces one published revision. Its successor's version message
// is the receipt, even if another author has since published a later edit.
func (c *Confluence) Publish(ctx context.Context, ref provider.Ref, draft provider.DocumentDraft, opts provider.WriteOptions) (provider.WriteResult, error) {
	var zero provider.WriteResult
	expected, err := ConfluenceReplacementVersion(ref, opts.ExpectedRevision, draft.Links)
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
	// Hash a fixed sequence of strings so arbitrary input cannot alter the
	// boundary between fields or inject a second marker into the message.
	payload, err := json.Marshal([]string{page.URL, ref.String(), opts.ExpectedRevision, opts.OperationID, draft.Title, draft.Body})
	if err != nil {
		return zero, err
	}
	next := atlassian.PageVersion{Number: expected + 1, Message: fmt.Sprintf("devflow docs publish %x", sha256.Sum256(payload))}
	out := provider.WriteResult{Ref: ref, URL: page.URL, Revision: strconv.FormatInt(next.Number, 10)}
	if page.Version.Number != expected {
		receipt := page.Version
		if receipt.Number > next.Number {
			receipt, err = api.GetConfluenceVersion(ctx, ref.ID, next.Number)
			if err != nil {
				return zero, fmt.Errorf("cannot verify earlier Confluence version %d: %w", next.Number, atlassianError(err))
			}
		}
		if receipt != next {
			return zero, fmt.Errorf("%w: expected Confluence revision %d, found %d", provider.ErrConflict, expected, page.Version.Number)
		}
		out.Status = provider.AlreadyApplied
		return out, nil
	}
	if err := api.ReplaceConfluencePage(ctx, page, draft.Title, draft.Body, next); err != nil {
		return zero, fmt.Errorf("replace Confluence page: %w", atlassianError(err))
	}
	out.Status = provider.Applied
	return out, nil
}
