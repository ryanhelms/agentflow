// Package confluence shares the published-page revision and replay contract
// across the REST, Rovo MCP and Teamwork Graph CLI adapters.
package confluence

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/ryanhelms/agentflow/internal/atlassian"
	"github.com/ryanhelms/agentflow/internal/provider"
)

// ReplacementVersion checks the options supported by every publish backend.
func ReplacementVersion(ref provider.Ref, revision string, links []provider.Link) (int64, error) {
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

// Replacement binds a complete literal-text replacement to one page's
// successor. Its version message is portable across the three backends.
type Replacement struct {
	Ref       provider.Ref
	URL       string
	Title     string
	HTML      string
	Expected  int64
	Successor atlassian.PageVersion
}

func PageURL(site, id string) string {
	return strings.TrimRight(site, "/") + "/wiki/pages/viewpage.action?pageId=" + id
}

func NewReplacement(pageURL string, ref provider.Ref, draft provider.DocumentDraft, opts provider.WriteOptions) (Replacement, error) {
	if err := ref.Validate(); err != nil {
		return Replacement{}, err
	}
	expected, err := ReplacementVersion(ref, opts.ExpectedRevision, draft.Links)
	if err != nil {
		return Replacement{}, err
	}
	if strings.TrimSpace(opts.OperationID) == "" || strings.TrimSpace(draft.Title) == "" {
		return Replacement{}, errors.New("document title and stable operation ID are required")
	}
	// Preserve the REST receipt format, including field order and the original
	// body. The fixed JSON sequence keeps the fields unambiguous.
	payload, err := json.Marshal([]string{pageURL, ref.String(), opts.ExpectedRevision, opts.OperationID, draft.Title, draft.Body})
	if err != nil {
		return Replacement{}, err
	}
	return Replacement{
		Ref: ref, URL: pageURL, Title: draft.Title, HTML: atlassian.ConfluenceLiteralHTML(draft.Body),
		Expected: expected, Successor: atlassian.PageVersion{
			Number: expected + 1, Message: fmt.Sprintf("agentflow docs publish %x", sha256.Sum256(payload)),
		},
	}, nil
}

// Replay checks the exact successor, even if the current edit is newer. A
// zero result with no error means the expected revision is still current.
func (r Replacement) Replay(current atlassian.PageVersion, hasCurrentMessage bool, history func(int64) (atlassian.PageVersion, error)) (provider.WriteResult, error) {
	if current.Number == r.Expected {
		return provider.WriteResult{}, nil
	}
	receipt := current
	if current.Number > r.Successor.Number || (current.Number == r.Successor.Number && !hasCurrentMessage) {
		var err error
		receipt, err = history(r.Successor.Number)
		if err != nil {
			return provider.WriteResult{}, fmt.Errorf("cannot verify earlier Confluence version %d: %w", r.Successor.Number, err)
		}
	}
	if receipt != r.Successor {
		return provider.WriteResult{}, fmt.Errorf("%w: expected Confluence revision %d, found %d", provider.ErrConflict, r.Expected, current.Number)
	}
	return r.Result(provider.AlreadyApplied), nil
}

func (r Replacement) Result(status provider.WriteStatus) provider.WriteResult {
	return provider.WriteResult{Ref: r.Ref, URL: r.URL, Revision: strconv.FormatInt(r.Successor.Number, 10), Status: status}
}
