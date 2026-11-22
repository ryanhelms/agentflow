package app

import (
	"context"
	"fmt"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/filesystem"
	"github.com/nscaledev/devflow/internal/provider/repository"
)

func (a *App) addAsanaDocuments(ctx context.Context, tc *TicketContext, links []provider.Link) {
	for _, link := range links {
		ref := link.Ref
		if ref == nil || ref.Instance == "asana" {
			continue
		}
		page := atlassian.Page{ID: ref.String(), Title: link.Title, URL: link.URL, Space: ref.Scope}
		doc, err := a.linkedKnowledgeDocument(ctx, *ref)
		if err != nil {
			tc.Warnings = append(tc.Warnings, fmt.Sprintf("document %s: %v", ref, err))
		} else {
			page.Title = doc.Title
			if doc.URL != "" {
				page.URL = doc.URL
			}
		}
		if page.Title == "" {
			page.Title = ref.ID
		}
		tc.Docs = append(tc.Docs, page)
	}
}

func (a *App) linkedKnowledgeDocument(ctx context.Context, ref provider.Ref) (provider.Document, error) {
	var zero provider.Document
	name := a.Cfg.Providers.KnowledgeBase.Provider
	if ref.Instance != name {
		return zero, fmt.Errorf("selected knowledge provider is %q", name)
	}
	var knowledge provider.Knowledge
	var err error
	switch name {
	case "filesystem":
		store, err := filesystem.NewKnowledge(name, a.knowledgeDirectory())
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
		knowledge, err = repository.NewKnowledge(cfg)
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
