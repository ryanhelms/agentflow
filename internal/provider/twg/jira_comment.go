package twg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
)

// TWG converts these tokens into Jira mentions even in plain text mode.
var jiraMention = regexp.MustCompile(`(?i)\[~accountId:(?:[0-9a-f]{24}|[0-9]+:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\]`)

type commentProfile struct {
	ID, Name, Environment, AuthMethod, UserID string
	Site, Domain                              string
	Revision                                  int
	Sites                                     []struct{ Site string }
}

func (p commentProfile) coversSite(origin string) bool {
	host := strings.TrimPrefix(origin, "https://")
	slug, cloud := strings.CutSuffix(host, ".atlassian.net")
	matches := func(site string) bool {
		site = strings.ToLower(strings.TrimSuffix(site, "/"))
		return site == origin || site == host || cloud && site == slug
	}
	if matches(p.Site) || matches(p.Domain) {
		return true
	}
	for _, site := range p.Sites {
		if matches(site.Site) {
			return true
		}
	}
	return false
}

func (j *Jira) commentProfile(ctx context.Context) (commentProfile, string, error) {
	// The vendor command only returns profile identities, never their secrets.
	var catalog struct {
		Storage, ActiveEnvironment string
		Defaults                   map[string]string
		Profiles                   []commentProfile
	}
	if err := j.read(ctx, "select jira OAuth profile", []string{"auth", "profile", "list"}, &catalog); err != nil {
		return commentProfile{}, "", err
	}
	if catalog.Profiles == nil || catalog.ActiveEnvironment == "" {
		return commentProfile{}, "", fmt.Errorf("twg omitted its profile catalog: %w", ErrFailure)
	}
	defaultID := catalog.Defaults[catalog.ActiveEnvironment]
	var candidates []commentProfile
	for _, profile := range catalog.Profiles {
		if profile.Environment != catalog.ActiveEnvironment || profile.AuthMethod != "oauth" || !profile.coversSite(j.site) {
			continue
		}
		selector := profile.Name
		if catalog.Storage == "legacy" {
			// Before profile migration the CLI only accepts the default alias.
			if profile.ID != defaultID {
				continue
			}
			selector = "default"
		}
		if profile.ID == "" || selector == "" || profile.UserID == "" {
			return commentProfile{}, "", fmt.Errorf("twg OAuth profile omitted its login identity: %w", ErrFailure)
		}
		if profile.ID == defaultID {
			return profile, selector, nil
		}
		candidates = append(candidates, profile)
	}
	switch len(candidates) {
	case 0:
		return commentProfile{}, "", ErrAuthRequired
	case 1:
		return candidates[0], candidates[0].Name, nil
	default:
		return commentProfile{}, "", fmt.Errorf("multiple twg OAuth profiles cover the configured Jira site: %w", ErrAccessDenied)
	}
}

type PreparedJiraComment struct {
	client   *Jira
	profile  commentProfile
	selector string
	ref      provider.Ref
	issueID  string
	body     string
}

// Intent describes the exact append and the saved login that will perform it.
func (p *PreparedJiraComment) Intent() map[string]string {
	return map[string]string{"backend": "twg", "site": p.client.site, "project": p.ref.Scope, "key": p.ref.ID, "issue_id": p.issueID,
		"profile": p.profile.Name, "profile_id": p.profile.ID, "account_id": p.profile.UserID,
		"profile_selector": p.selector, "profile_revision": strconv.Itoa(p.profile.Revision), "body_format": "plain", "comment": p.body}
}

// PrepareComment resolves the approved key to its stable ID on the configured
// site. The app repeats this read when redeeming the approval.
func (j *Jira) PrepareComment(ctx context.Context, ref provider.Ref, body string) (*PreparedJiraComment, error) {
	ref, err := j.issueRef(ref)
	if err != nil {
		return nil, err
	}
	profile, selector, err := j.commentProfile(ctx)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID, Key, URL string
		Project      named
		Error        json.RawMessage
	}
	if err := j.read(ctx, "verify jira comment target", []string{"jira", "workitem", "get", ref.ID, "--fields", "project", "--profile", selector}, &rows); err != nil {
		// Only the initial lookup of the CLI can select REST.
		if errors.Is(err, ErrToolAbsent) {
			return nil, fmt.Errorf("twg disappeared during jira comment preparation: %w", ErrFailure)
		}
		return nil, err
	}
	if len(rows) != 1 {
		return nil, fmt.Errorf("expected one jira comment target: %w", ErrFailure)
	}
	row := rows[0]
	if present(row.Error) {
		return nil, classify(row.Error)
	}
	if row.Key != ref.ID || row.Project.Key != ref.Scope || !decimalID.MatchString(row.ID) {
		return nil, fmt.Errorf("jira comment target differs from the requested issue or project: %w", ErrAccessDenied)
	}
	if row.URL != "" {
		u, err := j.sameSiteURL(row.URL)
		if err != nil || u.EscapedPath() != "/browse/"+ref.ID {
			return nil, fmt.Errorf("jira comment target has a mismatched issue url: %w", ErrAccessDenied)
		}
	}
	// The vendor CLI trims the body and expands mention markup before writing.
	if body == "" || !utf8.ValidString(body) || strings.ContainsRune(body, 0) || jiraMention.MatchString(body) ||
		strings.TrimFunc(body, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }) != body {
		return nil, errors.New("twg requires literal comment text without surrounding whitespace, NUL or Jira mention markup")
	}
	return &PreparedJiraComment{client: j, profile: profile, selector: selector, ref: ref, issueID: row.ID, body: body}, nil
}

func (p *PreparedJiraComment) Apply(ctx context.Context) (string, error) {
	args := []string{"jira", "workitem", "comment", "create", "--issue-id", p.issueID, "--body=" + p.body, "--body-format", "plain", "--profile", p.selector}
	var reply envelope
	started, err := p.client.run(ctx, "create jira comment", args, &reply)
	if !started {
		return "", errors.Join(native.ErrWriteNotDispatched, err)
	}
	if err == nil {
		var id string
		id, err = p.commentID(reply)
		if err == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("inspect the issue before approving another comment: %w", errors.Join(native.ErrWriteUncertain, err))
}

func (p *PreparedJiraComment) commentID(reply envelope) (string, error) {
	var request struct{ Site, IssueID string }
	if reply.Command != "jira.workitem.comment.create" || json.Unmarshal(reply.Request, &request) != nil ||
		request.Site != p.client.site || request.IssueID != p.issueID {
		return "", fmt.Errorf("jira comment acknowledgement has a missing or mismatched target: %w", ErrFailure)
	}
	var comment struct {
		ID     string
		Author struct{ AccountID string }
		Body   struct {
			Type    string
			Version int
			Content []struct {
				Type    string
				Content []struct{ Type, Text string }
			}
		}
	}
	if json.Unmarshal(reply.Data, &comment) != nil || !decimalID.MatchString(comment.ID) ||
		(p.profile.UserID != "" && comment.Author.AccountID != p.profile.UserID) ||
		comment.Body.Type != "doc" || comment.Body.Version != 1 || len(comment.Body.Content) != 1 {
		return "", fmt.Errorf("incomplete or mismatched jira comment receipt: %w", ErrFailure)
	}
	paragraph := comment.Body.Content[0]
	if paragraph.Type != "paragraph" || len(paragraph.Content) != 1 || paragraph.Content[0].Type != "text" || paragraph.Content[0].Text != p.body {
		return "", fmt.Errorf("jira did not acknowledge the approved plain text: %w", ErrFailure)
	}
	return comment.ID, nil
}
