package app

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/policy"
	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/devopsfixture"
	"github.com/nscaledev/agentflow/internal/provider/githubdevops"
)

// ReleaseGet looks up a published release in the selected release provider.
func (a *App) ReleaseGet(ctx context.Context, tag string) (provider.Release, error) {
	selection := a.Cfg.Providers.DevOps.Releases
	var releases provider.ReleaseReader
	var repo provider.RepositoryRef
	switch selection.Provider {
	case "", "github":
		var err error
		repo, err = a.devOpsGitHubRepository(ctx, "devops.releases", selection)
		if err != nil {
			return provider.Release{}, err
		}
		client, err := a.GitHub(ctx)
		if err != nil {
			return provider.Release{}, err
		}
		releases = githubdevops.New(client)
	case "fixture":
		settings := selection.Settings.(*config.DevOpsFixtureSettings)
		fixture, err := devopsfixture.LoadReleases(a.Repo.Dir, settings.Path)
		if err != nil {
			return provider.Release{}, err
		}
		releases = fixture
	default:
		return provider.Release{}, provider.Unsupported(selection.Provider, "release lookup")
	}
	return releases.GetRelease(ctx, repo, tag)
}

type ReleasePublishInput struct {
	Tag      string
	Revision string
	Title    string
	Notes    string
	DryRun   bool
}

// ReleasePublish previews or creates a stable release at an explicit commit.
// Retries use the repository and tag as their operation identity.
func (a *App) ReleasePublish(ctx context.Context, in ReleasePublishInput, token string) (any, error) {
	selection := a.Cfg.Providers.DevOps.Releases
	if selection.Provider != "" && selection.Provider != "github" {
		return nil, provider.Unsupported(selection.Provider, "release publication")
	}
	if _, err := a.Repo.Git(ctx, "check-ref-format", "refs/tags/"+in.Tag); err != nil {
		return nil, fmt.Errorf("invalid release tag %q: %w", in.Tag, err)
	}
	if hash, err := hex.DecodeString(in.Revision); err != nil || len(hash) != 20 {
		return nil, errors.New("release publication requires an explicit full commit sha via --revision")
	}
	if in.Title == "" {
		in.Title = in.Tag
	} else if strings.TrimSpace(in.Title) == "" {
		return nil, errors.New("release title cannot be only whitespace")
	}
	repo, err := a.devOpsGitHubRepository(ctx, "devops.releases", selection)
	if err != nil {
		return nil, err
	}
	release := provider.Release{Tag: in.Tag, Revision: strings.ToLower(in.Revision), Title: in.Title, Notes: in.Notes}
	summary := struct {
		Repository string           `json:"repository"`
		Release    provider.Release `json:"release"`
	}{Repository: repo.Host + "/" + repo.Path, Release: release}
	if in.DryRun {
		return summary, nil
	}
	const action = "release_publish"
	if p, err := a.gate(policy.Remote, action, summary, token, true, nil); p != nil || err != nil {
		return p, err
	}
	client, err := a.GitHub(ctx)
	if err != nil {
		a.audit(policy.Remote, action, summary, err)
		return nil, err
	}
	var releases provider.Releases = githubdevops.New(client)
	out, err := releases.PublishRelease(ctx, repo, release, provider.WriteOptions{})
	a.audit(policy.Remote, action, summary, err)
	return out, err
}
