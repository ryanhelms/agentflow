package okf

import (
	"bytes"
	"errors"
	"fmt"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// ValidateFormat checks the requested encoding for newly published concepts.
func ValidateFormat(format string) error {
	if format != "okf" && format != "legacy" {
		return fmt.Errorf("unknown knowledge format %q (expected okf or legacy)", format)
	}
	return nil
}

// SetField edits one top-level string key, leaving other keys and their YAML
// representation intact. Replacing an alias changes only this field.
func SetField(mapping *yaml.Node, key string, value any) error {
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return fmt.Errorf("encode field %q: %w", key, err)
	}
	for i := 0; i < len(mapping.Content); i += 2 {
		if stringValue(mapping.Content[i]) == key {
			old := mapping.Content[i+1]
			node.HeadComment, node.LineComment, node.FootComment = old.HeadComment, old.LineComment, old.FootComment
			mapping.Content[i+1] = &node
			return nil
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &node)
	return nil
}

// Marshal writes the complete metadata tree and the exact Markdown body.
func Marshal(mapping *yaml.Node, body string) ([]byte, error) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, errors.New("frontmatter must be a mapping")
	}
	if _, err := readFields(mapping); err != nil {
		return nil, err
	}
	if !utf8.ValidString(body) {
		return nil, errors.New("markdown body must be UTF-8")
	}
	emitter := nodeEmitter{emitted: make(map[*yaml.Node]string), used: make(map[string]bool)}
	header, err := yaml.Marshal(emitter.prepare(mapping))
	if err != nil {
		return nil, fmt.Errorf("encode OKF frontmatter: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(header)
	buf.WriteString("---\n")
	buf.WriteString(body)
	return buf.Bytes(), nil
}

// If a replaced field defined an anchor, its surviving aliases still refer to
// the original value. Emit that value at the first surviving reference. Work
// on a copy so serializing a concept doesn't mutate its parsed metadata.
type nodeEmitter struct {
	emitted map[*yaml.Node]string
	used    map[string]bool
}

func (e *nodeEmitter) prepare(n *yaml.Node) *yaml.Node {
	src := dereference(n)
	out := *src
	if src.Anchor != "" {
		if anchor, ok := e.emitted[src]; ok {
			alias := *n
			alias.Kind, alias.Value, alias.Alias = yaml.AliasNode, anchor, src
			return &alias
		}
		for suffix := 2; e.used[out.Anchor]; suffix++ {
			out.Anchor = fmt.Sprintf("%s_%d", src.Anchor, suffix)
		}
		e.used[out.Anchor] = true
		e.emitted[src] = out.Anchor
	}
	// yaml.v3's implicit schema differs from YAML 1.2. In particular it
	// may emit very large integers or exponent-shaped strings unquoted.
	if out.Kind == yaml.ScalarNode && out.Tag == "!!str" && out.Style == 0 && coreTag(out.Value) != "!!str" {
		out.Style = yaml.DoubleQuotedStyle
	}
	if len(src.Content) > 0 {
		out.Content = make([]*yaml.Node, len(src.Content))
		for i, child := range src.Content {
			out.Content[i] = e.prepare(child)
		}
	}
	return &out
}
