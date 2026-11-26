package githubdevops

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ryanhelms/agentflow/internal/provider"
)

type githubRelease struct {
	Tag        string `json:"tag_name"`
	Title      string `json:"name"`
	Notes      string `json:"body"`
	URL        string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name        string `json:"name"`
		DownloadURL string `json:"browser_download_url"`
		Size        int64  `json:"size"`
		Digest      string `json:"digest"`
	} `json:"assets"`
}

// GetRelease reads a published release by tag. Revision is the commit the tag
// points to, including for annotated tags.
func (a *Adapter) GetRelease(ctx context.Context, repo provider.RepositoryRef, tag string) (provider.Release, error) {
	base, ref, err := releaseLocation(repo, tag)
	if err != nil {
		return provider.Release{}, err
	}
	remote, err := a.releaseByTag(ctx, base, ref.ID)
	if err != nil {
		return provider.Release{}, err
	}
	if remote.Draft {
		return provider.Release{}, fmt.Errorf("github release %q is a draft: %w", tag, provider.ErrNotFound)
	}
	sha, err := a.tagCommit(ctx, base, tag)
	if err != nil {
		return provider.Release{}, err
	}
	release := provider.Release{Tag: remote.Tag, Revision: sha, Title: remote.Title, Notes: remote.Notes, URL: remote.URL}
	for _, asset := range remote.Assets {
		release.Assets = append(release.Assets, provider.ReleaseAsset(asset))
	}
	return release, nil
}

