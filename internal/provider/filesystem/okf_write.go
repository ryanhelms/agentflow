package filesystem

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"unicode/utf8"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/okf"
	"gopkg.in/yaml.v3"
)

// yaml.v3 encodes invalid UTF-8 strings as !!binary, and encoding/json
// replaces invalid bytes. Both would change the requested metadata.
func validatePublishText(ref provider.Ref, d provider.DocumentDraft, operation string) error {
	values := []string{ref.Instance, ref.Scope, ref.ID, d.Title, d.Type, d.Description, operation}
	values = append(values, d.Tags...)
	for _, link := range d.Links {
		values = append(values, link.Title, link.URL)
		if r := link.Ref; r != nil {
			values = append(values, r.Instance, r.Scope, r.ID)
		}
	}
	for _, value := range values {
		if !utf8.ValidString(value) {
			return errors.New("document metadata must be UTF-8")
		}
	}
	return nil
}

func (r record) hasConceptMetadata() bool {
	return r.concept.Type != "" || r.concept.Description != "" || r.concept.Tags != nil
}

// Include the original v1 request digest, plus every input whose meaning is
// new in OKF. In particular nil preserves a list and [] explicitly clears it.
// This JSON shape and field order are part of the durable receipt format.
func (r record) publishFingerprint(legacy string) (string, error) {
	request := struct {
		Legacy, Type, Description string
		Tags                      []string
		LinksSupplied             bool
	}{
		Legacy: legacy, Type: r.concept.Type, Description: r.concept.Description,
		Tags: r.concept.Tags, LinksSupplied: r.Links != nil,
	}
	b, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	return digest(append([]byte("publish-okf-v1\n"), b...)), nil
}

var readableID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func (r *record) prepareDocument(old *record, format string) error {
	if (old == nil && format != "okf") || (old != nil && old.concept.Frontmatter == nil) {
		if r.hasConceptMetadata() {
			return fmt.Errorf("type, description and tags require an OKF document: %w", provider.ErrUnsupported)
		}
		return nil
	}

	requestedTitle := r.Title
	var previous okf.Concept
	if old != nil {
		previous = old.concept
		if r.body == "" {
			r.body = old.body
		}
		if r.Title == "" {
			r.Title = old.Title
		}
	} else if readableID.MatchString(r.Ref.ID) {
		// The ref suffix separates scopes, reserved names and case aliases.
		r.name = r.Ref.ID + "-" + r.Ref.StorageKey()[:16] + ".md"
	}
	mapping := previous.Frontmatter
	if mapping == nil {
		mapping = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if r.concept.Type == "" {
			r.concept.Type = "Document"
		}
	}
	r.concept.Frontmatter = mapping
	for _, field := range []struct{ key, requested, previous string }{
		{"type", r.concept.Type, previous.Type},
		{"title", requestedTitle, previous.Title},
		{"description", r.concept.Description, previous.Description},
	} {
		if field.requested != "" && field.requested != field.previous {
			if err := okf.SetField(mapping, field.key, field.requested); err != nil {
				return err
			}
		}
	}
	if r.concept.Tags != nil && (len(r.concept.Tags) == 0 || !slices.Equal(r.concept.Tags, previous.Tags)) {
		if err := okf.SetField(mapping, "tags", r.concept.Tags); err != nil {
			return err
		}
	}
	if old == nil || !old.explicitRef {
		if err := okf.SetField(mapping, "x-devflow-ref", map[string]string{"scope": r.Ref.Scope, "id": r.Ref.ID}); err != nil {
			return err
		}
	}
	if r.Links != nil && (old == nil || !reflect.DeepEqual(r.Links, old.Links)) {
		return okf.SetField(mapping, "x-devflow-links", r.Links)
	}
	return nil
}
