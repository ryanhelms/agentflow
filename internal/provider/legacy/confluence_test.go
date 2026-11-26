package legacy

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ryanhelms/agentflow/internal/atlassian"
	"github.com/ryanhelms/agentflow/internal/provider"
)

type confluenceSearch func(text, space string, limit int) ([]atlassian.Page, error)

func (f confluenceSearch) SearchConfluence(_ context.Context, text, space string, limit int) ([]atlassian.Page, error) {
	return f(text, space, limit)
}

func TestConfluenceFind(t *testing.T) {
	client := confluenceSearch(func(text, space string, limit int) ([]atlassian.Page, error) {
		if text != "delivery loop" || space != "ENG" || limit != 10 {
			t.Fatalf("search args = %q, %q, %d", text, space, limit)
		}
		return []atlassian.Page{{ID: "77", Title: "Design", Excerpt: "Search excerpt", Space: "ENG", URL: "https://agentflow.example/wiki/design"}}, nil
	})
	confluence := NewConfluence("wiki", client)
	want := []provider.Document{{
		Ref:   provider.Ref{Instance: "wiki", Scope: "ENG", ID: "77"},
		Title: "Design", Body: "Search excerpt", URL: "https://agentflow.example/wiki/design",
	}}
	got, err := confluence.Find(t.Context(), provider.DocumentQuery{Text: "delivery loop", Scope: "ENG"})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("find = %+v, %v", got, err)
	}
}

func TestConfluenceUnsupportedOperations(t *testing.T) {
	confluence := NewConfluence("wiki", nil)
	ref := provider.Ref{Instance: "wiki", Scope: "ENG", ID: "77"}
	if _, err := confluence.Get(t.Context(), ref); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("get error = %v", err)
	}
	if _, err := confluence.Publish(t.Context(), ref, provider.DocumentDraft{Title: "Design"}, provider.WriteOptions{}); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("publish error = %v", err)
	}
}
