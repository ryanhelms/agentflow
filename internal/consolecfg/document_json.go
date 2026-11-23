package consolecfg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"errors"
	"strings"

	"gopkg.in/yaml.v3"
)

// Read JSON through its own grammar (including escaped surrogate pairs) while
// keeping member order and the source coordinates needed by the form.
func readJSON(content string) (*yaml.Node, error) {
	d := json.NewDecoder(strings.NewReader(content))
	d.UseNumber()
	offset, line, column := 0, 1, 1
	var walk func(int) (*yaml.Node, error)
	walk = func(depth int) (*yaml.Node, error) {
		if depth > 128 {
			return nil, errors.New("JSON nesting exceeds the editor limit")
		}
		i := int(d.InputOffset())
		for i < len(content) && strings.ContainsRune(", :\t\r\n", rune(content[i])) {
			i++
		}
		for _, r := range content[offset:i] {
			if r == '\n' {
				line, column = line+1, 1
			} else {
				column++
			}
		}
		offset = i
		t, err := d.Token()
		if err != nil {
			return nil, invalidJSON(content)
		}
		n := &yaml.Node{Kind: yaml.ScalarNode, Line: line, Column: column}
		switch v := t.(type) {
		case json.Delim:
			n.Kind, n.Tag = yaml.SequenceNode, "!!seq"
			if v == '{' {
				n.Kind, n.Tag = yaml.MappingNode, "!!map"
			}
			keys := map[string]bool{}
			for d.More() {
				child, err := walk(depth + 1)
				if err != nil {
					return nil, err
				}
				if n.Kind == yaml.MappingNode && len(n.Content)%2 == 0 {
					if keys[child.Value] {
						return nil, fmt.Errorf("duplicate JSON key at line %d, column %d", child.Line, child.Column)
					}
					keys[child.Value] = true
				}
				n.Content = append(n.Content, child)
				if n.Kind == yaml.MappingNode {
					value, err := walk(depth + 1)
					if err != nil {
						return nil, err
					}
					n.Content = append(n.Content, value)
				}
			}
			if _, err := d.Token(); err != nil {
				return nil, invalidJSON(content)
			}
		case string:
			n.Tag, n.Value, n.Style = "!!str", v, yaml.DoubleQuotedStyle
		case json.Number:
			n.Tag, n.Value = "!!int", v.String()
			if strings.ContainsAny(n.Value, ".eE") {
				n.Tag = "!!float"
			}
		case bool:
			n.Tag, n.Value = "!!bool", fmt.Sprint(v)
		default:
			n.Tag, n.Value = "!!null", "null"
		}
		return n, nil
	}
	root, err := walk(0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, invalidJSON(content)
	}
	return root, nil
}

type span struct{ start, end int }

// jsonSpans maps the ordered syntax tree to byte ranges. JSON edits splice
// just the affected value or member; untouched formatting and number lexemes survive.
func jsonSpans(source string, root *yaml.Node) (map[*yaml.Node]span, error) {
	result := map[*yaml.Node]span{}
	dec := json.NewDecoder(strings.NewReader(source))
	dec.UseNumber()
	var walk func(*yaml.Node) error
	walk = func(node *yaml.Node) error {
		i := int(dec.InputOffset())
		for i < len(source) && strings.ContainsRune(", :\t\r\n", rune(source[i])) {
			i++
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		if node.Kind == yaml.MappingNode || node.Kind == yaml.SequenceNode {
			for _, c := range node.Content {
				if err := walk(c); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
		}
		result[node] = span{i, int(dec.InputOffset())}
		return nil
	}
	return result, walk(root)
}

func encodeNewJSON(n, leaf *yaml.Node, raw json.RawMessage) (string, error) {
	if n == leaf {
		return string(bytes.TrimSpace(raw)), nil
	}
	if n.Kind == yaml.SequenceNode && len(n.Content) == 1 {
		child, err := encodeNewJSON(n.Content[0], leaf, raw)
		return "[" + child + "]", err
	}
	if n.Kind == yaml.MappingNode && len(n.Content) == 2 {
		key, _ := json.Marshal(n.Content[0].Value)
		child, err := encodeNewJSON(n.Content[1], leaf, raw)
		return "{" + string(key) + ":" + child + "}", err
	}
	return "", errors.New("invalid insertion")
}

func editJSON(content string, root *yaml.Node, change fieldMutation, leaf *yaml.Node, raw json.RawMessage) (string, error) {
	spans, err := jsonSpans(content, root)
	if err != nil {
		return "", err
	}
	middle := ""
	if change.replacement != nil {
		middle, err = encodeNewJSON(change.replacement, leaf, raw)
		if err != nil {
			return "", err
		}
	}
	region := spans[root]
	p, idx := change.parent, change.index
	if p != nil {
		width := 1
		if p.Kind == yaml.MappingNode {
			width = 2
		}
		switch {
		case idx < 0:
			end := spans[p].end - 1
			parentStart := spans[p].start
			// Insert after the last value and before existing trailing whitespace.
			last := parentStart + 1
			gap := ""
			if len(p.Content) > 0 {
				last = spans[p.Content[len(p.Content)-1]].end
				gap = content[parentStart+1 : spans[p.Content[0]].start]
			} else if strings.Contains(content[last:end], "\n") {
				gap = "\n" + strings.Repeat(" ", sourceIndent(content))
			}
			prefix := gap
			if len(p.Content) > 0 {
				prefix = "," + gap
			}
			if p.Kind == yaml.MappingNode {
				key, _ := json.Marshal(change.key)
				sep := ":"
				if len(p.Content) > 0 {
					sep = content[spans[p.Content[0]].end:spans[p.Content[1]].start]
				}
				middle = string(key) + sep + middle
			}
			middle = prefix + middle
			region = span{last, last}
		case change.replacement == nil:
			region = span{spans[p.Content[idx]].start, spans[p.Content[idx+width-1]].end}
			if idx+width < len(p.Content) {
				region.end = spans[p.Content[idx+width]].start
			} else if idx > 0 {
				region.start = spans[p.Content[idx-1]].end
			}
		default:
			region = spans[p.Content[idx+width-1]]
		}
	}
	return content[:region.start] + middle + content[region.end:], nil
}
