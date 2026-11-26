package twg

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/atlassian"
	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/confluence"
)

// Confluence reads pages and replaces published pages on one Atlassian site. References use
// the Confluence space key as scope and the decimal page ID as ID.
type Confluence struct {
	binding
	receiptSite string
}

var _ provider.Knowledge = (*Confluence)(nil)

// NewConfluence binds reads to the configured space keys. An empty list permits
// searches across the configured site.
func NewConfluence(instance, site string, spaces []string) (*Confluence, error) {
	b, err := newBinding(instance, site, spaces, spaceKey)
	if err != nil {
		return nil, err
	}
	return &Confluence{binding: b, receiptSite: site}, nil
}

type confluenceVersion struct {
	Number  int64   `json:"number"`
	Message *string `json:"message"`
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
	ID          json.Number     `json:"id"`
	ContentID   json.Number     `json:"contentId"`
	Title       string          `json:"title"`
	Type        string          `json:"type"`
	ContentType string          `json:"contentType"`
	SpaceKey    string          `json:"spaceKey"`
	Space       confluenceSpace `json:"space"`
	SpaceID     json.Number     `json:"spaceId"`
	Status      string          `json:"status"`
	Snapshot    string          `json:"snapshotToken"`
	Operation   string          `json:"operationPerformed"`
	Metadata    struct {
		SpaceID  json.Number `json:"spaceId"`
		SpaceKey string      `json:"spaceKey"`
	} `json:"metadata"`
	URL     string            `json:"url"`
	WebURL  string            `json:"webUrl"`
	Version confluenceVersion `json:"version"`
	Links   struct {
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
	if !decimalID.MatchString(ref.ID) {
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
	if !decimalID.MatchString(id) || strings.TrimSpace(page.Title) == "" {
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

type ConfluencePublishIntent struct {
	Site          string `json:"site"`
	SpaceID       string `json:"space_id"`
	PayloadSHA256 string `json:"payload_sha256"`
}

// PreparedConfluencePublish is a same-site, revision-bound update or a
// verified replay. PreparePublish does not modify the page.
type PreparedConfluencePublish struct {
	client      *Confluence
	replacement confluence.Replacement
	intent      ConfluencePublishIntent
	snapshot    string
	replay      provider.WriteResult
}

func (p PreparedConfluencePublish) Intent() ConfluencePublishIntent { return p.intent }

func (c *Confluence) PreparePublish(ctx context.Context, ref provider.Ref, draft provider.DocumentDraft, opts provider.WriteOptions) (PreparedConfluencePublish, error) {
	var zero PreparedConfluencePublish
	if err := c.checkRef(ref); err != nil {
		return zero, err
	}
	if opts.CheckTask != nil {
		return zero, provider.Unsupported(ref.Instance, "confluence replacement with a task precondition")
	}
	replacement, err := confluence.NewReplacement(confluence.PageURL(c.receiptSite, ref.ID), ref, draft, opts)
	if err != nil {
		return zero, err
	}
	if _, err := exec.LookPath("twg"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return zero, ErrToolAbsent
		}
		return zero, fmt.Errorf("find twg executable: %w", err)
	}
	page, spaceID, err := c.publishedPage(ctx, ref)
	if err != nil {
		// Only executable lookup above can select another backend.
		if errors.Is(err, ErrToolAbsent) {
			err = ErrFailure
		}
		return zero, fmt.Errorf("verify published confluence page through twg: %w", err)
	}
	replay, err := c.replay(ctx, replacement, page)
	if err != nil {
		if errors.Is(err, ErrToolAbsent) {
			err = fmt.Errorf("twg became unavailable during confluence replay: %w", ErrFailure)
		}
		return zero, err
	}
	snapshot := page.Snapshot
	if replay.Status != "" {
		snapshot = ""
	} else if strings.TrimSpace(snapshot) == "" || strings.ContainsRune(snapshot, '\x00') {
		return zero, fmt.Errorf("confluence page response omitted a usable snapshot token: %w", ErrFailure)
	}
	prepared := PreparedConfluencePublish{client: c, replacement: replacement, snapshot: snapshot, replay: replay}
	// The temporary path varies at dispatch. Bind the exact command and file
	// contents using a fixed placeholder for that local path.
	payload, err := json.Marshal(append(prepared.updateArgs(""), replacement.HTML))
	if err != nil {
		return zero, err
	}
	prepared.intent = ConfluencePublishIntent{Site: c.site, SpaceID: spaceID, PayloadSHA256: fmt.Sprintf("%x", sha256.Sum256(payload))}
	return prepared, nil
}

func (c *Confluence) publishedPage(ctx context.Context, ref provider.Ref) (confluencePage, string, error) {
	var page confluencePage
	args := []string{"confluence", "content", "get", ref.ID, "--detail", "full", "--format", "html", "--include-metadata"}
	if err := c.read(ctx, "read confluence page for publication", args, &page); err != nil {
		return confluencePage{}, "", err
	}
	if page.Status != "current" || page.Version.Number < 1 ||
		first(page.ContentType, page.Type) != "page" || (page.Type != "" && page.Type != "page") {
		return confluencePage{}, "", fmt.Errorf("confluence result lacks published page or version metadata: %w", ErrFailure)
	}
	if page.SpaceKey == "" {
		page.SpaceKey = page.Metadata.SpaceKey
	}
	spaceID := first(page.SpaceID.String(), page.Metadata.SpaceID.String())
	if !atlassian.ValidConfluenceID(spaceID) ||
		(page.Metadata.SpaceID != "" && page.Metadata.SpaceID.String() != spaceID) ||
		(page.Metadata.SpaceKey != "" && page.Metadata.SpaceKey != ref.Scope) {
		return confluencePage{}, "", fmt.Errorf("confluence result lacks matching space metadata: %w", ErrFailure)
	}
	doc, err := c.document(page, []string{ref.Scope})
	if err != nil {
		return confluencePage{}, "", err
	}
	if doc.Ref != ref {
		return confluencePage{}, "", fmt.Errorf("confluence result is outside the requested page or space: %w", ErrFailure)
	}
	return page, spaceID, nil
}

func (c *Confluence) replay(ctx context.Context, replacement confluence.Replacement, page confluencePage) (provider.WriteResult, error) {
	current := atlassian.PageVersion{Number: page.Version.Number}
	if page.Version.Message != nil {
		current.Message = *page.Version.Message
	}
	return replacement.Replay(current, page.Version.Message != nil, func(number int64) (atlassian.PageVersion, error) {
		return c.historicalVersion(ctx, replacement.Ref, number)
	})
}

func (c *Confluence) historicalVersion(ctx context.Context, ref provider.Ref, number int64) (atlassian.PageVersion, error) {
	cursor := ""
	seen := make(map[string]bool)
	for range 5 {
		var result struct {
			Results    []confluenceVersion `json:"results"`
			Versions   []confluenceVersion `json:"versions"`
			NextCursor string              `json:"nextCursor"`
			ContentID  json.Number         `json:"contentId"`
		}
		args := []string{"confluence", "content", "versions", "list", "--id", ref.ID, "--limit", "100"}
		if cursor != "" {
			args = append(args, "--cursor="+cursor)
		}
		if err := c.read(ctx, "read confluence version history", args, &result); err != nil {
			return atlassian.PageVersion{}, err
		}
		if (result.Results == nil) == (result.Versions == nil) ||
			(result.ContentID != "" && result.ContentID.String() != ref.ID) {
			return atlassian.PageVersion{}, fmt.Errorf("invalid confluence version history: %w", ErrFailure)
		}
		var receipt *string
		for _, version := range append(result.Results, result.Versions...) {
			if version.Number == number {
				if receipt != nil || version.Message == nil {
					return atlassian.PageVersion{}, fmt.Errorf("ambiguous confluence successor version: %w", ErrFailure)
				}
				receipt = version.Message
			}
		}
		if receipt != nil {
			return atlassian.PageVersion{Number: number, Message: *receipt}, nil
		}
		cursor = result.NextCursor
		if cursor == "" || seen[cursor] {
			break
		}
		seen[cursor] = true
	}
	return atlassian.PageVersion{}, fmt.Errorf("confluence history did not prove the requested version: %w", ErrFailure)
}

func (p PreparedConfluencePublish) updateArgs(bodyFile string) []string {
	return []string{"confluence", "content", "update", p.replacement.Ref.ID,
		"--snapshot-token=" + p.snapshot, "--title=" + p.replacement.Title, "--body-file", bodyFile,
		"--format", "html", "--ack-body-formats", "--version-message", p.replacement.Successor.Message, "--content-type", "page", "--yes"}
}

// Apply dispatches the approved replacement once per invocation. Reconcile an
// ambiguous result through PreparePublish before invoking another update.
func (p PreparedConfluencePublish) Apply(ctx context.Context) (provider.WriteResult, error) {
	var zero provider.WriteResult
	if p.replay.Status != "" {
		return p.replay, nil
	}
	if p.client == nil || p.snapshot == "" {
		return zero, fmt.Errorf("confluence publication was not prepared: %w", ErrFailure)
	}
	body, err := os.CreateTemp("", "agentflow-confluence-*.html")
	if err != nil {
		return zero, fmt.Errorf("create confluence body file: %w", err)
	}
	defer os.Remove(body.Name())
	_, err = body.WriteString(p.replacement.HTML)
	if err := errors.Join(err, body.Close()); err != nil {
		return zero, fmt.Errorf("write confluence body file: %w", err)
	}
	var ack confluencePage
	if err := p.client.read(ctx, "replace confluence page", p.updateArgs(body.Name()), &ack); err != nil {
		return zero, err
	}
	if err := p.checkAcknowledgement(ack); err != nil {
		return zero, err
	}
	page, spaceID, err := p.client.publishedPage(ctx, p.replacement.Ref)
	if err != nil || spaceID != p.intent.SpaceID {
		return zero, fmt.Errorf("cannot confirm the confluence update in the approved space: %w", errors.Join(ErrFailure, err))
	}
	status, err := p.client.replay(ctx, p.replacement, page)
	if err != nil || status.Status != provider.AlreadyApplied ||
		(page.Version.Number == p.replacement.Successor.Number && page.Title != p.replacement.Title) {
		return zero, fmt.Errorf("cannot confirm the requested confluence successor: %w", errors.Join(ErrFailure, err))
	}
	return p.replacement.Result(provider.Applied), nil
}

func (p PreparedConfluencePublish) checkAcknowledgement(ack confluencePage) error {
	switch strings.ToLower(ack.Status) {
	case "snapshot_stale":
		return fmt.Errorf("confluence snapshot is stale: %w", provider.ErrConflict)
	case "", "current", "success", "updated":
	default:
		return fmt.Errorf("confluence update reported an unconfirmed status: %w", ErrFailure)
	}
	id := first(ack.ID.String(), ack.ContentID.String())
	spaceID := p.intent.SpaceID
	scope := p.replacement.Ref.Scope
	if id != p.replacement.Ref.ID || (ack.ContentID != "" && ack.ContentID.String() != id) ||
		(ack.Title != "" && ack.Title != p.replacement.Title) ||
		(ack.SpaceID != "" && ack.SpaceID.String() != spaceID) ||
		(ack.Metadata.SpaceID != "" && ack.Metadata.SpaceID.String() != spaceID) ||
		(ack.SpaceKey != "" && ack.SpaceKey != scope) || (ack.Space.Key != "" && ack.Space.Key != scope) ||
		(ack.Metadata.SpaceKey != "" && ack.Metadata.SpaceKey != scope) ||
		(ack.ContentType != "" && ack.ContentType != "page") || (ack.Type != "" && ack.Type != "page") ||
		(ack.Operation != "" && !strings.EqualFold(ack.Operation, "updated")) ||
		(ack.Version.Number != 0 && ack.Version.Number != p.replacement.Successor.Number) ||
		(ack.Version.Message != nil && *ack.Version.Message != p.replacement.Successor.Message) {
		return fmt.Errorf("confluence update acknowledgement does not match the requested replacement: %w", ErrFailure)
	}
	for _, raw := range []string{ack.URL, ack.WebURL, ack.Links.WebUI} {
		if raw != "" {
			if _, err := p.client.sameSiteURL(raw); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Confluence) Publish(ctx context.Context, ref provider.Ref, draft provider.DocumentDraft, opts provider.WriteOptions) (provider.WriteResult, error) {
	publish, err := c.PreparePublish(ctx, ref, draft, opts)
	if err != nil {
		return provider.WriteResult{}, err
	}
	return publish.Apply(ctx)
}
