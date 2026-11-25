package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/gh"
	"github.com/nscaledev/agentflow/internal/gitx"
	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/githubwiki"
	"github.com/nscaledev/agentflow/internal/provider/repository"
)

func (a *App) knowledgeWiki(ctx context.Context, beforeWrite func(base, target string) error) (*githubwiki.Knowledge, error) {
	cfg := a.Cfg.Providers.KnowledgeBase.Settings.(*config.GitHubWikiSettings)
	slug := ""
	if cfg.Repository != nil {
		slug = *cfg.Repository
	} else {
		// Resolve the same target that forge detection selects for GitHub Issues.
		d, err := a.detectForge(ctx)
		if err != nil {
			return nil, fmt.Errorf("detect the GitHub wiki repository: %w", err)
		}
		if !d.OK() || d.Forge != gitx.ForgeGitHub || !strings.HasPrefix(d.Target.ID(), "github.com/") ||
			d.Target.URL.Project != "" || !githubDotComPort(d.Target.URL) {
			return nil, errors.New("no GitHub.com repository detected for the wiki; configure knowledge_base.settings.repository with owner/name")
		}
		slug = d.Target.URL.Path
	}
	k, err := githubwiki.NewKnowledge(githubwiki.Config{
		Instance: "github-wiki", Repository: slug, BeforeWrite: beforeWrite,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve GitHub wiki destination (knowledge_base.settings.repository): %w", err)
	}
	return k, nil
}

func githubDotComPort(remote gitx.RemoteURL) bool {
	standard := map[string]string{"https": "443", "http": "80", "ssh": "22", "git": "9418"}
	sshOverHTTPS := remote.Host == "ssh.github.com" && remote.Transport == "ssh" && remote.Port == "443"
	return remote.Port == "" || remote.Port == standard[remote.Transport] || sshOverHTTPS
}

type repositoryPublisher struct {
	client *gh.Client
	origin string
	repo   string
	kind   string
	ref    provider.Ref
}

func (a *App) repositoryPublisher(ctx context.Context, origin, kind string, ref provider.Ref) (*repositoryPublisher, error) {
	remote, err := gitx.ParseRemoteURL(origin)
	if err != nil {
		return nil, fmt.Errorf("GitHub pull-request publication requires a hosted GitHub origin: %w", err)
	}
	// The shared client authenticates to api.github.com.
	switch remote.Host {
	case "github.com", "www.github.com", "ssh.github.com":
	default:
		return nil, fmt.Errorf("no GitHub pull-request publisher for host %q", remote.Host)
	}
	if !githubDotComPort(remote) {
		return nil, fmt.Errorf("no GitHub pull-request publisher for %s:%s", remote.Host, remote.Port)
	}
	if !githubSlug.MatchString(remote.Path) || remote.Project != "" {
		return nil, errors.New("GitHub pull-request publication requires an owner/repository origin")
	}
	c, err := a.GitHub(ctx)
	if err != nil {
		return nil, err
	}
	account, err := c.SignedInCLIAccount(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticate GitHub pull-request publication: %w", err)
	}
	if account == nil && c.Token == "" {
		return nil, errors.New("GitHub authentication is required for pull-request publication")
	}
	return &repositoryPublisher{client: c, origin: origin, repo: remote.Path, kind: kind, ref: ref}, nil
}

func (p *repositoryPublisher) EnsurePullRequest(ctx context.Context, pub repository.Publication) (string, error) {
	if err := pub.Ref.Validate(); err != nil {
		return "", err
	}
	// Task creation allocates its ID in the store. All other writes bind the exact reference.
	if pub.Origin != p.origin || pub.Ref.Instance != p.ref.Instance || pub.Ref.Scope != p.ref.Scope ||
		(p.ref.ID != "" && pub.Ref.ID != p.ref.ID) {
		return "", errors.New("publication does not match the configured repository and reference")
	}
	if found, err := p.findPullRequest(ctx, pub); err != nil || found != "" {
		return found, err
	}
	title := "docs: publish " + pub.Ref.ID
	if p.kind == "task" {
		title = "chore: publish task " + pub.Ref.ID
	}
	pr, err := p.client.CreatePR(ctx, p.repo, title, "Publish "+p.kind+" "+pub.Ref.String()+".", pub.Head, pub.Base, false)
	if err == nil && pr.HTMLURL != "" {
		return pr.HTMLURL, nil
	}
	if err == nil {
		err = errors.New("GitHub returned a publication PR without a URL")
	}
	// A concurrent caller or a lost response may have created the PR.
	found, lookupErr := p.findPullRequest(ctx, pub)
	if found != "" {
		return found, nil
	}
	return "", errors.Join(err, lookupErr)
}

func (p *repositoryPublisher) findPullRequest(ctx context.Context, pub repository.Publication) (string, error) {
	owner, _, _ := strings.Cut(p.repo, "/")
	q := url.Values{"state": {"all"}, "head": {owner + ":" + pub.Head}, "base": {pub.Base}}
	var prs []struct {
		gh.PR
		Head struct {
			Ref  string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
	}
	if err := p.client.Do(ctx, "GET", "/repos/"+p.repo+"/pulls?"+q.Encode(), nil, &prs); err != nil {
		return "", err
	}
	for _, pr := range prs {
		if pr.Head.Ref != pub.Head || pr.Base.Ref != pub.Base || !strings.EqualFold(pr.Head.Repo.FullName, p.repo) {
			continue
		}
		if pr.State != "open" {
			return "", fmt.Errorf("publication PR already closed at %s: %w", pr.HTMLURL, provider.ErrConflict)
		}
		if pr.HTMLURL == "" {
			return "", errors.New("GitHub returned a publication PR without a URL")
		}
		return pr.HTMLURL, nil
	}
	return "", nil
}