// PublishRelease creates a stable release. The repository and tag provide the
// operation identity across retries. A replay must match the title, notes and
// any requested revision. Pass a commit SHA to pin the target across retries.
// GitHub's create API has no revision precondition.
func (a *Adapter) PublishRelease(ctx context.Context, repo provider.RepositoryRef, release provider.Release, options provider.WriteOptions) (provider.WriteResult, error) {
	if options.ExpectedRevision != "" {
		return provider.WriteResult{}, provider.Unsupported(instance, "publish release with an expected revision")
	}
	base, ref, err := releaseLocation(repo, release.Tag)
	if err != nil {
		return provider.WriteResult{}, err
	}
	if release.Revision != "" {
		release.Revision, err = a.commitSHA(ctx, base, release.Revision)
		if err != nil {
			return provider.WriteResult{}, err
		}
	}
	remote, err := a.releaseByTag(ctx, base, release.Tag)
	if err == nil {
		return a.verifyRelease(ctx, base, ref, remote, release, provider.AlreadyApplied)
	}
	if !errors.Is(err, provider.ErrNotFound) {
		return provider.WriteResult{}, err
	}
	if release.Revision != "" {
		// GitHub ignores target_commitish if the tag already exists.
		sha, err := a.tagCommit(ctx, base, release.Tag)
		switch {
		case err == nil && sha != release.Revision:
			return provider.WriteResult{}, tagConflict(release.Tag, sha, release.Revision)
		case err != nil && !errors.Is(err, provider.ErrNotFound):
			return provider.WriteResult{}, err
		}
	}

	input := struct {
		Tag        string `json:"tag_name"`
		Target     string `json:"target_commitish,omitempty"`
		Title      string `json:"name"`
		Notes      string `json:"body"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}{Tag: release.Tag, Target: release.Revision, Title: release.Title, Notes: release.Notes}
	var created githubRelease
	if err := a.client.Do(ctx, http.MethodPost, base+"/releases", input, &created); err != nil {
		return a.reconcileRelease(ctx, base, ref, release, err)
	}
	if created.Tag == "" {
		return a.reconcileRelease(ctx, base, ref, release, errors.New("github returned no release tag after creation"))
	}
	return a.verifyRelease(ctx, base, ref, created, release, provider.Applied)
}

func (a *Adapter) reconcileRelease(ctx context.Context, base string, ref provider.Ref, release provider.Release, writeErr error) (provider.WriteResult, error) {
	writeErr = fmt.Errorf("publish github release %q: %w", release.Tag, writeErr)
	remote, err := a.releaseByTag(ctx, base, release.Tag)
	if errors.Is(err, provider.ErrNotFound) {
		return provider.WriteResult{}, writeErr
	}
	if err != nil {
		return provider.WriteResult{}, errors.Join(writeErr, err)
	}
	result, err := a.verifyRelease(ctx, base, ref, remote, release, provider.AlreadyApplied)
	if err != nil {
		return provider.WriteResult{}, errors.Join(writeErr, err)
	}
	return result, nil
}

func (a *Adapter) verifyRelease(ctx context.Context, base string, ref provider.Ref, remote githubRelease, release provider.Release, status provider.WriteStatus) (provider.WriteResult, error) {
	if remote.Tag != release.Tag || remote.Draft || remote.Prerelease || remote.Title != release.Title || remote.Notes != release.Notes {
		return provider.WriteResult{}, fmt.Errorf("github release %q does not match requested stable release metadata: %w", release.Tag, provider.ErrConflict)
	}
	sha, err := a.tagCommit(ctx, base, release.Tag)
	if err != nil {
		return provider.WriteResult{}, err
	}
	if release.Revision != "" && sha != release.Revision {
		return provider.WriteResult{}, tagConflict(release.Tag, sha, release.Revision)
	}
	return provider.WriteResult{Ref: ref, Status: status, Revision: sha, URL: remote.URL}, nil
}

func tagConflict(tag, got, want string) error {
	return fmt.Errorf("github tag %q points to %s, expected %s: %w", tag, got, want, provider.ErrConflict)
}

func releaseLocation(repo provider.RepositoryRef, tag string) (string, provider.Ref, error) {
	base, err := repositoryPath(repo)
	if err != nil {
		return "", provider.Ref{}, err
	}
	ref := provider.Ref{Instance: instance, Scope: repo.Path, ID: tag}
	if err := ref.Validate(); err != nil {
		return "", provider.Ref{}, fmt.Errorf("invalid github release reference: %w", err)
	}
	if !validSegment(tag) {
		return "", provider.Ref{}, fmt.Errorf("invalid github release tag %q", tag)
	}
	return base, ref, nil
}

func (a *Adapter) releaseByTag(ctx context.Context, base, tag string) (githubRelease, error) {
	path := base + "/releases/tags/" + url.PathEscape(tag)
	var remote githubRelease
	if err := a.client.Do(ctx, http.MethodGet, path, nil, &remote); err != nil {
		return githubRelease{}, fmt.Errorf("read github release %q: %w", tag, githubReadError(err, path))
	}
	if remote.Tag != tag {
		return githubRelease{}, fmt.Errorf("github returned tag %q for release %q", remote.Tag, tag)
	}
	return remote, nil
}

func (a *Adapter) tagCommit(ctx context.Context, base, tag string) (string, error) {
	// The commit API returns 422 for a missing ref. Read the tag ref first to
	// distinguish an absent tag from a failed lookup before creating a release.
	path := base + "/git/ref/tags/" + url.PathEscape(tag)
	var ref struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if err := a.client.Do(ctx, http.MethodGet, path, nil, &ref); err != nil {
		return "", fmt.Errorf("read github tag %q: %w", tag, githubReadError(err, path))
	}
	if sha, err := hex.DecodeString(ref.Object.SHA); err != nil || len(sha) != 20 {
		return "", fmt.Errorf("github tag %q has an invalid object sha", tag)
	}
	switch ref.Object.Type {
	case "commit":
		return ref.Object.SHA, nil
	case "tag":
		return a.commitSHA(ctx, base, "refs/tags/"+tag)
	default:
		return "", fmt.Errorf("github tag %q points to a %s object, expected a commit", tag, ref.Object.Type)
	}
}

func (a *Adapter) commitSHA(ctx context.Context, base, revision string) (string, error) {
	if !validSegment(revision) {
		return "", fmt.Errorf("invalid github revision %q", revision)
	}
	path := base + "/commits/" + url.PathEscape(revision)
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := a.client.Do(ctx, http.MethodGet, path, nil, &commit); err != nil {
		return "", fmt.Errorf("resolve github revision %q: %w", revision, githubReadError(err, path))
	}
	if commit.SHA == "" {
		return "", fmt.Errorf("github returned an empty commit sha for %q", revision)
	}
	if hash, err := hex.DecodeString(revision); err == nil && len(hash) == 20 && !strings.EqualFold(commit.SHA, revision) {
		return "", fmt.Errorf("github resolved commit %q to %q: %w", revision, commit.SHA, provider.ErrConflict)
	}
	return commit.SHA, nil
}
