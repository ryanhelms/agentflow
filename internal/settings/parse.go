package settings

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ryanhelms/agentflow/internal/config"
)

// ResolveKey turns a command-line KEY into a field ID: an ID as given, or a
// path that exactly one layer has. A path both layers have is an error that
// names both IDs.
func ResolveKey(cat Catalog, key string) (string, error) {
	if l, _, ok := strings.Cut(key, ":"); ok && (l == string(config.FieldPersonal) || l == string(config.FieldRepo)) {
		if _, _, err := config.ParseFieldID(key); err != nil {
			return "", err
		}
		return key, nil
	}
	if _, err := config.ParseFieldPath(key); err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	var found []string
	for _, l := range []config.FieldLayer{config.FieldPersonal, config.FieldRepo} {
		id := config.FieldID(l, key)
		if _, _, ok := cat.Lookup(id, ""); ok {
			found = append(found, id)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("%s: no setting has this path; see agentflow config fields", key)
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%s is a setting in both layers: use %s", key, strings.Join(found, " or "))
}

// ParseAssignment turns KEY=VALUE into a set edit (the Message Translator of
// the config set verb). VALUE is literal text for a string field, a
// comma-separated list for a list of strings unless it starts with "[", and
// JSON otherwise.
func ParseAssignment(cat Catalog, arg string) (Edit, error) {
	key, raw, ok := strings.Cut(arg, "=")
	if !ok || key == "" {
		return Edit{}, fmt.Errorf("%q: expected KEY=VALUE", arg)
	}
	id, err := ResolveKey(cat, key)
	if err != nil {
		return Edit{}, err
	}
	node := config.Field{}
	if f, rest, ok := cat.Lookup(id, ""); ok {
		node = nodeAt(f.Schema, rest)
	}
	var value json.RawMessage
	switch {
	case node.Type == "string":
		value, _ = json.Marshal(raw)
	case node.Type == "array" && node.Items != nil && node.Items.Type == "string" && !strings.HasPrefix(strings.TrimSpace(raw), "["):
		items := []string{}
		for _, item := range strings.Split(raw, ",") {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}
		value, _ = json.Marshal(items)
	default:
		if !json.Valid([]byte(raw)) {
			return Edit{}, fmt.Errorf("%s: %q is not JSON; quote text as \"...\"", key, raw)
		}
		value = json.RawMessage(raw)
	}
	return Edit{ID: id, Op: OpSet, Value: value}, nil
}

// ParseUnset turns --unset KEY into an unset edit.
func ParseUnset(cat Catalog, key string) (Edit, error) {
	id, err := ResolveKey(cat, key)
	if err != nil {
		return Edit{}, err
	}
	return Edit{ID: id, Op: OpUnset}, nil
}
