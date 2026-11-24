// Package tasktemplate prepares task drafts and validates them against a pinned template.
package tasktemplate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nscaledev/agentflow/internal/provider"
)

// Ref is stored alongside the created task. Revision is an opaque, exact identifier.
type Ref = provider.TaskTemplateRef

type Template struct {
	Ref       `yaml:",inline"`
	Defaults  Fields    `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	Required  []string  `yaml:"required,omitempty" json:"required,omitempty"`
	Sections  []Section `yaml:"sections,omitempty" json:"sections,omitempty"`
	Checklist []string  `yaml:"checklist,omitempty" json:"checklist,omitempty"`
}

// Fields distinguishes omission from an explicitly empty value, including an empty list.
type Fields struct {
	Scope       *string         `yaml:"scope,omitempty" json:"scope,omitempty"`
	Title       *string         `yaml:"title,omitempty" json:"title,omitempty"`
	Description *string         `yaml:"description,omitempty" json:"description,omitempty"`
	Type        *string         `yaml:"type,omitempty" json:"type,omitempty"`
	Priority    *string         `yaml:"priority,omitempty" json:"priority,omitempty"`
	Labels      []string        `yaml:"labels" json:"labels"`
	Links       []provider.Link `yaml:"links" json:"links"`
}

// Section requires a level-two heading with content. Body supplies the creation default.
type Section struct {
	Heading string `yaml:"heading" json:"heading"`
	Body    string `yaml:"body,omitempty" json:"body,omitempty"`
}

type Catalog []Template

// Select retrieves exactly one definition of the stored name and revision.
func (c Catalog) Select(ref Ref) (Template, error) {
	if err := ref.Validate(); err != nil {
		return Template{}, err
	}
	var match *Template
	for i := range c {
		if c[i].Ref == ref {
			if match != nil {
				return Template{}, fmt.Errorf("duplicate template %q revision %q", ref.Name, ref.Revision)
			}
			match = &c[i]
		}
	}
	if match == nil {
		return Template{}, fmt.Errorf("unknown template %q revision %q", ref.Name, ref.Revision)
	}
	if err := match.checkDefinition(); err != nil {
		return Template{}, err
	}
	return *match, nil
}

// CheckSupport verifies that the configured adapter can carry every template requirement.
// Pass the canonical field names the adapter can write (or read for later validation).
func (t Template) CheckSupport(supported []string) error {
	if err := t.checkDefinition(); err != nil {
		return err
	}
	for _, field := range t.Required {
		if !slices.Contains(supported, field) {
			return fmt.Errorf("adapter cannot carry required field %q: %w", field, provider.ErrUnsupported)
		}
	}
	if (len(t.Sections) > 0 || len(t.Checklist) > 0) && !slices.Contains(supported, "description") {
		return fmt.Errorf("adapter cannot carry required body content: %w", provider.ErrUnsupported)
	}
	return nil
}

func (t Template) checkDefinition() error {
	if err := t.Ref.Validate(); err != nil {
		return err
	}
	for _, field := range t.Required {
		switch field {
		case "scope", "title", "description", "type", "priority", "labels", "links":
		default:
			return fmt.Errorf("unsupported required field %q: %w", field, provider.ErrUnsupported)
		}
	}
	var headings []string
	for _, section := range t.Sections {
		headings = append(headings, section.Heading)
	}
	if err := checkNames("section heading", headings); err != nil {
		return err
	}
	return checkNames("checklist item", t.Checklist)
}

func checkNames(kind string, names []string) error {
	seen := make(map[string]bool)
	for _, name := range names {
		if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\r\n") || seen[name] {
			return fmt.Errorf("invalid or duplicate %s %q", kind, name)
		}
		seen[name] = true
	}
	return nil
}

func (f Fields) apply(d *provider.TaskDraft) {
	for _, pair := range []struct{ src, dst *string }{
		{f.Scope, &d.Scope}, {f.Title, &d.Title}, {f.Description, &d.Description},
		{f.Type, &d.Type}, {f.Priority, &d.Priority},
	} {
		if pair.src != nil {
			*pair.dst = *pair.src
		}
	}
	if f.Labels != nil {
		d.Labels = slices.Clone(f.Labels)
	}
	if f.Links != nil {
		d.Links = slices.Clone(f.Links)
		for i, link := range d.Links {
			if link.Ref != nil {
				ref := *link.Ref
				d.Links[i].Ref = &ref
			}
		}
	}
}

// Resolve constructs and validates a creation draft. Only successful results may be submitted.
func (t Template) Resolve(inputs Fields) (provider.TaskDraft, error) {
	if err := t.checkDefinition(); err != nil {
		return provider.TaskDraft{}, err
	}
	var draft provider.TaskDraft
	t.Defaults.apply(&draft)
	inputs.apply(&draft)
	body := inspectBody(draft.Description)
	var additions []string
	for _, s := range t.Sections {
		if _, exists := body.sections[s.Heading]; !exists && strings.TrimSpace(s.Body) != "" {
			additions = append(additions, "## "+s.Heading+"\n"+s.Body)
		}
	}
	draft.Description = appendBlocks(draft.Description, additions)
	body = inspectBody(draft.Description)
	if err := t.validateSections(body); err != nil {
		return provider.TaskDraft{}, err
	}
	var items []string
	for _, item := range t.Checklist {
		if !body.items[item] {
			items = append(items, "- [ ] "+item)
		}
	}
	if len(items) > 0 {
		draft.Description = appendBlocks(draft.Description, []string{strings.Join(items, "\n")})
	}
	if err := t.validateDraft(draft); err != nil {
		return provider.TaskDraft{}, err
	}
	return draft, nil
}

// Validate checks a current task snapshot before a later operation without applying creation defaults.
func (t Template) Validate(draft provider.TaskDraft) error {
	if err := t.checkDefinition(); err != nil {
		return err
	}
	return t.validateDraft(draft)
}

func (t Template) validateDraft(d provider.TaskDraft) error {
	values := map[string]string{
		"title": d.Title, "scope": d.Scope, "description": d.Description,
		"type": d.Type, "priority": d.Priority,
	}
	for _, field := range t.Required {
		present := strings.TrimSpace(values[field]) != ""
		switch field {
		case "labels":
			present = slices.ContainsFunc(d.Labels, func(label string) bool { return strings.TrimSpace(label) != "" })
		case "links":
			present = slices.ContainsFunc(d.Links, func(link provider.Link) bool {
				return strings.TrimSpace(link.URL) != "" || link.Ref != nil && link.Ref.Validate() == nil
			})
		}
		if !present {
			return fmt.Errorf("missing required field %q", field)
		}
	}
	body := inspectBody(d.Description)
	if err := t.validateSections(body); err != nil {
		return err
	}
	for _, item := range t.Checklist {
		if !body.items[item] {
			return fmt.Errorf("missing required checklist item %q", item)
		}
	}
	return nil
}

func (t Template) validateSections(body bodyIndex) error {
	for _, s := range t.Sections {
		if !body.sections[s.Heading] {
			return fmt.Errorf("missing content for required section %q", s.Heading)
		}
	}
	return nil
}
