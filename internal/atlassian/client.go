// Package atlassian is a small Jira Cloud (REST v3) and Confluence Cloud (REST v2) client.
package atlassian

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nscaledev/devflow/internal/auth"
)

// Client talks to one Atlassian site with the developer's credential.
type Client struct {
	Base string
	cred auth.Atlassian
	http *http.Client
}

// New returns a client for site using cred.
func New(cred auth.Atlassian) *Client {
	return &Client{
		Base: strings.TrimRight(cred.Site, "/"),
		cred: cred,
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

// APIError is a non-2xx response.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("atlassian: HTTP %d: %s", e.Status, e.Body)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.cred.Bearer {
			req.Header.Set("Authorization", "Bearer "+c.cred.Token)
		} else {
			req.SetBasicAuth(c.cred.Email, c.cred.Token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			wait := time.Duration(1<<attempt) * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
				wait = time.Duration(s) * time.Second
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
			if in != nil {
				b, _ := json.Marshal(in)
				body = bytes.NewReader(b)
			}
			continue
		}
		if resp.StatusCode >= 300 {
			msg := string(data)
			if len(msg) > 500 {
				msg = msg[:500]
			}
			return &APIError{Status: resp.StatusCode, Body: msg}
		}
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
}

// ---- Jira ----

// User is a Jira user.
type User struct {
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
}

// Status is a Jira status with its category key (new, indeterminate, done).
type Status struct {
	Name     string `json:"name"`
	Category struct {
		Key string `json:"key"`
	} `json:"statusCategory"`
}

// Issue is the subset of a Jira issue devflow uses.
type Issue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
		Status      Status          `json:"status"`
		IssueType   struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Priority *struct {
			Name string `json:"name"`
		} `json:"priority"`
		Assignee   *User    `json:"assignee"`
		Labels     []string `json:"labels"`
		Components []struct {
			Name string `json:"name"`
		} `json:"components"`
		Parent *struct {
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
			} `json:"fields"`
		} `json:"parent"`
		Updated string `json:"updated"`
	} `json:"fields"`
}

// DescriptionText renders the ADF description as plain text.
func (i *Issue) DescriptionText() string {
	return ADFText(i.Fields.Description)
}

const issueFields = "summary,description,status,issuetype,priority,assignee,labels,components,parent,updated"

// Myself returns the authenticated user.
func (c *Client) Myself(ctx context.Context) (User, error) {
	var u User
	err := c.do(ctx, "GET", "/rest/api/3/myself", nil, &u)
	return u, err
}

// GetIssue fetches an issue.
func (c *Client) GetIssue(ctx context.Context, key string) (*Issue, error) {
	var i Issue
	err := c.do(ctx, "GET", "/rest/api/3/issue/"+url.PathEscape(key)+"?fields="+issueFields, nil, &i)
	return &i, err
}

// Search runs JQL via the enhanced search endpoint.
func (c *Client) Search(ctx context.Context, jql string, max int) ([]Issue, error) {
	var out struct {
		Issues []Issue `json:"issues"`
	}
	in := map[string]any{"jql": jql, "maxResults": max, "fields": strings.Split(issueFields, ",")}
	err := c.do(ctx, "POST", "/rest/api/3/search/jql", in, &out)
	return out.Issues, err
}

// Transition is an available workflow transition.
type Transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   Status `json:"to"`
}

// Transitions lists transitions available from the issue's current status.
func (c *Client) Transitions(ctx context.Context, key string) ([]Transition, error) {
	var out struct {
		Transitions []Transition `json:"transitions"`
	}
	err := c.do(ctx, "GET", "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions", nil, &out)
	return out.Transitions, err
}

// DoTransition applies a transition by ID.
func (c *Client) DoTransition(ctx context.Context, key, id string) error {
	in := map[string]any{"transition": map[string]string{"id": id}}
	return c.do(ctx, "POST", "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions", in, nil)
}

// AddComment posts a plain-text comment.
func (c *Client) AddComment(ctx context.Context, key, text string) error {
	return c.do(ctx, "POST", "/rest/api/3/issue/"+url.PathEscape(key)+"/comment", map[string]any{"body": ADFDoc(text)}, nil)
}

// Assign assigns an issue to an account.
func (c *Client) Assign(ctx context.Context, key, accountID string) error {
	return c.do(ctx, "PUT", "/rest/api/3/issue/"+url.PathEscape(key)+"/assignee", map[string]string{"accountId": accountID}, nil)
}

