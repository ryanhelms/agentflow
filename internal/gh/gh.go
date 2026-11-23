// Package gh is a small GitHub REST and GraphQL client.
package gh

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const requestTimeout = 20 * time.Second

// Client calls GitHub through HTTP or the GitHub CLI.
type Client struct {
	Token string
	API   string
	http  *http.Client
	cli   *cliBackend
}

// New returns a client for token.
func New(token string) *Client {
	return &Client{Token: token, API: "https://api.github.com", http: &http.Client{Timeout: requestTimeout}}
}

// Do performs a REST call. path may include a query string.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	_, err := c.do(ctx, method, path, in, out, nil)
	return err
}

func (c *Client) do(ctx context.Context, method, path string, in, out any, hdr map[string]string) (http.Header, error) {
	var payload []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	headers := http.Header{}
	headers.Set("Accept", "application/vnd.github+json")
	headers.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.cli == nil && c.Token != "" {
		headers.Set("Authorization", "Bearer "+c.Token)
	}
	for k, v := range hdr {
		headers.Set(k, v)
	}
	if c.cli != nil {
		return c.cli.do(ctx, method, path, payload, out, headers)
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.API+path, body)
	if err != nil {
		return nil, err
	}
	req.Header = headers
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.Header, decodeResponse(method, path, resp.StatusCode, resp.Header, data, out)
}

// ErrNotModified is returned for a 304 on conditional requests.
var ErrNotModified = fmt.Errorf("not modified")

// GraphQL runs a GraphQL query.
func (c *Client) GraphQL(ctx context.Context, query string, vars map[string]any, out any) error {
	var resp graphQLResponse
	if err := c.Do(ctx, "POST", "/graphql", map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		return &resp.Errors[0]
	}
	err := json.Unmarshal(resp.Data, out)
	if err != nil && c.cli != nil {
		return cliOutcomeError(http.MethodPost, err)
	}
	return err
}

