package consolecfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/nscaledev/devflow/internal/config"
	"gopkg.in/yaml.v3"
)

// EditSetting prepares repository YAML for a preview. A blank path changes
// the role selection. Resetting a property removes only its inline override.
func EditSetting(content, role, path string, value json.RawMessage, inherit bool) (string, error) {
	if !slices.Contains(config.ProviderRoles, role) {
		return "", errors.New("unknown provider role")
	}
	if len(content)+len(value) > maxSourceBytes {
		return "", errors.New("source editor limit exceeded")
	}
	if sensitive(content) || sensitive(string(value)) {
		return "", errors.New("credential-like value in source")
	}
	if err := validateFile(core, content); err != nil {
		return "", errors.New(safeError(err))
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return "", err
	}
	parts := strings.Split(role, ".")
	var replacement yaml.Node
	if path == "" {
		parts = append(parts, "provider")
		if !inherit {
			var id string
			if err := json.Unmarshal(value, &id); err != nil {
				return "", errors.New("provider selection requires an id")
			}
			for _, r := range config.ProviderCatalog() {
				if r.Role == role && !slices.Contains(r.Providers, id) {
					return "", errors.New("unknown provider for this role")
				}
			}
		}
	} else {
		keys, err := settingKeys(path)
		if err != nil {
			return "", err
		}
		parts = append(append(parts, "settings"), keys...)
	}
	if !inherit {
		if !json.Valid(value) {
			return "", errors.New("setting value must be valid JSON")
		}
		var val yaml.Node
		if err := yaml.Unmarshal(value, &val); err != nil {
			return "", err
		}
		replacement = *val.Content[0]
	}
	parent := doc.Content[0]
	for _, part := range parts[:len(parts)-1] {
		if err := editableMap(parent); err != nil {
			return "", err
		}
		idx := field(parent, part)
		if idx < 0 {
			if inherit {
				return content, nil
			}
			child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			parent.Content = append(parent.Content, keyNode(part), child)
			parent = child
		} else {
			parent = parent.Content[idx+1]
		}
	}
	if err := editableMap(parent); err != nil {
		return "", err
	}
	key := parts[len(parts)-1]
	idx := field(parent, key)
	if inherit && idx < 0 {
		return content, nil
	}
	// A role's inline settings are typed by its provider. Preserve them
	// when reselecting the same ID or explicitly selecting the default.
	if path == "" {
		var defaultID string
		for _, r := range config.ProviderCatalog() {
			if r.Role == role {
				defaultID = r.Default
				break
			}
		}
		oldID := defaultID
		if idx >= 0 {
			oldID = parent.Content[idx+1].Value
		}
		nextID := defaultID
		if !inherit {
			nextID = replacement.Value
		}
		if oldID != nextID {
			if at := field(parent, "settings"); at >= 0 {
				parent.Content = slices.Delete(parent.Content, at, at+2)
				idx = field(parent, key)
			}
		}
	}
	switch {
	case inherit:
		parent.Content = slices.Delete(parent.Content, idx, idx+2)
	case idx >= 0:
		old := parent.Content[idx+1]
		if old.Kind == yaml.AliasNode || old.Anchor != "" {
			return "", errors.New("edit the anchored setting in the source editor")
		}
		replacement.HeadComment = old.HeadComment
		replacement.LineComment = old.LineComment
		replacement.FootComment = old.FootComment
		parent.Content[idx+1] = &replacement
	default:
		parent.Content = append(parent.Content, keyNode(key), &replacement)
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	if err := validateFile(core, out.String()); err != nil {
		return "", errors.New("use the source editor to preserve linked YAML anchors")
	}
	if sensitive(out.String()) {
		return "", errors.New("composed source contains a credential-like value; use a credential reference")
	}
	return out.String(), nil
}

func keyNode(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }
func editableMap(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode || n.Anchor != "" {
		return errors.New("edit scalar, aliased or anchored sections in the source editor")
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Tag == "!!merge" {
			return errors.New("edit YAML merge keys in the source editor")
		}
	}
	return nil
}
func field(n *yaml.Node, key string) int {
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func settingKeys(path string) ([]string, error) {
	if len(path) > 2048 || strings.ContainsFunc(path, unicode.IsControl) {
		return nil, errors.New("invalid setting path")
	}
	var out []string
	var component strings.Builder
	escaped := false
	for _, r := range path {
		switch {
		case escaped:
			if r != '.' && r != '\\' {
				return nil, fmt.Errorf("invalid path escape %q", r)
			}
			component.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '.':
			if component.Len() == 0 {
				return nil, errors.New("empty path component")
			}
			out = append(out, component.String())
			component.Reset()
		default:
			component.WriteRune(r)
		}
	}
	if escaped || component.Len() == 0 {
		return nil, errors.New("incomplete setting path")
	}
	return append(out, component.String()), nil
}
