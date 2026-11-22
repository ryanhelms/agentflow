package legacy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nscaledev/devflow/internal/gh"
	"github.com/nscaledev/devflow/internal/provider"
)

// GitHubClient is the subset of the GitHub client used by GitHub.
type GitHubClient interface {
	PRForBranch(ctx context.Context, repo, branch string) (*gh.PR, error)
	CreatePR(ctx context.Context, repo, title, body, head, base string, draft bool) (*gh.PR, error)
	ReviewThreads(ctx context.Context, repo string, number int) ([]gh.Thread, error)
}

// GitHub adapts the legacy client for GitHub.com repositories.
type GitHub struct {
	client GitHubClient
}

func NewGitHub(client GitHubClient) *GitHub {
	return &GitHub{client: client}
}

var (
	_ GitHubClient         = (*gh.Client)(nil)
	_ provider.CodeHosting = (*GitHub)(nil)
)

var githubPath = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$`)

func validateGitHubRepo(repo provider.RepositoryRef) error {
	switch strings.ToLower(repo.Host) {
	case "github.com", "www.github.com", "ssh.github.com":
	default:
		return provider.Unsupported("github", fmt.Sprintf("use repository host %q", repo.Host))
	}
	_, name, _ := strings.Cut(repo.Path, "/")
	if !githubPath.MatchString(repo.Path) || name == "." || name == ".." {
		return fmt.Errorf("invalid github repository path %q: expected owner/name", repo.Path)
	}
	return nil
}

// FindChange looks for an open pull request with a head owned by the target
// repository's owner. The legacy branch lookup always scopes to that owner.
func (g *GitHub) FindChange(ctx context.Context, repo provider.RepositoryRef, head string) (provider.ChangeRequest, error) {
	if err := validateGitHubRepo(repo); err != nil {
		return provider.ChangeRequest{}, err
	}
	if owner, branch, qualified := strings.Cut(head, ":"); qualified {
		repoOwner, _, _ := strings.Cut(repo.Path, "/")
		if !strings.EqualFold(owner, repoOwner) {
			return provider.ChangeRequest{}, provider.Unsupported("github", "find a pull request by a head in another owner's fork")
		}
		head = branch
	}
	if strings.TrimSpace(head) == "" || strings.Contains(head, ":") {
		return provider.ChangeRequest{}, errors.New("github pull request lookup requires a valid head branch")
	}
	pr, err := g.client.PRForBranch(ctx, repo.Path, head)
	if err != nil {
		return provider.ChangeRequest{}, fmt.Errorf("find github pull request for %s: %w", head, err)
	}
	if pr == nil {
		return provider.ChangeRequest{}, fmt.Errorf("no open github pull request for %s: %w", head, provider.ErrNotFound)
	}
	return githubChange(repo, pr)
}

func (g *GitHub) OpenChange(ctx context.Context, repo provider.RepositoryRef, draft provider.ChangeDraft) (provider.ChangeRequest, error) {
	if err := validateGitHubRepo(repo); err != nil {
		return provider.ChangeRequest{}, err
	}
	pr, err := g.client.CreatePR(ctx, repo.Path, draft.Title, draft.Body, draft.Head, draft.Base, draft.Draft)
	if err != nil {
		return provider.ChangeRequest{}, fmt.Errorf("open github pull request: %w", err)
	}
	return githubChange(repo, pr)
}

func githubChange(repo provider.RepositoryRef, pr *gh.PR) (provider.ChangeRequest, error) {
	if pr == nil || pr.Number <= 0 {
		return provider.ChangeRequest{}, errors.New("github response did not identify a pull request")
	}
	return provider.ChangeRequest{
		Repository: repo, ID: strconv.Itoa(pr.Number), URL: pr.HTMLURL,
		Title: pr.Title, Head: pr.Head.Ref, Base: pr.Base.Ref, Draft: pr.Draft,
	}, nil
}

// Reviews returns unresolved review threads. The client exposes an outdated flag
// for each thread but no thread URL or submitted review decision.
func (g *GitHub) Reviews(ctx context.Context, change provider.ChangeRequest) ([]provider.Review, error) {
	if err := validateGitHubRepo(change.Repository); err != nil {
		return nil, err
	}
	number, err := strconv.Atoi(change.ID)
	if err != nil || number <= 0 {
		return nil, fmt.Errorf("invalid github pull request number %q", change.ID)
	}
	threads, err := g.client.ReviewThreads(ctx, change.Repository.Path, number)
	if err != nil {
		return nil, fmt.Errorf("get github reviews for pull request %d: %w", number, err)
	}
	reviews := make([]provider.Review, 0, len(threads))
	for _, thread := range threads {
		state := "unresolved"
		if thread.Outdated {
			state = "outdated"
		}
		reviews = append(reviews, provider.Review{ID: thread.ID, Author: thread.Author, Body: thread.Body, State: state})
	}
	return reviews, nil
}
