package legacy

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/nscaledev/devflow/internal/gh"
	"github.com/nscaledev/devflow/internal/provider"
)

type githubMock struct {
	find    func(repo, branch string) (*gh.PR, error)
	create  func(repo, title, body, head, base string, draft bool) (*gh.PR, error)
	reviews func(repo string, number int) ([]gh.Thread, error)
}

func (m githubMock) PRForBranch(_ context.Context, repo, branch string) (*gh.PR, error) {
	return m.find(repo, branch)
}

func (m githubMock) CreatePR(_ context.Context, repo, title, body, head, base string, draft bool) (*gh.PR, error) {
	return m.create(repo, title, body, head, base, draft)
}

func (m githubMock) ReviewThreads(_ context.Context, repo string, number int) ([]gh.Thread, error) {
	return m.reviews(repo, number)
}

func TestGitHubChanges(t *testing.T) {
	repo := provider.RepositoryRef{Host: "github.com", Path: "org/project"}
	draft := provider.ChangeDraft{Title: "Add adapters", Body: "Description", Head: "fork:feature/adapters", Base: "main", Draft: true}
	pr := &gh.PR{Number: 17, Title: draft.Title, HTMLURL: "https://github.com/org/project/pull/17", Draft: true}
	pr.Head.Ref, pr.Base.Ref = "feature/adapters", draft.Base
	mock := &githubMock{
		find: func(path, branch string) (*gh.PR, error) {
			if path != repo.Path || branch != "org:"+pr.Head.Ref {
				t.Fatalf("find args = %q, %q", path, branch)
			}
			return pr, nil
		},
		create: func(path, title, body, head, base string, isDraft bool) (*gh.PR, error) {
			if path != repo.Path || title != draft.Title || body != draft.Body || head != draft.Head || base != draft.Base || !isDraft {
				t.Fatalf("create args = %q, %q, %q, %q, %q, %t", path, title, body, head, base, isDraft)
			}
			return pr, nil
		},
		reviews: func(path string, number int) ([]gh.Thread, error) {
			if path != repo.Path || number != 17 {
				t.Fatalf("review args = %q, %d", path, number)
			}
			return []gh.Thread{
				{ID: "thread-1", Author: "reviewer", Body: "Can we simplify this?"},
				{ID: "thread-2", Author: "reviewer", Body: "Moved in a later revision", Outdated: true},
			}, nil
		},
	}
	github := NewGitHub(mock)
	want := provider.ChangeRequest{Repository: repo, ID: "17", Title: pr.Title, URL: pr.HTMLURL, Head: pr.Head.Ref, Base: pr.Base.Ref, Draft: true}
	if got, err := github.OpenChange(t.Context(), repo, draft); err != nil || got != want {
		t.Fatalf("open = %+v, %v", got, err)
	}
	if got, err := github.FindChange(t.Context(), repo, "org:"+pr.Head.Ref); err != nil || got != want {
		t.Fatalf("find = %+v, %v", got, err)
	}
	wantReviews := []provider.Review{
		{ID: "thread-1", Author: "reviewer", Body: "Can we simplify this?", State: "unresolved"},
		{ID: "thread-2", Author: "reviewer", Body: "Moved in a later revision", State: "outdated"},
	}
	if got, err := github.Reviews(t.Context(), want); err != nil || !slices.Equal(got, wantReviews) {
		t.Fatalf("reviews = %+v, %v", got, err)
	}
	mock.find = func(_, _ string) (*gh.PR, error) { return nil, nil }
	if _, err := github.FindChange(t.Context(), repo, pr.Head.Ref); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("missing change returned %v", err)
	}
}

func TestGitHubRejectsUnsupportedRefs(t *testing.T) {
	github := NewGitHub(nil)
	repo := provider.RepositoryRef{Host: "github.com", Path: "org/project"}
	if _, err := github.FindChange(t.Context(), repo, "another-owner:"); err == nil {
		t.Fatalf("fork lookup error = %v", err)
	}
	if _, err := github.Reviews(t.Context(), provider.ChangeRequest{Repository: repo, ID: "-17"}); err == nil {
		t.Fatal("accepted invalid pull request number")
	}
	repo.Host = "gitlab.com"
	if _, err := github.OpenChange(t.Context(), repo, provider.ChangeDraft{}); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("foreign host error = %v", err)
	}
}
