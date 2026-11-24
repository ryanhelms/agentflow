package twg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"

	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/native"
)

// TWG converts these tokens into Jira mentions even in plain text mode.
var jiraMention = regexp.MustCompile(`(?i)\[~accountId:(?:[0-9a-f]{24}|[0-9]+:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\]`)

type PreparedJiraComment struct {
	client   *Jira
	profile  jiraProfile
	selector string
	ref      provider.Ref
	issueID  string
	body     string
}

// Intent describes the exact append and the saved login that will perform it.
func (p *PreparedJiraComment) Intent() map[string]string {
	intent := p.profile.intent(p.selector)
	maps.Copy(intent, map[string]string{"backend": "twg", "site": p.client.site, "project": p.ref.Scope,
		"key": p.ref.ID, "issue_id": p.issueID, "body_format": "plain", "comment": p.body})
	return intent
}

// PrepareComment resolves the approved key to its stable ID on the configured
// site. The app repeats this read when redeeming the approval.
func (j *Jira) PrepareComment(ctx context.Context, ref provider.Ref, body string) (*PreparedJiraComment, error) {
	ref, err := j.issueRef(ref)
	if err != nil {
		return nil, err
	}
	profile, selector, err := j.jiraProfile(ctx)
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
	if body == "" || !literalCLIValue(body) || jiraMention.MatchString(body) {
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