// File fetches a file's content at ref, with an optional ETag for caching.
// It returns the content, the new ETag, and ErrNotModified when unchanged.
func (c *Client) File(ctx context.Context, repo, ref, path, etag string) ([]byte, string, error) {
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	hdr := map[string]string{}
	if etag != "" {
		hdr["If-None-Match"] = etag
	}
	h, err := c.do(ctx, "GET", "/repos/"+repo+"/contents/"+path+"?ref="+url.QueryEscape(ref), nil, &out, hdr)
	if err != nil {
		return nil, "", err
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	return b, h.Get("ETag"), err
}

// PR is a pull request summary.
type PR struct {
	NodeID  string `json:"node_id"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	MergedAt string `json:"merged_at"`
}

// PRForBranch returns the open PR whose head is branch, if any.
func (c *Client) PRForBranch(ctx context.Context, repo, branch string) (*PR, error) {
	prs, err := c.pullsForBranch(ctx, repo, branch)
	if err != nil || len(prs) == 0 {
		return nil, err
	}
	return &prs[0], nil
}

// UniquePRForBranch refuses to select one of multiple possible mutation targets.
func (c *Client) UniquePRForBranch(ctx context.Context, repo, branch string) (*PR, error) {
	prs, err := c.pullsForBranch(ctx, repo, branch)
	if err != nil || len(prs) == 0 {
		return nil, err
	}
	if len(prs) > 1 {
		return nil, fmt.Errorf("multiple open pull requests use head %q in %s", branch, repo)
	}
	return &prs[0], nil
}

func (c *Client) pullsForBranch(ctx context.Context, repo, branch string) ([]PR, error) {
	if !strings.Contains(branch, ":") {
		owner, _, _ := strings.Cut(repo, "/")
		branch = owner + ":" + branch
	}
	var prs []PR
	err := c.Do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls?state=open&head=%s", repo, url.QueryEscape(branch)), nil, &prs)
	return prs, err
}

// OpenPRs lists open PRs in a repo (first 100).
func (c *Client) OpenPRs(ctx context.Context, repo string) ([]PR, error) {
	var prs []PR
	err := c.Do(ctx, "GET", "/repos/"+repo+"/pulls?state=open&per_page=100", nil, &prs)
	return prs, err
}

// PRFiles lists files changed by a PR.
func (c *Client) PRFiles(ctx context.Context, repo string, number int) ([]string, error) {
	var files []struct {
		Filename string `json:"filename"`
	}
	if err := c.Do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100", repo, number), nil, &files); err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		out = append(out, f.Filename)
	}
	return out, nil
}

// CreatePR opens a pull request.
func (c *Client) CreatePR(ctx context.Context, repo, title, body, head, base string, draft bool) (*PR, error) {
	var pr PR
	in := map[string]any{"title": title, "body": body, "head": head, "base": base, "draft": draft}
	err := c.Do(ctx, "POST", "/repos/"+repo+"/pulls", in, &pr)
	return &pr, err
}

// MarkReady promotes one GitHub draft PR and checks the revision returned by GitHub.
// The GraphQL mutation accepts a PR ID but no expected head revision.
func (c *Client) MarkReady(ctx context.Context, nodeID, sha string) error {
	const mutation = `mutation($id:ID!){markPullRequestReadyForReview(input:{pullRequestId:$id}){pullRequest{id isDraft headRefOid}}}`
	var result struct {
		MarkPullRequestReadyForReview struct {
			PullRequest struct {
				ID         string
				IsDraft    bool
				HeadRefOid string
			}
		}
	}
	if err := c.GraphQL(ctx, mutation, map[string]any{"id": nodeID}, &result); err != nil {
		return err
	}
	pr := result.MarkPullRequestReadyForReview.PullRequest
	if pr.ID != nodeID || pr.IsDraft || pr.HeadRefOid != sha {
		return fmt.Errorf("GitHub ready-for-review result differed from the approved pull request or revision: %w", ErrWriteOutcomeUnknown)
	}
	return nil
}

// RequestReviewers requests reviews from users and teams.
func (c *Client) RequestReviewers(ctx context.Context, repo string, number int, users, teams []string) error {
	if len(users)+len(teams) == 0 {
		return nil
	}
	in := map[string]any{"reviewers": users, "team_reviewers": teams}
	return c.Do(ctx, "POST", fmt.Sprintf("/repos/%s/pulls/%d/requested_reviewers", repo, number), in, nil)
}

// AddLabels adds labels to an issue or PR.
func (c *Client) AddLabels(ctx context.Context, repo string, number int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	return c.Do(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/labels", repo, number), map[string]any{"labels": labels}, nil)
}

// Thread is an unresolved review thread.
type Thread struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Author   string `json:"author"`
	Body     string `json:"body"`
	Outdated bool   `json:"outdated"`
}

// ReviewThreads returns unresolved review threads on a PR.
func (c *Client) ReviewThreads(ctx context.Context, repo string, number int) ([]Thread, error) {
	owner, name, _ := strings.Cut(repo, "/")
	const q = `query($owner:String!,$name:String!,$n:Int!){repository(owner:$owner,name:$name){pullRequest(number:$n){
	  reviewThreads(first:100){nodes{id isResolved isOutdated path line comments(first:1){nodes{body author{login}}}}}}}}`
	var out struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					Nodes []struct {
						ID         string `json:"id"`
						IsResolved bool   `json:"isResolved"`
						IsOutdated bool   `json:"isOutdated"`
						Path       string `json:"path"`
						Line       int    `json:"line"`
						Comments   struct {
							Nodes []struct {
								Body   string `json:"body"`
								Author struct {
									Login string `json:"login"`
								} `json:"author"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := c.GraphQL(ctx, q, map[string]any{"owner": owner, "name": name, "n": number}, &out); err != nil {
		return nil, err
	}
	var threads []Thread
	for _, n := range out.Repository.PullRequest.ReviewThreads.Nodes {
		if n.IsResolved || len(n.Comments.Nodes) == 0 {
			continue
		}
		cm := n.Comments.Nodes[0]
		threads = append(threads, Thread{ID: n.ID, Path: n.Path, Line: n.Line, Author: cm.Author.Login, Body: cm.Body, Outdated: n.IsOutdated})
	}
	return threads, nil
}

// ThreadTarget resolves the repository and pull request of a GitHub thread node.
func (c *Client) ThreadTarget(ctx context.Context, threadID string) (string, int, error) {
	const query = `query($id:ID!){node(id:$id){... on PullRequestReviewThread{pullRequest{number repository{nameWithOwner}}}}}`
	var out struct {
		Node struct {
			PullRequest struct {
				Number     int
				Repository struct{ NameWithOwner string }
			}
		}
	}
	err := c.GraphQL(ctx, query, map[string]any{"id": threadID}, &out)
	return out.Node.PullRequest.Repository.NameWithOwner, out.Node.PullRequest.Number, err
}

// ReplyToThread posts a reply on a review thread.
func (c *Client) ReplyToThread(ctx context.Context, threadID, body string) error {
	const m = `mutation($id:ID!,$body:String!){addPullRequestReviewThreadReply(input:{pullRequestReviewThreadId:$id,body:$body}){comment{id}}}`
	var out map[string]any
	return c.GraphQL(ctx, m, map[string]any{"id": threadID, "body": body}, &out)
}

// CheckRun is a CI check on a commit.
type CheckRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// Checks lists check runs for a ref.
func (c *Client) Checks(ctx context.Context, repo, ref string) ([]CheckRun, error) {
	var out struct {
		CheckRuns []CheckRun `json:"check_runs"`
	}
	err := c.Do(ctx, "GET", "/repos/"+repo+"/commits/"+url.PathEscape(ref)+"/check-runs?per_page=100", nil, &out)
	return out.CheckRuns, err
}

// Deployment is a GitHub deployment with its latest status.
type Deployment struct {
	Environment string `json:"environment"`
	Ref         string `json:"ref"`
	SHA         string `json:"sha"`
	CreatedAt   string `json:"created_at"`
	State       string `json:"state"`
	ID          int64  `json:"id"`
}

// Deployments lists recent deployments with their latest state.
func (c *Client) Deployments(ctx context.Context, repo string) ([]Deployment, error) {
	var deps []Deployment
	if err := c.Do(ctx, "GET", "/repos/"+repo+"/deployments?per_page=20", nil, &deps); err != nil {
		return nil, err
	}
	for i := range deps {
		var st []struct {
			State string `json:"state"`
		}
		if err := c.Do(ctx, "GET", fmt.Sprintf("/repos/%s/deployments/%d/statuses?per_page=1", repo, deps[i].ID), nil, &st); err == nil && len(st) > 0 {
			deps[i].State = st[0].State
		}
	}
	return deps, nil
}

// SearchPRs searches pull requests (e.g. "org:nscaledev is:open PLAT-123").
func (c *Client) SearchPRs(ctx context.Context, q string) ([]PR, error) {
	var out struct {
		Items []struct {
			Number        int    `json:"number"`
			Title         string `json:"title"`
			State         string `json:"state"`
			HTMLURL       string `json:"html_url"`
			RepositoryURL string `json:"repository_url"`
			User          struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"items"`
	}
	if err := c.Do(ctx, "GET", "/search/issues?per_page=30&q="+url.QueryEscape(q+" is:pr"), nil, &out); err != nil {
		return nil, err
	}
	var prs []PR
	for _, it := range out.Items {
		pr := PR{Number: it.Number, Title: it.Title, State: it.State, HTMLURL: it.HTMLURL}
		pr.User.Login = it.User.Login
		prs = append(prs, pr)
	}
	return prs, nil
}

type Account struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

func (c *Client) account(ctx context.Context) (Account, error) {
	var account Account
	err := c.Do(ctx, "GET", "/user", nil, &account)
	return account, err
}

// SignedInCLIAccount returns the account observed by gh, or nil for direct API clients.
func (c *Client) SignedInCLIAccount(ctx context.Context) (*Account, error) {
	if c.cli == nil {
		return nil, nil
	}
	account, err := c.account(ctx)
	if err != nil {
		return nil, fmt.Errorf("verify signed-in gh account: %w", err)
	}
	if account.ID <= 0 || strings.TrimSpace(account.Login) == "" {
		return nil, fmt.Errorf("github did not identify the signed-in gh account")
	}
	return &account, nil
}

// Viewer returns the authenticated login.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	account, err := c.account(ctx)
	return account.Login, err
}
