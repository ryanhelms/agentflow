package config

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// Apply only supplied fields to the freshly constructed settings object. Walking
// the Go schema also gives type/unknown-field errors the full configuration path.
// Lists replace their default as a unit. Object and map keys merge individually.
func applySettings(settings, value any, path string) error {
	return mergeSetting(reflect.ValueOf(settings).Elem(), value, path, "")
}

func mergeSetting(dst reflect.Value, value any, path string, rules reflect.StructTag) error {
	switch dst.Kind() {
	case reflect.Struct:
		fields, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected an object with string keys", path)
		}
		return mergeFields(dst, fields, path)
	case reflect.Pointer:
		if value == nil {
			dst.SetZero()
			return nil
		}
		next := reflect.New(dst.Type().Elem())
		if !dst.IsNil() {
			next.Elem().Set(dst.Elem())
		}
		dst.Set(next)
		return mergeSetting(dst.Elem(), value, path, rules)
	case reflect.Slice:
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: expected a list", path)
		}
		list := reflect.MakeSlice(dst.Type(), len(items), len(items))
		for i, item := range items {
			if err := mergeSetting(list.Index(i), item, fmt.Sprintf("%s[%d]", path, i), rules); err != nil {
				return err
			}
		}
		dst.Set(list)
	case reflect.Map:
		fields, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected an object with string keys", path)
		}
		merged := reflect.MakeMapWithSize(dst.Type(), dst.Len())
		iter := dst.MapRange()
		for iter.Next() {
			merged.SetMapIndex(iter.Key(), iter.Value())
		}
		dst.Set(merged)
		for _, key := range slices.Sorted(maps.Keys(fields)) {
			mapKey := reflect.ValueOf(key)
			item := reflect.New(dst.Type().Elem()).Elem()
			if current := dst.MapIndex(mapKey); current.IsValid() {
				item.Set(current)
			}
			if err := mergeSetting(item, fields[key], path+"."+key, rules); err != nil {
				return err
			}
			dst.SetMapIndex(mapKey, item)
		}
	default:
		// Do not allow YAML's implicit conversion of a boolean or number to a
		// string. An explicit false, zero or empty string is a real override.
		if dst.Type() != reflect.TypeOf(value) {
			return fmt.Errorf("%s: expected %s", path, dst.Type())
		}
		if allowed := rules.Get("allowed"); allowed != "" && !slices.Contains(strings.Split(allowed, ","), value.(string)) {
			return fmt.Errorf("%s: expected one of %s", path, allowed)
		}
		if rules.Get("nonnegative") == "true" && value.(int) < 0 {
			return fmt.Errorf("%s: expected a non-negative integer", path)
		}
		dst.Set(reflect.ValueOf(value))
	}
	return nil
}

func mergeFields(dst reflect.Value, fields map[string]any, path string) error {
	properties := make(map[string]reflect.StructField)
	for _, field := range reflect.VisibleFields(dst.Type()) {
		// Embedded inline structs expose their tagged fields at the same level.
		if name := field.Tag.Get("yaml"); name != "" && name != ",inline" {
			properties[name] = field
		}
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		propertyPath := path + "." + name
		field, ok := properties[name]
		if !ok {
			return fmt.Errorf("%s: unknown property", propertyPath)
		}
		if err := mergeSetting(dst.FieldByIndex(field.Index), fields[name], propertyPath, field.Tag); err != nil {
			return err
		}
	}
	return nil
}
