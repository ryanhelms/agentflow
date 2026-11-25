package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/filesystem"
	"github.com/nscaledev/devflow/internal/provider/githubissues"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
	"github.com/nscaledev/devflow/internal/provider/repository"
	"github.com/nscaledev/devflow/internal/provider/twg"
)

func (a *App) addTaskDocuments(ctx context.Context, tc *TicketContext, task provider.Task) error {
	links := task.Links
	if a.usesIssues() {
		var warnings []string
		links, warnings = githubissues.RelatedKnowledge(task.Description)
		tc.Warnings = append(tc.Warnings, warnings...)
	}
	for _, link := range links {
		ref := link.Ref
		if ref == nil || (a.usesAsana() && ref.Instance == "asana") {
			continue
		}
		page := atlassian.Page{ID: ref.String(), Title: link.Title, URL: link.URL, Space: ref.Scope}
		doc, err := a.DocsGet(ctx, *ref)
		if err != nil {
			var request *native.Request
			if errors.As(err, &request) {
				return err
			}
			if a.usesIssues() && errors.Is(err, provider.ErrNotFound) {
				tc.Warnings = append(tc.Warnings, fmt.Sprintf("document %s not found", ref))
			} else {
				tc.Warnings = append(tc.Warnings, fmt.Sprintf("document %s: %v", ref, err))
			}
			if a.usesIssues() {
				continue
			}
		} else {
			page.Title = doc.Title
			page.Revision = doc.Revision
			if doc.URL != "" {
				page.URL = doc.URL
			}
		}
		if page.Title == "" {
			page.Title = ref.ID
		}
		tc.Docs = append(tc.Docs, page)
	}
	return nil
}

// DocsGet reads one document by its full identity from the selected provider.
func (a *App) DocsGet(ctx context.Context, ref provider.Ref) (provider.Document, error) {
	ctx = a.standaloneReadContext(ctx)
	var zero provider.Document
	if err := ref.Validate(); err != nil {
		return zero, err
	}
	name := a.Cfg.Providers.KnowledgeBase.Provider
	if ref.Instance != name {
		return zero, fmt.Errorf("selected knowledge provider is %q", name)
	}
	var knowledge provider.Knowledge
	var err error
	switch name {
	case "confluence":
		site, err := a.confluenceSite()
		if err != nil {
			return zero, err
		}
		legacyRead := func() (provider.Document, error) {
			store, err := a.confluenceKnowledge(site)
			if err != nil {
				return zero, err
			}
			return store.Get(ctx, ref)
		}
		if !strings.HasPrefix(site, "https://") {
			return legacyRead()
		}
		read, err := nativemap.ConfluencePage(ref, site)
		if err != nil {
			return zero, err
		}
		return mappedNativeRead(ctx, a.Repo.Dir, "atlassian", read, func(ctx context.Context) (provider.Document, error) {
			if !useAtlassianCLI(ctx, site, "confluence") {
				return legacyRead()
			}
			cli, err := twg.NewConfluence(ref.Instance, site, []string{ref.Scope})
			if err != nil {
				return zero, err
			}
			doc, err := cli.Get(ctx, ref)
			if errors.Is(err, twg.ErrToolAbsent) {
				return legacyRead()
			}
			return doc, atlCLIError("confluence", err)
		})
	case "filesystem":
		store, err := filesystem.NewOKFCompatibleKnowledge(name, a.knowledgeDirectory())
		if err != nil {
			return zero, err
		}
		defer store.Close()
		knowledge = store
	case "repository":
		cfg, err := a.knowledgeRepository(ctx)
		if err != nil {
			return zero, err
		}
		knowledge, err = repository.NewOKFCompatibleKnowledge(cfg)
		if err != nil {
			return zero, err
		}
	case "github-wiki":
		knowledge, err = a.knowledgeWiki(ctx, nil)
		if err != nil {
			return zero, err
		}
	default:
		return zero, provider.Unsupported(name, "document lookup by reference")
	}
	return knowledge.Get(ctx, ref)
}
