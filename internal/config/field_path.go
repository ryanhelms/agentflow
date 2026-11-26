package config

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Field IDs and paths: the phase 4 grammar (ADR section "Phase 4 field IDs").
//
// A path is the YAML keys from the layer file's root, joined by ".". Inside a
// key, "\." is a dot and "\\" a backslash: the setting path grammar that
// agentflow_config already uses (consolecfg). A segment that is exactly "*"
// is a wildcard in a catalog pattern: any map key or list index. A key that
// is a literal "*" cannot be addressed.
//
// An ID is "<layer>:<path>", for example "repo:policy.max_autonomy.local" or
// "personal:autonomy.actions.console\.publish".

// MaxFieldPath bounds a path in bytes.
const MaxFieldPath = 2048

// FieldWildcard is the catalog segment that matches any key or index.
const FieldWildcard = "*"

// FormatFieldPath joins keys into a path, escaping each key.
func FormatFieldPath(keys ...string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		k = strings.ReplaceAll(k, `\`, `\\`)
		parts[i] = strings.ReplaceAll(k, ".", `\.`)
	}
	return strings.Join(parts, ".")
}

// ParseFieldPath splits a path into its keys.
func ParseFieldPath(path string) ([]string, error) {
	if path == "" {
		return nil, errors.New("empty field path")
	}
	if len(path) > MaxFieldPath || strings.ContainsFunc(path, unicode.IsControl) {
		return nil, errors.New("invalid field path")
	}
	var out []string
	var key strings.Builder
	escaped := false
	end := func() error {
		if key.Len() == 0 {
			return errors.New("empty field path segment")
		}
		out = append(out, key.String())
		key.Reset()
		return nil
	}
	for _, r := range path {
		switch {
		case escaped:
			if r != '.' && r != '\\' {
				return nil, fmt.Errorf("invalid field path escape %q", r)
			}
			key.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '.':
			if err := end(); err != nil {
				return nil, err
			}
		default:
			key.WriteRune(r)
		}
	}
	if escaped {
		return nil, errors.New("incomplete field path escape")
	}
	if err := end(); err != nil {
		return nil, err
	}
	return out, nil
}

// FieldID joins a layer and a path.
func FieldID(layer FieldLayer, path string) string { return string(layer) + ":" + path }

// ParseFieldID splits an ID into its layer and path, and checks both.
func ParseFieldID(id string) (FieldLayer, string, error) {
	layer, path, ok := strings.Cut(id, ":")
	if !ok {
		return "", "", fmt.Errorf("field %q: expected <layer>:<path>", id)
	}
	switch FieldLayer(layer) {
	case FieldPersonal, FieldRepo:
	default:
		return "", "", fmt.Errorf("field %q: unknown layer %q", id, layer)
	}
	if _, err := ParseFieldPath(path); err != nil {
		return "", "", fmt.Errorf("field %q: %w", id, err)
	}
	return FieldLayer(layer), path, nil
}

// MatchFieldPath reports whether concrete keys match a catalog pattern's
// keys: the same length, and every pattern segment is FieldWildcard or the
// same key.
func MatchFieldPath(pattern, keys []string) bool {
	if len(pattern) != len(keys) {
		return false
	}
	for i, p := range pattern {
		if p != FieldWildcard && p != keys[i] {
			return false
		}
	}
	return true
}
