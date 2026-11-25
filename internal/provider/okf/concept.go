// Package okf reads Open Knowledge Format v0.3 concept documents.
package okf

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// ErrNotConcept identifies ordinary Markdown and mappings without a concept type.
var ErrNotConcept = errors.New("not an OKF concept")

// Concept contains the common string fields and the complete frontmatter
// mapping, including any unrecognized or unprojected values.
// Body contains every byte following the closing delimiter line.
type Concept struct {
	Type, Title, Description, Body string
	Tags                           []string
	Frontmatter                    *yaml.Node
}

// Parse reads one Markdown file. It returns ErrNotConcept for ordinary Markdown
// or an absent type, and a different error for malformed frontmatter or UTF-8.
// It performs no path resolution or other I/O.
func Parse(data []byte) (Concept, error) {
	if !utf8.Valid(data) {
		return Concept{}, errors.New("invalid UTF-8 in markdown")
	}
	line, rest := cutLine(data)
	if !bytes.Equal(line, []byte("---")) {
		return Concept{}, ErrNotConcept
	}
	header := rest
	for len(rest) > 0 {
		line, body := cutLine(rest)
		if bytes.Equal(line, []byte("---")) {
			root, err := parseMapping(header[:len(header)-len(rest)])
			if err != nil {
				return Concept{}, err
			}
			concept, err := readFields(root)
			if err != nil {
				return Concept{}, err
			}
			concept.Body = string(body)
			return concept, nil
		}
		rest = body
	}
	return Concept{}, errors.New("unclosed frontmatter")
}

// cutLine recognizes Markdown's LF, CRLF and CR line endings.
func cutLine(data []byte) (line, rest []byte) {
	i := bytes.IndexAny(data, "\r\n")
	if i < 0 {
		return data, nil
	}
	end := i + 1
	if data[i] == '\r' && end < len(data) && data[end] == '\n' {
		end++
	}
	return data[:i], data[end:]
}

func parseMapping(header []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(parserInput(header)))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode frontmatter: %w", err)
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, errors.New("multiple YAML documents in frontmatter")
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("decode trailing frontmatter: %w", err)
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("frontmatter must be a YAML mapping")
	}
	resolveCore(root, tagSource(header))
	keys := keyTable{ids: make(map[*yaml.Node]int), values: make(map[keyValue]int)}
	if err := keys.check(root); err != nil {
		return nil, err
	}
	return root, nil
}

func readFields(root *yaml.Node) (Concept, error) {
	c := Concept{Frontmatter: root}
	for i := 0; i < len(root.Content); i += 2 {
		value := dereference(root.Content[i+1])
		switch stringValue(root.Content[i]) {
		case "type":
			switch {
			case value.Kind == yaml.ScalarNode && value.Tag == "!!str":
				c.Type = value.Value
			case value.Tag == "!!null", value.Tag == "!!seq" && len(value.Content) == 0:
				// OKF §11.2 treats null and an empty list as absent.
			default:
				return Concept{}, errors.New("concept type must be a string")
			}
		case "title":
			c.Title = stringValue(value)
		case "description":
			c.Description = stringValue(value)
		case "tags":
			if value.Kind == yaml.SequenceNode && value.Tag == "!!seq" {
				for _, item := range value.Content {
					if tag := stringValue(item); tag != "" {
						c.Tags = append(c.Tags, tag)
					}
				}
			} else if tag := stringValue(value); tag != "" {
				c.Tags = []string{tag}
			}
		}
	}
	if c.Type == "" {
		return Concept{}, ErrNotConcept
	}
	return c, nil
}

func dereference(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode {
		return n.Alias // yaml.v3 anchors always refer directly to a concrete node.
	}
	return n
}

func stringValue(n *yaml.Node) string {
	n = dereference(n)
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
		return n.Value
	}
	return ""
}
