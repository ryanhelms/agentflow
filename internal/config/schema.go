package config

import (
	"reflect"
	"strings"
)

// Field describes a provider settings node using the same types and tags as
// mergeSetting. Runtime defaults, dependencies and validation stay in LoadRepo.
type Field struct {
	Type                 string           `json:"type"`
	Nullable             bool             `json:"nullable,omitempty"`
	Properties           map[string]Field `json:"properties,omitempty"`
	Items                *Field           `json:"items,omitempty"`
	AdditionalProperties *Field           `json:"additional_properties,omitempty"`
	Enum                 []string         `json:"enum,omitempty"`
	Suggestions          []string         `json:"suggestions,omitempty"`
	KeySuggestions       []string         `json:"key_suggestions,omitempty"`
	Required             bool             `json:"required,omitempty"`
	Default              any              `json:"default,omitempty"`
	Constraint           *Constraint      `json:"constraint,omitempty"`
	Nonnegative          bool             `json:"nonnegative,omitempty"`
}

// ProviderFieldSchemas describes every compiled provider option, including
// providers not selected in this checkout.
func ProviderFieldSchemas() map[string]map[string]Field {
	out := map[string]map[string]Field{}
	for role, s := range providerSchemas() {
		fields := map[string]Field{"none": {Type: "object"}}
		for id, factory := range s.providers {
			fields[id] = fieldSchema(reflect.TypeOf(factory()).Elem(), "", "yaml", false)
		}
		out[role] = fields
	}
	return out
}

// SchemaFor describes the source representation of a Go configuration type.
// The root pointer supplied by a caller describes its value, not a nullable file.
func SchemaFor(value any, tag string) Field {
	t := reflect.TypeOf(value)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return fieldSchema(t, "", tag, true)
}

func fieldSchema(t reflect.Type, rules reflect.StructTag, tag string, nullableCollections bool) Field {
	switch t.Kind() {
	case reflect.Pointer:
		child := fieldSchema(t.Elem(), rules, tag, nullableCollections)
		child.Nullable = true
		return child
	case reflect.Struct:
		out := Field{Type: "object", Properties: map[string]Field{}}
		for _, field := range reflect.VisibleFields(t) {
			name, _, _ := strings.Cut(field.Tag.Get(tag), ",")
			if field.IsExported() && name != "" && name != "-" {
				out.Properties[name] = fieldSchema(field.Type, field.Tag, tag, nullableCollections)
			}
		}
		return out
	case reflect.Slice:
		child := fieldSchema(t.Elem(), rules, tag, nullableCollections)
		return Field{Type: "array", Items: &child, Nullable: nullableCollections}
	case reflect.Map:
		child := fieldSchema(t.Elem(), rules, tag, nullableCollections)
		return Field{Type: "object", AdditionalProperties: &child, Nullable: nullableCollections}
	case reflect.Bool:
		return Field{Type: "boolean"}
	case reflect.Int:
		return Field{Type: "integer", Nonnegative: rules.Get("nonnegative") == "true"}
	default:
		out := Field{Type: "string"}
		if choices := rules.Get("allowed"); choices != "" {
			out.Enum = strings.Split(choices, ",")
		}
		return out
	}
}
