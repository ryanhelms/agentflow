package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/filesystem"
	"github.com/ryanhelms/agentflow/internal/provider/repository"
)

var githubSlug = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$`)

func (a *App) knowledgeRepository(ctx context.Context) (repository.Config, error) {
	s := a.Cfg.Providers.KnowledgeBase.Settings.(*config.RepositoryKnowledgeSettings)
	cfg := repository.Config{Instance: "repository", Directory: s.Directory, Publication: s.Publication}
	if cfg.Publication == "" {
		cfg.Publication = "pull-request"
	}
	if s.Branch != nil {
		cfg.Branch = *s.Branch
	}
	if s.Repository != nil {
		cfg.Origin = *s.Repository
	}
	if cfg.Origin == "" {
		raw, err := a.Repo.Git(ctx, "config", "--get", "remote.origin.url")
		if err != nil {
			return cfg, fmt.Errorf("read knowledge repository origin: %w", err)
		}
		cfg.Origin = strings.TrimSpace(raw)
	} else if githubSlug.MatchString(cfg.Origin) {
		cfg.Origin = "https://github.com/" + cfg.Origin
	}
	if _, err := gitx.ParseRemoteURL(cfg.Origin); errors.Is(err, gitx.ErrLocalRemote) && !filepath.IsAbs(cfg.Origin) && !strings.HasPrefix(cfg.Origin, "file://") {
		cfg.Origin = filepath.Join(a.Repo.Dir, cfg.Origin)
	}
	// The origin appears in the approval summary and may appear in Git errors.
	if strings.Contains(cfg.Origin, "://") {
		u, err := url.Parse(cfg.Origin)
		if err != nil || u.RawQuery != "" || u.Fragment != "" || (u.User != nil && u.Scheme != "ssh") {
			return cfg, errors.New("use a knowledge repository URL without embedded credentials, query or fragment")
		}
		if _, ok := u.User.Password(); ok {
			return cfg, errors.New("use a knowledge repository URL without a password")
		}
	}
	return cfg, nil
}

// DocsPublishInput identifies a single create or revision-checked replacement.
// OperationID is caller-supplied so a retry can reconcile the same write.
type DocsPublishInput struct {
	Ref              provider.Ref    `json:"ref" jsonschema:"qualified destination with instance, scope and id"`
	Title            string          `json:"title"`
	Body             string          `json:"body" jsonschema:"Markdown document body"`
	Type             string          `json:"type,omitempty" jsonschema:"OKF concept type (defaults to Document on create)"`
	Description      string          `json:"description,omitempty" jsonschema:"OKF concept description"`
	Tags             []string        `json:"tags,omitzero" jsonschema:"OKF concept tags (omit to preserve or supply an empty list to clear)"`
	Links            []provider.Link `json:"links,omitzero" jsonschema:"links for the replacement document"`
	OperationID      string          `json:"operation_id" jsonschema:"stable operation ID to reuse on retry"`
	ExpectedRevision string          `json:"expected_revision,omitempty" jsonschema:"revision returned by search, omit only for a new document"`
}

func (a *App) DocsPublish(ctx context.Context, in DocsPublishInput, token string) (any, error) {
	name := a.Cfg.Providers.KnowledgeBase.Provider
	switch name {
	case "filesystem", "repository":
	case "github-wiki", "confluence":
		if in.Type != "" || in.Description != "" || in.Tags != nil {
			return nil, provider.Unsupported(name, "document type, description and tags")
		}
	default:
		return nil, provider.Unsupported(name, "document publish")
	}
	if err := in.Ref.Validate(); err != nil {
		return nil, err
	}
	if in.Ref.Instance != name {
		return nil, fmt.Errorf("reference instance %q does not match knowledge provider %q", in.Ref.Instance, name)
	}
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.OperationID) == "" {
		return nil, errors.New("document title and stable operation ID are required")
	}

	summary := map[string]any{"document": in}
	if name == "confluence" {
		return a.publishConfluence(ctx, in, summary, token)
	}
	if name == "filesystem" {
		root := a.knowledgeDirectory()
		format := a.Cfg.Providers.KnowledgeBase.Settings.(*config.FileSystemKnowledgeSettings).Format
		summary["directory"], summary["format"] = root, format
		if p, err := a.gate(policy.Local, "docs_publish", summary, token, false, nil); p != nil || err != nil {
			return p, err
		}
		k, err := filesystem.NewOKFKnowledge(name, root, format)
		if err != nil {
			a.audit(policy.Local, "docs_publish", summary, err)
			return nil, err
		}
		defer k.Close()
		out, err := in.publish(ctx, k)
		a.audit(policy.Local, "docs_publish", summary, err)
		return out, err
	}

	// The adapter resolves remote HEAD and the operation's branch together.
	// Bind the token to those same branches, including on confirmation replay.
	var pending *Pending
	errPending := errors.New("document publication pending approval")
	beforeWrite := func(base, target string) error {
		summary["base"], summary["target"] = base, target
		if policy.IsProtected(target, a.Cfg.Policy.ProtectedBranches) {
			return fmt.Errorf("pushing to protected branch %q is forbidden", target)
		}
		var gateErr error
		pending, gateErr = a.gate(policy.Remote, "docs_publish", summary, token, false, nil)
		if pending != nil {
			return errPending
		}
		return gateErr
	}

	var knowledge provider.Knowledge
	if name == "github-wiki" {
		wiki, err := a.knowledgeWiki(ctx, beforeWrite)
		if err != nil {
			return nil, err
		}
		summary["origin"], summary["directory"] = wiki.Destination()
		summary["publication"] = "push"
		knowledge = wiki
	} else {
		cfg, err := a.knowledgeRepository(ctx)
		if err != nil {
			return nil, err
		}
		summary["origin"], summary["directory"], summary["publication"] = cfg.Origin, cfg.Directory, cfg.Publication
		if cfg.Publication == "pull-request" {
			cfg.Publisher, err = a.repositoryPublisher(ctx, cfg.Origin, "document", in.Ref)
			if err != nil {
				return nil, err
			}
		}
		cfg.BeforeWrite = beforeWrite
		format := a.Cfg.Providers.KnowledgeBase.Settings.(*config.RepositoryKnowledgeSettings).Format
		summary["format"] = format
		knowledge, err = repository.NewOKFKnowledge(cfg, format)
		if err != nil {
			return nil, err
		}
	}
	out, err := in.publish(ctx, knowledge)
	if errors.Is(err, errPending) {
		return pending, nil
	}
	a.audit(policy.Remote, "docs_publish", summary, err)
	return out, err
}

func (in DocsPublishInput) publish(ctx context.Context, knowledge provider.Knowledge) (provider.WriteResult, error) {
	draft, opts := in.replacement()
	return knowledge.Publish(ctx, in.Ref, draft, opts)
}

func (in DocsPublishInput) replacement() (provider.DocumentDraft, provider.WriteOptions) {
	draft := provider.DocumentDraft{
		Title: in.Title, Body: in.Body, Links: in.Links,
		Type: in.Type, Description: in.Description, Tags: in.Tags,
	}
	return draft, provider.WriteOptions{OperationID: in.OperationID, ExpectedRevision: in.ExpectedRevision}
}
