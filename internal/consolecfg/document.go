package consolecfg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type Document struct {
	Format    string              `json:"format"`
	Schema    fieldSpec           `json:"schema"`
	Value     any                 `json:"value"`
	Positions map[string]Position `json:"positions"`
}

// DescribeDocument exposes the candidate's source representation, including
// temporarily invalid domain values, so users can repair it before Preview.
func DescribeDocument(ctx context.Context, root, path, content, baseDigest string) (Document, error) {
	snap, err := View(ctx, root)
	if err != nil {
		return Document{}, err
	}
	for _, f := range snap.Files {
		if f.Path != path {
			continue
		}
		if !f.Editable {
			break
		}
		if f.Digest != baseDigest {
			return Document{}, errors.New("source changed; reload the file before editing")
		}
		format := "yaml"
		if f.Kind == catalog {
			format = "json"
		}
		node, value, err := readDocument(content, format)
		if err != nil {
			return Document{}, err
		}
		d := Document{Format: format, Schema: sourceSchema(f.Kind, value, snap), Value: value, Positions: map[string]Position{}}
		positions(node.Content[0], "", d.Positions)
		return d, nil
	}
	return Document{}, errors.New("file is not editable in this repository")
}

// A long map key shared by thousands of descendants must not inflate a
// bounded source file into an unbounded collection of JSON pointers.
func positions(root *yaml.Node, prefix string, result map[string]Position) {
	budget := 4 * maxSourceBytes
	var visit func(*yaml.Node, string)
	visit = func(n *yaml.Node, path string) {
		if len(path) > 4096 || budget < len(path)+32 {
			return
		}
		budget -= len(path) + 32
		result[path] = Position{n.Line, n.Column}
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i < len(n.Content) && budget > 32; i += 2 {
				key := n.Content[i].Value
				if len(key) > 2048 {
					continue
				}
				key = strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
				visit(n.Content[i+1], path+"/"+key)
			}
		case yaml.SequenceNode:
			for i, child := range n.Content {
				if budget <= 32 {
					break
				}
				visit(child, path+"/"+strconv.Itoa(i))
			}
		}
	}
	visit(root, prefix)
}

func readDocument(content, format string) (*yaml.Node, any, error) {
	if len(content) > maxSourceBytes || sensitive(content) {
		return nil, nil, errors.New("source exceeds the editor limit or contains a credential-like value")
	}
	if format != "yaml" && format != "json" {
		return nil, nil, errors.New("unsupported document format")
	}
	var doc yaml.Node
	var value any
	if format == "json" {
		node, err := readJSON(content)
		if err != nil {
			return nil, nil, err
		}
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{node}}
		dec := json.NewDecoder(strings.NewReader(content))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			return nil, nil, invalidJSON(content)
		}
	} else {
		d := yaml.NewDecoder(strings.NewReader(content))
		if err := d.Decode(&doc); err != nil {
			return nil, nil, errors.New(safeError(err))
		}
		if len(doc.Content) != 1 || !errors.Is(d.Decode(new(yaml.Node)), io.EOF) {
			return nil, nil, errors.New("expected one configuration document")
		}
		if err := doc.Decode(&value); err != nil {
			return nil, nil, errors.New(safeError(err))
		}
	}
	if !browserNumbers(doc.Content[0]) {
		return nil, nil, errors.New("a number exceeds browser precision; edit this document in the source editor")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, nil, errors.New("structured editing requires JSON-compatible values and string keys")
	}
	if sensitive(string(b)) {
		return nil, nil, errors.New("document contains a credential-like value")
	}
	return &doc, value, nil
}

// The browser uses IEEE 754 numbers, including when editing a containing list
// or map. Refuse the structured view rather than silently rounding a source ID.
func browserNumbers(n *yaml.Node) bool {
	if n.Kind == yaml.ScalarNode {
		raw := strings.ReplaceAll(n.Value, "_", "")
		switch n.ShortTag() {
		case "!!int":
			if len(raw) > 128 {
				return false
			}
			i, ok := new(big.Int).SetString(raw, 0)
			return ok && i.BitLen() <= 53
		case "!!float":
			// Limit work even for adversarially large decimal exponents.
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil || len(raw) > 256 {
				return false
			}
			canonical := strconv.FormatFloat(f, 'g', -1, 64)
			if f == 0 {
				return strings.Trim(strings.SplitN(strings.ToLower(raw), "e", 2)[0], "0.-+") == ""
			}
			orig, ok := new(big.Rat).SetString(raw)
			roundTrip, valid := new(big.Rat).SetString(canonical)
			return ok && valid && orig.Cmp(roundTrip) == 0 && (!orig.IsInt() || orig.Num().BitLen() <= 53)
		}
	}
	for _, c := range n.Content {
		if !browserNumbers(c) {
			return false
		}
	}
	return true
}

func sameNodeValue(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return false
	}
	var first, second any
	return a.Decode(&first) == nil && b.Decode(&second) == nil && reflect.DeepEqual(first, second)
}

