package tasktemplate

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/provider"
)

func TestProfessionalCatalog(t *testing.T) {
	f, err := os.Open("testdata/professional.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	catalog, err := DecodeCatalog(f)
	if err != nil {
		t.Fatal(err)
	}
	template, err := catalog.Select(Ref{Name: "professional", Revision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := template.CheckSupport([]string{"title"}); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("adapter support error = %v", err)
	}
	title := "Improve delivery reporting"
	description := "## Objective\nShow build outcomes.\n\n## Scope\nUpdate the dashboard.\n\n## Acceptance Criteria\nShow failed builds.\n\n## Verification\nCompare with a build log."
	draft, err := template.Resolve(Fields{Title: &title, Description: &description, Labels: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Title != title || draft.Description != description+"\n\n## Dependencies\nNone identified." ||
		draft.Priority != "normal" || draft.Labels == nil || len(draft.Labels) != 0 || draft.Links != nil {
		t.Fatalf("unexpected draft: %+v", draft)
	}
	if _, err := catalog.Select(Ref{Name: "professional", Revision: "001"}); err == nil {
		t.Fatal("resolved a nonmatching revision")
	}
}

func TestDecodeInvalidCatalog(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`null`, "must be a JSON array"},
		{`{} `, "decode template catalog"},
		{`[] []`, "unexpected JSON value"},
		{`[] x`, "read end of template catalog"},
		{`[{"name":"work","revision":1}]`, "cannot unmarshal number"},
		{`[{"name":"work"}]`, "requires a name and revision"},
		{`[{"name":"work","revision":"1","script":"echo foo"}]`, `unknown field "script"`},
		{`[{"name":"work","revision":"1","defaults":{"bogus":true}}]`, `unknown field "bogus"`},
		{`[{"name":"work","revision":"1","sections":[{"heading":"Objective","name":"oops"}]}]`, `unknown field "name"`},
		{`[{"name":"work","revision":"1"},{"name":"work","revision":"1"}]`, "duplicate template"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			catalog, err := DecodeCatalog(strings.NewReader(tc.input))
			if catalog != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("DecodeCatalog = %v, %v, want error containing %q", catalog, err, tc.want)
			}
		})
	}
}
