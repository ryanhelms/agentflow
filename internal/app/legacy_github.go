package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/legacy"
)

// The legacy client sends authenticated requests to api.github.com. A forge
// driver mapping alone does not establish that a remote lives on GitHub.com.
func (a *App) githubRemotes(ctx context.Context) (gitx.Detection, error) {
	if p := a.Cfg.Providers.CodeHosting.Provider; p != "" && p != "auto" && p != "github" {
		return gitx.Detection{}, provider.Unsupported(p, "GitHub code hosting")
	}
	d, err := a.detectForge(ctx)
	if err != nil {
		return d, err
	}
	if !d.OK() {
		return d, fmt.Errorf("detect GitHub repository: %s", d.Message)
	}
	for _, remote := range []*gitx.Remote{d.Source, d.Target} {
		if remote.Forge != gitx.ForgeGitHub || !publicGitHubRemote(remote.URL) {
			return d, fmt.Errorf("remote %q is not a GitHub.com repository", remote.Name)
		}
	}
	return d, nil
}

func publicGitHubRemote(u gitx.RemoteURL) bool {
	repo := provider.RepositoryRef{Host: u.Host, Path: u.Path}
	return u.Project == "" && githubDotComPort(u) && legacy.ValidateGitHubRepo(repo) == nil
}

// GitHubRepo returns the validated code-hosting target as owner/name.
func (a *App) GitHubRepo(ctx context.Context) (string, error) {
	d, err := a.githubRemotes(ctx)
	if err != nil {
		return "", err
	}
	return d.Target.URL.Path, nil
}

func githubTarget(d gitx.Detection) provider.RepositoryRef {
	return provider.RepositoryRef{Host: "github.com", Path: d.Target.URL.Path}
}

func githubHead(d gitx.Detection, branch string) string {
	if d.Fork {
		return d.Source.URL.Owner + ":" + branch
	}
	return branch
}

func (a *App) pushURL(ctx context.Context, source *gitx.Remote) (string, error) {
	raw, err := a.Repo.Git(ctx, "remote", "get-url", "--push", "--all", "--", source.Name)
	if err != nil {
		return "", err
	}
	push, err := gitx.ParseRemoteURL(strings.TrimSuffix(raw, "\n"))
	if err != nil || !publicGitHubRemote(push) || !strings.EqualFold(push.Path, source.URL.Path) {
		return "", errors.New("the source remote must have one GitHub push destination matching its repository")
	}
	return strings.TrimSuffix(raw, "\n"), nil
}