func invalidJSON(content string) error {
	var syntax *json.SyntaxError
	if err := json.Unmarshal([]byte(content), new(json.RawMessage)); errors.As(err, &syntax) {
		idx := max(0, min(len(content), int(syntax.Offset)-1))
		before := content[:idx]
		return fmt.Errorf("invalid JSON at line %d, column %d: %s", 1+strings.Count(before, "\n"), idx-strings.LastIndex(before, "\n"), safeError(err))
	}
	return errors.New("invalid JSON document")
}

type fieldMutation struct {
	parent      *yaml.Node
	index       int // mapping key or sequence index, -1 appends
	key         string
	replacement *yaml.Node
}

func listIndex(part string, length int, appendOK bool) (int, error) {
	if appendOK && part == "-" {
		return length, nil
	}
	i, err := strconv.Atoi(part)
	if err != nil || strconv.Itoa(i) != part || i < 0 || i > length || i == length && !appendOK {
		return 0, errors.New("invalid array index")
	}
	return i, nil
}

func childSchema(parent fieldSpec, key string) fieldSpec {
	if parent.Items != nil {
		return *parent.Items
	}
	if child, ok := parent.Properties[key]; ok {
		return child
	}
	if parent.AdditionalProperties != nil {
		return *parent.AdditionalProperties
	}
	return fieldSpec{}
}

func planField(root *yaml.Node, parts []string, replacement *yaml.Node, schema fieldSpec) (fieldMutation, error) {
	if len(parts) == 0 {
		return fieldMutation{replacement: replacement}, nil
	}
	parent := root
	for i, key := range parts {
		var idx int
		var next *yaml.Node
		switch parent.Kind {
		case yaml.MappingNode:
			if err := editableMap(parent); err != nil {
				return fieldMutation{}, err
			}
			idx = field(parent, key)
			if idx >= 0 {
				next = parent.Content[idx+1]
			}
		case yaml.SequenceNode:
			if parent.Anchor != "" {
				return fieldMutation{}, errors.New("edit anchored sequences in the source editor")
			}
			j, err := listIndex(key, len(parent.Content), replacement != nil)
			if err != nil {
				return fieldMutation{}, err
			}
			idx = -1
			if j < len(parent.Content) {
				idx = j
				next = parent.Content[idx]
			}
		default:
			return fieldMutation{}, errors.New("field parent must be an object or array")
		}
		schema = childSchema(schema, key)
		if i == len(parts)-1 || next == nil {
			if replacement != nil {
				var err error
				replacement, err = missingContainers(parts[i+1:], replacement, schema)
				if err != nil {
					return fieldMutation{}, err
				}
			}
			return fieldMutation{parent: parent, index: idx, key: key, replacement: replacement}, nil
		}
		parent = next
	}
	return fieldMutation{}, errors.New("invalid field path")
}

func missingContainers(parts []string, value *yaml.Node, schema fieldSpec) (*yaml.Node, error) {
	if len(parts) == 0 {
		return value, nil
	}
	key := parts[0]
	child, err := missingContainers(parts[1:], value, childSchema(schema, key))
	if err != nil {
		return nil, err
	}
	if schema.Type == "array" || schema.Type == "" && (key == "-" || key == "0") {
		if _, err := listIndex(key, 0, true); err != nil {
			return nil, err
		}
		return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{child}}, nil
	}
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{keyNode(key), child}}, nil
}

func linkedNode(n *yaml.Node) bool {
	if n.Anchor != "" || n.Kind == yaml.AliasNode {
		return true
	}
	return slices.ContainsFunc(n.Content, linkedNode)
}

