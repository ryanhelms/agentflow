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
	Nonnegative          bool             `json:"nonnegative,omitempty"`
}

// ProviderFieldSchemas describes every compiled provider option, including
// providers not selected in this checkout.
func ProviderFieldSchemas() map[string]map[string]Field {
	out := map[string]map[string]Field{}
	for role, s := range providerSchemas() {
		fields := map[string]Field{"none": {Type: "object"}}
		for id, factory := range s.providers {
			fields[id] = fieldSchema(reflect.TypeOf(factory()).Elem(), "")
		}
		out[role] = fields
	}
	return out
}

func fieldSchema(t reflect.Type, rules reflect.StructTag) Field {
	switch t.Kind() {
	case reflect.Pointer:
		child := fieldSchema(t.Elem(), rules)
		child.Nullable = true
		return child
	case reflect.Struct:
		out := Field{Type: "object", Properties: map[string]Field{}}
		for _, field := range reflect.VisibleFields(t) {
			name := field.Tag.Get("yaml")
			if name != "" && name != ",inline" {
				out.Properties[name] = fieldSchema(field.Type, field.Tag)
			}
		}
		return out
	case reflect.Slice:
		child := fieldSchema(t.Elem(), rules)
		return Field{Type: "array", Items: &child}
	case reflect.Map:
		child := fieldSchema(t.Elem(), rules)
		return Field{Type: "object", AdditionalProperties: &child}
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
