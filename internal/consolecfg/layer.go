package consolecfg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"gopkg.in/yaml.v3"
)

// Sensitive reports whether text looks like it holds a credential: the
// patterns every console surface refuses.
func Sensitive(text string) bool { return sensitive(text) }

// Digest is the hex SHA-256 of text, the form File.Digest and Preview's
// base digest use.
func Digest(text string) string { return digest(text) }

// MaxLayerBytes is the largest layer file the settings editor reads or writes.
const MaxLayerBytes = maxSourceBytes

// EditLayer sets or removes one path in a configuration layer file (YAML),
// keeping comments, key order and the rest of the document: the same node
// edits the document editor makes. keys are the YAML keys from the root; a
// key that is a list index addresses a list item. An empty file becomes a
// mapping. Removing a path that is not set returns content unchanged.
func EditLayer(content string, keys []string, value json.RawMessage, remove bool) (string, error) {
	if len(keys) == 0 {
		return "", errors.New("a settings edit needs a path")
	}
	if strings.TrimSpace(content) == "" {
		if remove {
			return content, nil
		}
		return newLayer(keys, value)
	}
	if len(value) > 0 && !remove && !json.Valid(value) {
		return "", errors.New("setting value must be valid JSON")
	}
	return EditDocument(content, "yaml", keys, value, remove)
}

// newLayer writes the first setting of an empty layer file.
func newLayer(keys []string, value json.RawMessage) (string, error) {
	val, _, err := readDocument(string(value), "json")
	if err != nil {
		return "", err
	}
	child, err := missingContainers(keys[1:], val.Content[0], fieldSpec{})
	if err != nil {
		return "", err
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{keyNode(keys[0]), child}}}}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// LayerValue returns the value at keys in a layer file as JSON, and whether
// it is set. A missing file or path is not set.
func LayerValue(content string, keys []string) (json.RawMessage, bool, error) {
	if strings.TrimSpace(content) == "" {
		return nil, false, nil
	}
	var doc any
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return nil, false, errors.New(safeError(err))
	}
	v, ok := lookupPlain(doc, keys)
	if !ok {
		return nil, false, nil
	}
	b, err := json.Marshal(jsonCompatible(v))
	if err != nil {
		return nil, false, errors.New("the value is not JSON-compatible")
	}
	return b, true, nil
}

func lookupPlain(v any, keys []string) (any, bool) {
	for _, k := range keys {
		switch node := v.(type) {
		case map[string]any:
			next, ok := node[k]
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			i := -1
			for j := range node {
				if k == itoa(j) {
					i = j
				}
			}
			if i < 0 {
				return nil, false
			}
			v = node[i]
		default:
			return nil, false
		}
	}
	return v, true
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// jsonCompatible converts YAML-decoded maps with non-string keys, which
// yaml.v3 produces as map[any]any only for such keys, into string-keyed maps.
func jsonCompatible(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = jsonCompatible(x)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			b, _ := json.Marshal(k)
			out[strings.Trim(string(b), `"`)] = jsonCompatible(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = jsonCompatible(x)
		}
		return out
	}
	return v
}

// DefinitionSources returns the worktree's provider definition files
// (.agentflow/providers/*.yml) by path, for config.InspectRepo.
func DefinitionSources(ctx context.Context, root string) (map[string][]byte, error) {
	snap, err := View(ctx, root)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, f := range snap.Files {
		if f.Kind == def && f.Editable {
			out[f.Path] = []byte(f.Content)
		}
	}
	return out, nil
}

// PlainValue returns the value at keys in v, a value decoded from YAML or
// built from a configuration type, as JSON, and whether it is there.
func PlainValue(v any, keys []string) (json.RawMessage, bool) {
	found, ok := lookupPlain(jsonCompatible(v), keys)
	if !ok {
		return nil, false
	}
	b, err := json.Marshal(found)
	if err != nil {
		return nil, false
	}
	return b, true
}

// Plain turns a configuration value into the plain form PlainValue reads:
// marshalled as YAML and decoded again, as the file would hold it.
func Plain(v any) (any, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := yaml.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return jsonCompatible(out), nil
}