// EditDocument changes a single source path. The optional source schema
// distinguishes a numeric map key from an array index in an absent parent.
func EditDocument(content, format string, path []string, value json.RawMessage, remove bool, schema ...fieldSpec) (string, error) {
	if len(path) > 64 || len(value) > maxSourceBytes {
		return "", errors.New("field edit exceeds the editor limit")
	}
	for _, p := range path {
		if p == "" || len(p) > 2048 || strings.ContainsFunc(p, unicode.IsControl) {
			return "", errors.New("invalid field path")
		}
	}
	doc, _, err := readDocument(content, format)
	if err != nil {
		return "", err
	}
	root := doc.Content[0]
	var replacement *yaml.Node
	if !remove {
		val, _, err := readDocument(string(value), "json")
		if err != nil {
			return "", err
		}
		replacement = val.Content[0]
	}
	if len(path) == 0 && remove {
		return "", errors.New("cannot remove a document or replace linked YAML anchors")
	}
	var s fieldSpec
	if len(schema) > 0 {
		s = schema[0]
	}
	edit, err := planField(root, path, replacement, s)
	if err != nil {
		return "", err
	}
	if edit.parent != nil && edit.index < 0 && remove {
		return content, nil
	}
	var prior *yaml.Node
	if edit.parent == nil {
		prior = root
	} else if edit.index >= 0 {
		index := edit.index
		if edit.parent.Kind == yaml.MappingNode {
			index++
		}
		prior = edit.parent.Content[index]
	}
	if sameNodeValue(prior, edit.replacement) {
		return content, nil
	}
	if prior != nil && format == "yaml" {
		if edit.replacement == nil && linkedNode(prior) {
			return "", errors.New("edit linked YAML anchors in the source editor")
		}
		if edit.replacement != nil {
			if err := preserveYAML(prior, edit.replacement); err != nil {
				return "", err
			}
		}
	}
	var result string
	if format == "json" {
		result, err = editJSON(content, root, edit, replacement, value)
	} else {
		applyYAML(doc, edit)
		var out bytes.Buffer
		enc := yaml.NewEncoder(&out)
		enc.SetIndent(sourceIndent(content))
		if err := enc.Encode(doc); err != nil {
			return "", err
		}
		if err := enc.Close(); err != nil {
			return "", err
		}
		result = out.String()
		if !strings.HasSuffix(content, "\n") {
			result = strings.TrimSuffix(result, "\n")
		}
		if strings.Contains(content, "\r\n") && !strings.Contains(strings.ReplaceAll(content, "\r\n", ""), "\n") {
			result = strings.ReplaceAll(result, "\n", "\r\n")
		}
	}
	if err != nil {
		return "", err
	}
	if _, _, err = readDocument(result, format); err != nil {
		return "", err
	}
	return result, nil
}

func sourceIndent(s string) int {
	for line := range strings.SplitSeq(s, "\n") {
		n := len(line) - len(strings.TrimLeft(line, " "))
		trim := strings.TrimSpace(line)
		if n > 0 && n <= 8 && trim != "" && !strings.HasPrefix(trim, "#") {
			return n
		}
	}
	return 2
}

func applyYAML(doc *yaml.Node, edit fieldMutation) {
	p, i, v := edit.parent, edit.index, edit.replacement
	if p == nil {
		doc.Content[0] = v
		return
	}
	if i < 0 {
		if p.Kind == yaml.MappingNode {
			p.Content = append(p.Content, keyNode(edit.key))
		}
		p.Content = append(p.Content, v)
		return
	}
	width := 1
	if p.Kind == yaml.MappingNode {
		width = 2
	}
	if v == nil {
		p.Content = slices.Delete(p.Content, i, i+width)
	} else {
		p.Content[i+width-1] = v
	}
}

// Reuse unchanged syntax nodes so comments and anchors stay with their
// original keys or list items even when their neighbors move.
func preserveYAML(old, next *yaml.Node) error {
	if sameNodeValue(old, next) {
		*next = *old
		return nil
	}
	if old.Anchor != "" || old.Kind == yaml.AliasNode {
		return errors.New("edit linked YAML anchors in the source editor")
	}
	next.HeadComment, next.LineComment, next.FootComment = old.HeadComment, old.LineComment, old.FootComment
	if old.Kind != next.Kind || old.ShortTag() != next.ShortTag() {
		if linkedNode(old) {
			return errors.New("edit linked YAML anchors in the source editor")
		}
		return nil
	}
	if old.Kind != yaml.ScalarNode || old.Style != 0 {
		next.Style = old.Style
	}
	switch next.Kind {
	case yaml.MappingNode:
		indices := map[string]int{}
		for i := 0; i < len(next.Content); i += 2 {
			indices[next.Content[i].Value] = i
		}
		for i := 0; i < len(old.Content); i += 2 {
			key := old.Content[i].Value
			j, ok := indices[key]
			if !ok {
				if linkedNode(old.Content[i+1]) {
					return errors.New("edit linked YAML anchors in the source editor")
				}
				continue
			}
			next.Content[j] = old.Content[i]
			if err := preserveYAML(old.Content[i+1], next.Content[j+1]); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		// Match equal items first, including moves and duplicates. A changed
		// item at an unclaimed index keeps its local comments on an in-place edit.
		prior := map[string][]int{}
		for i, n := range old.Content {
			k := itemKey(n)
			prior[k] = append(prior[k], i)
		}
		used := make([]bool, len(old.Content))
		matched := make([]bool, len(next.Content))
		for j, n := range next.Content {
			k := itemKey(n)
			if candidates := prior[k]; len(candidates) > 0 {
				i := candidates[0]
				prior[k] = candidates[1:]
				used[i], matched[j] = true, true
				next.Content[j] = old.Content[i]
			}
		}
		for i, n := range old.Content {
			if used[i] {
				continue
			}
			if len(old.Content) == len(next.Content) && !matched[i] {
				if err := preserveYAML(n, next.Content[i]); err != nil {
					return err
				}
			} else if linkedNode(n) {
				return errors.New("edit linked YAML anchors in the source editor")
			}
		}
	}
	return nil
}

func itemKey(n *yaml.Node) string {
	var v any
	_ = n.Decode(&v)
	data, _ := json.Marshal(v) // readDocument checked JSON compatibility
	return string(data)
}
