package tasktemplate

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/provider"
)

func TestResolvePortableRevision(t *testing.T) {
	var catalog Catalog
	err := json.Unmarshal([]byte(`[
		{"name":"bug","revision":"1","defaults":{"scope":"OPS","title":"Bug","type":"bug","priority":"low","labels":["triage"]},
		 "required":["scope","title","type","description","links"],
		 "sections":[{"heading":"Impact","body":"Investigate"},{"heading":"Evidence","body":"Collect logs"}],
		 "checklist":["Reproduce","Verify"]},
		{"name":"bug","revision":"2","required":["priority"]}
	]`), &catalog)
	if err != nil {
		t.Fatal(err)
	}
	ref := Ref{Name: "bug", Revision: "1"}
	template, err := catalog.Select(ref)
	if err != nil {
		t.Fatal(err)
	}
	description := "Report\r\n\r\n## Impact\r\nEdited by author\r\n- [x] Reproduce"
	title, blank := "Disk full", ""
	target := provider.Ref{Instance: "work", Scope: "OPS", ID: "42"}
	inputs := Fields{Title: &title, Description: &description, Priority: &blank,
		Labels: []string{}, Links: []provider.Link{{Ref: &target}}}
	want := provider.TaskDraft{Scope: "OPS", Title: title, Type: "bug", Labels: []string{},
		Description: description + "\n\n## Evidence\nCollect logs\n\n- [ ] Verify", Links: inputs.Links}

	for range 2 {
		got, err := template.Resolve(inputs)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("resolve = %#v, %v, want %#v", got, err, want)
		}
		// A caller can edit the resolved links independently of the input.
		got.Links[0].Ref.ID = "different"
		if target.ID != "42" {
			t.Fatal("resolve aliased input ref")
		}
	}

	existing := want
	existing.Description += "\n\n## Notes\nPreserve this text"
	existing.Labels = []string{"incident"}
	before, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := template.Validate(existing); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(existing)
	if err != nil || string(before) != string(after) {
		t.Fatalf("validation changed task: %s, %v", after, err)
	}
	existing.Scope = ""
	if err := template.Validate(existing); err == nil {
		t.Fatal("later validation used creation default")
	}
}

func TestInvalidSelection(t *testing.T) {
	base := Template{Ref: Ref{Name: "incident", Revision: "1"}}
	for _, tc := range []struct {
		c   Catalog
		ref Ref
	}{
		{Catalog{base}, Ref{Name: "incident", Revision: "2"}},
		{Catalog{base}, Ref{Name: "incident", Revision: ""}},
		{Catalog{base, base}, base.Ref},
		{Catalog{{}}, Ref{}},
	} {
		if _, err := tc.c.Select(tc.ref); err == nil {
			t.Fatalf("selected %v from %v", tc.ref, tc.c)
		}
	}
}

func TestRequiredFields(t *testing.T) {
	for _, field := range []string{"scope", "title", "description", "type", "priority", "labels", "links", "assignee"} {
		t.Run(field, func(t *testing.T) {
			template := Template{Ref: Ref{Name: "bug", Revision: "1"}, Required: []string{field}}
			blank := " "
			defaultTitle := "default title"
			template.Defaults.Title = &defaultTitle
			got, err := template.Resolve(Fields{Title: &blank, Labels: []string{" "}, Links: []provider.Link{{Title: "empty target"}}})
			if err == nil || !strings.Contains(err.Error(), field) || !reflect.DeepEqual(got, provider.TaskDraft{}) {
				t.Fatalf("resolve returned %#v, %v", got, err)
			}
			if field == "assignee" && !errors.Is(err, provider.ErrUnsupported) {
				t.Fatalf("unsupported field error = %v", err)
			}
		})
	}
}

func TestRequiredBody(t *testing.T) {
	template := Template{Ref: Ref{Name: "review", Revision: "2"}, Sections: []Section{{Heading: "Impact"}}, Checklist: []string{"Verify"}}
	for _, body := range []string{"- [ ] Verify", "## Impact\n\n## Next\n- [x] Verify", "## Impact\nReported\n~~~\n- [ ] Verify\n~~~"} {
		if err := template.Validate(provider.TaskDraft{Description: body}); err == nil {
			t.Errorf("accepted incomplete body %q", body)
		}
	}
	body := "## Impact"
	if _, err := template.Resolve(Fields{Description: &body}); err == nil {
		t.Fatal("generated checklist supplied empty section content")
	}
	template.Checklist = []string{"duplicate", "duplicate"}
	if _, err := template.Resolve(Fields{}); err == nil {
		t.Fatal("accepted duplicate checklist definition")
	}
}

func TestAdapterSupport(t *testing.T) {
	template := Template{Ref: Ref{Name: "bug", Revision: "1"}, Required: []string{"priority"}, Checklist: []string{"Verify"}}
	for _, fields := range [][]string{{"description"}, {"priority"}} {
		if err := template.CheckSupport(fields); !errors.Is(err, provider.ErrUnsupported) {
			t.Errorf("support for %v = %v", fields, err)
		}
	}
	if err := template.CheckSupport([]string{"priority", "description"}); err != nil {
		t.Fatal(err)
	}
}
