package twg

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
)

// Confluence reads pages and live docs on one Atlassian site. References use
// the Confluence space key as scope and the decimal page ID as ID.
type Confluence struct{ binding }

var _ provider.Knowledge = (*Confluence)(nil)

// NewConfluence binds reads to the configured space keys. An empty list permits
// searches across the configured site.
func NewConfluence(instance, site string, spaces []string) (*Confluence, error) {
	b, err := newBinding(instance, site, spaces, spaceKey)
	if err != nil {
		return nil, err
	}
	return &Confluence{binding: b}, nil
}

type confluenceVersion struct {
	Number int64 `json:"number"`
}

func (v *confluenceVersion) UnmarshalJSON(raw []byte) error {
	if raw[0] == '{' {
		type object confluenceVersion
		return json.Unmarshal(raw, (*object)(v))
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return err
	}
	if number == "" {
		return nil
	}
	var err error
	v.Number, err = number.Int64()
	return err
}

type confluenceSpace struct {
	Key string `json:"key"`
}

func (s *confluenceSpace) UnmarshalJSON(raw []byte) error {
	if raw[0] == '"' {
		return json.Unmarshal(raw, &s.Key)
	}
	type object confluenceSpace
	return json.Unmarshal(raw, (*object)(s))
}

type confluencePage struct {
	ID          json.Number       `json:"id"`
	ContentID   json.Number       `json:"contentId"`
	Title       string            `json:"title"`
	Type        string            `json:"type"`
	ContentType string            `json:"contentType"`
	SpaceKey    string            `json:"spaceKey"`
	Space       confluenceSpace   `json:"space"`
	URL         string            `json:"url"`
	WebURL      string            `json:"webUrl"`
	Version     confluenceVersion `json:"version"`
	Links       struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
	Body struct {
		Value *string `json:"value"`
	} `json:"body"`
}

type confluenceHit struct {
	confluencePage
	Content *confluencePage `json:"content"`
	Excerpt string          `json:"excerpt"`
}

func (c *Confluence) Get(ctx context.Context, ref provider.Ref) (provider.Document, error) {
	if err := c.checkRef(ref); err != nil {
		return provider.Document{}, err
	}
	if !pageID.MatchString(ref.ID) {
		return provider.Document{}, fmt.Errorf("confluence requires a positive decimal page id: %w", ErrAccessDenied)
	}
	var page confluencePage
	args := []string{"confluence", "content", "get", ref.ID, "--detail", "full", "--format", "md"}
	if err := c.read(ctx, "read confluence page", args, &page); err != nil {
		return provider.Document{}, err
	}
	doc, err := c.document(page, []string{ref.Scope})
	if err != nil {
		return provider.Document{}, err
	}
	if doc.Ref.ID != ref.ID || page.Body.Value == nil {
		return provider.Document{}, fmt.Errorf("confluence read returned a different page or omitted the inline body: %w", ErrFailure)
	}
	doc.Body = *page.Body.Value
	return doc, nil
}

func (c *Confluence) Find(ctx context.Context, q provider.DocumentQuery) ([]provider.Document, error) {
	scopes, err := c.queryScopes(q.Scope)
	if err != nil {
		return nil, err
	}
	query, err := textFilter(andFilter("type = page", scopeClause("space", scopes)), "text", q.Text)
	if err != nil {
		return nil, err
	}
	limit := queryLimit(q.Limit, 10, 100)
	var result struct {
		Results []confluenceHit `json:"results"`
	}
	args := []string{"confluence", "search", "query", "--cql", query, "--limit", strconv.Itoa(limit)}
	if err := c.read(ctx, "find confluence pages", args, &result); err != nil {
		return nil, err
	}
	if result.Results == nil {
		return nil, fmt.Errorf("confluence query omitted its results list: %w", ErrFailure)
	}
	docs := make([]provider.Document, 0, min(len(result.Results), limit))
	for _, hit := range result.Results {
		page := hit.confluencePage
		if hit.Content != nil {
			page = *hit.Content
			if page.Title == "" {
				page.Title = hit.Title
			}
			if page.SpaceKey == "" {
				page.SpaceKey = first(hit.SpaceKey, hit.Space.Key)
			}
		}
		doc, err := c.document(page, scopes, hit.URL, hit.WebURL)
		if err != nil {
			return nil, err
		}
		doc.Body = strings.NewReplacer("@@@hl@@@", "", "@@@endhl@@@", "").Replace(hit.Excerpt)
		if len(docs) < limit {
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

func (c *Confluence) document(page confluencePage, scopes []string, resultURLs ...string) (provider.Document, error) {
	id := first(page.ID.String(), page.ContentID.String())
	if !pageID.MatchString(id) || strings.TrimSpace(page.Title) == "" {
		return provider.Document{}, fmt.Errorf("confluence result is missing a valid page id or title: %w", ErrFailure)
	}
	if page.ContentID != "" && page.ContentID.String() != id {
		return provider.Document{}, fmt.Errorf("confluence result has conflicting page identity: %w", ErrFailure)
	}
	switch strings.ToLower(first(page.ContentType, page.Type)) {
	case "", "page", "live_doc", "live-doc", "livedoc":
	default:
		return provider.Document{}, fmt.Errorf("confluence result is not a page: %w", ErrFailure)
	}
	scope := first(page.SpaceKey, page.Space.Key)
	if page.SpaceKey != "" && page.Space.Key != "" && page.SpaceKey != page.Space.Key {
		return provider.Document{}, fmt.Errorf("confluence result has conflicting space identity: %w", ErrFailure)
	}
	for _, raw := range append(resultURLs, page.URL, page.WebURL, page.Links.WebUI) {
		if raw == "" {
			continue
		}
		u, err := c.sameSiteURL(raw)
		if err != nil {
			return provider.Document{}, err
		}
		parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(u.Path, "/wiki/"), "/"), "/")
		if len(parts) >= 4 && parts[0] == "spaces" && parts[2] == "pages" {
			if id != parts[3] || (scope != "" && scope != parts[1]) {
				return provider.Document{}, fmt.Errorf("confluence result has conflicting page or space identity: %w", ErrFailure)
			}
			scope = parts[1]
		}
	}
	if !spaceKey.MatchString(scope) || (len(scopes) > 0 && !slices.Contains(scopes, scope)) {
		return provider.Document{}, fmt.Errorf("confluence result is outside the requested space or omitted its space identity: %w", ErrFailure)
	}
	doc := provider.Document{
		Ref: c.ref(scope, id), Title: page.Title, URL: c.site + "/wiki/spaces/" + scope + "/pages/" + id,
	}
	if page.Version.Number > 0 {
		doc.Revision = strconv.FormatInt(page.Version.Number, 10)
	}
	return doc, nil
}

func (c *Confluence) Publish(context.Context, provider.Ref, provider.DocumentDraft, provider.WriteOptions) (provider.WriteResult, error) {
	return provider.WriteResult{}, provider.Unsupported(c.instance, "publish a confluence page through twg")
}