// CreateIssue creates an issue and returns its key.
func (c *Client) CreateIssue(ctx context.Context, project, issueType, summary, description string, labels []string, linkTo string) (string, error) {
	fields := map[string]any{
		"project":     map[string]string{"key": project},
		"issuetype":   map[string]string{"name": issueType},
		"summary":     summary,
		"description": ADFDoc(description),
	}
	if len(labels) > 0 {
		fields["labels"] = labels
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := c.do(ctx, "POST", "/rest/api/3/issue", map[string]any{"fields": fields}, &out); err != nil {
		return "", err
	}
	if linkTo != "" {
		link := map[string]any{
			"type":         map[string]string{"name": "Relates"},
			"inwardIssue":  map[string]string{"key": out.Key},
			"outwardIssue": map[string]string{"key": linkTo},
		}
		if err := c.do(ctx, "POST", "/rest/api/3/issueLink", link, nil); err != nil {
			return out.Key, fmt.Errorf("created %s but could not link it: %w", out.Key, err)
		}
	}
	return out.Key, nil
}

// LogWork adds a worklog, e.g. "1h 30m".
func (c *Client) LogWork(ctx context.Context, key, spent, comment string) error {
	in := map[string]any{"timeSpent": spent}
	if comment != "" {
		in["comment"] = ADFDoc(comment)
	}
	return c.do(ctx, "POST", "/rest/api/3/issue/"+url.PathEscape(key)+"/worklog", in, nil)
}

// ChangelogEntry is one status change.
type ChangelogEntry struct {
	Created string
	Author  string
	From    string
	To      string
}

// StatusChanges returns status transitions on an issue.
func (c *Client) StatusChanges(ctx context.Context, key string) ([]ChangelogEntry, error) {
	var out struct {
		Values []struct {
			Created string `json:"created"`
			Author  User   `json:"author"`
			Items   []struct {
				Field      string `json:"field"`
				FromString string `json:"fromString"`
				ToString   string `json:"toString"`
			} `json:"items"`
		} `json:"values"`
	}
	if err := c.do(ctx, "GET", "/rest/api/3/issue/"+url.PathEscape(key)+"/changelog?maxResults=100", nil, &out); err != nil {
		return nil, err
	}
	var entries []ChangelogEntry
	for _, v := range out.Values {
		for _, it := range v.Items {
			if it.Field == "status" {
				entries = append(entries, ChangelogEntry{Created: v.Created, Author: v.Author.DisplayName, From: it.FromString, To: it.ToString})
			}
		}
	}
	return entries, nil
}

// ---- Confluence ----

// Page is a Confluence search hit.
type Page struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Excerpt  string `json:"excerpt"`
	Space    string `json:"space"`
	Revision string `json:"revision,omitempty"`
}

// SearchConfluence runs a CQL text search over pages.
func (c *Client) SearchConfluence(ctx context.Context, text, space string, limit int) ([]Page, error) {
	cql := fmt.Sprintf(`type=page AND text ~ %q`, text)
	if space != "" {
		cql += fmt.Sprintf(` AND space = %q`, space)
	}
	var out struct {
		Results []struct {
			Content struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Space struct {
					Key string `json:"key"`
				} `json:"space"`
				Version PageVersion `json:"version"`
			} `json:"content"`
			Excerpt string `json:"excerpt"`
			URL     string `json:"url"`
		} `json:"results"`
	}
	q := url.Values{"cql": {cql}, "limit": {strconv.Itoa(limit)}, "expand": {"content.space,content.version"}}
	if err := c.do(ctx, "GET", "/wiki/rest/api/search?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	var pages []Page
	for _, r := range out.Results {
		var revision string
		if r.Content.Version.Number > 0 {
			revision = strconv.FormatInt(r.Content.Version.Number, 10)
		}
		pages = append(pages, Page{Revision: revision, ID: r.Content.ID, Title: r.Content.Title, URL: c.Base + "/wiki" + r.URL,
			Excerpt: cleanExcerpt(r.Excerpt), Space: r.Content.Space.Key})
	}
	return pages, nil
}

func cleanExcerpt(s string) string {
	r := strings.NewReplacer("@@@hl@@@", "", "@@@endhl@@@", "", "\n", " ")
	s = r.Replace(s)
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}

// RemoteLinks returns web links on an issue (used to find design docs).
func (c *Client) RemoteLinks(ctx context.Context, key string) ([]Page, error) {
	var out []struct {
		Object struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"object"`
	}
	if err := c.do(ctx, "GET", "/rest/api/3/issue/"+url.PathEscape(key)+"/remotelink", nil, &out); err != nil {
		return nil, err
	}
	var pages []Page
	for _, l := range out {
		pages = append(pages, Page{Title: l.Object.Title, URL: l.Object.URL})
	}
	return pages, nil
}
