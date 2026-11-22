package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/filesystem"
	"github.com/nscaledev/devflow/internal/provider/repository"
)

var githubSlug = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$`)

func (a *App) knowledgeRepository(ctx context.Context) (repository.Config, error) {
	s := a.Cfg.Providers.KnowledgeBase.Settings.(*config.RepositorySettings)
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
	Links            []provider.Link `json:"links,omitempty" jsonschema:"links for the replacement document"`
	OperationID      string          `json:"operation_id" jsonschema:"stable operation ID to reuse on retry"`
	ExpectedRevision string          `json:"expected_revision,omitempty" jsonschema:"revision returned by search, omit only for a new document"`
}

func (a *App) DocsPublish(ctx context.Context, in DocsPublishInput, token string) (any, error) {
	name := a.Cfg.Providers.KnowledgeBase.Provider
	if name != "filesystem" && name != "repository" {
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
	if name == "filesystem" {
		root := a.knowledgeDirectory()
		summary["directory"] = root
		if p, err := a.gate(policy.Local, "docs_publish", summary, token, false, nil); p != nil || err != nil {
			return p, err
		}
		k, err := filesystem.NewKnowledge(name, root)
		if err != nil {
			a.audit(policy.Local, "docs_publish", summary, err)
			return nil, err
		}
		defer k.Close()
		out, err := in.publish(ctx, k)
		a.audit(policy.Local, "docs_publish", summary, err)
		return out, err
	}

	cfg, err := a.knowledgeRepository(ctx)
	if err != nil {
		return nil, err
	}
	summary["origin"], summary["directory"], summary["publication"] = cfg.Origin, cfg.Directory, cfg.Publication
	if cfg.Publication == "pull-request" {
		cfg.Publisher, err = a.docsPublisher(ctx, cfg.Origin, in.Ref)
		if err != nil {
			return nil, err
		}
	}
	// The adapter resolves remote HEAD and the operation's branch together.
	// Bind the token to those same branches, including on confirmation replay.
	var pending *Pending
	errPending := errors.New("document publication pending approval")
	cfg.BeforeWrite = func(base, target string) error {
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
	k, err := repository.NewKnowledge(cfg)
	if err != nil {
		return nil, err
	}
	out, err := in.publish(ctx, k)
	if errors.Is(err, errPending) {
		return pending, nil
	}
	a.audit(policy.Remote, "docs_publish", summary, err)
	return out, err
}

func (in DocsPublishInput) publish(ctx context.Context, knowledge provider.Knowledge) (provider.WriteResult, error) {
	draft := provider.DocumentDraft{Title: in.Title, Body: in.Body, Links: in.Links}
	opts := provider.WriteOptions{OperationID: in.OperationID, ExpectedRevision: in.ExpectedRevision}
	return knowledge.Publish(ctx, in.Ref, draft, opts)
}
