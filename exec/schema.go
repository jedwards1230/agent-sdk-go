package exec

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
)

// schema is a documented subset of JSON Schema draft 2020-12, sufficient to
// validate a headless run's final text result without pulling in a dependency
// (tool.Schema is serialize-only). It supports exactly these keywords:
//
//   - type: object | array | string | number | integer | boolean | null
//     (integer = a JSON number with no fractional part; number accepts any JSON
//     number; JSON numbers decode to float64).
//   - properties (name→subschema) + required ([]string) for objects.
//   - additionalProperties (bool): when false, an unknown property is a
//     violation. Defaults to true.
//   - items (subschema applied to every array element).
//   - enum ([]any): the value must deep-equal one entry.
//   - minimum / maximum (inclusive numeric bounds).
//   - minLength / maxLength (string rune length).
//   - minItems / maxItems (array length).
//
// Unknown keywords are ignored (forward-compatible), but an unrecognized type
// is a compile error.
type schema struct {
	Type                 string             `json:"type"`
	Properties           map[string]*schema `json:"properties"`
	Required             []string           `json:"required"`
	AdditionalProperties *bool              `json:"additionalProperties"`
	Items                *schema            `json:"items"`
	Enum                 []any              `json:"enum"`
	Minimum              *float64           `json:"minimum"`
	Maximum              *float64           `json:"maximum"`
	MinLength            *int               `json:"minLength"`
	MaxLength            *int               `json:"maxLength"`
	MinItems             *int               `json:"minItems"`
	MaxItems             *int               `json:"maxItems"`
}

// compileSchema unmarshals doc into a recursive schema and validates that every
// type keyword names a supported type. A malformed document or an unrecognized
// type returns an error.
func compileSchema(doc []byte) (*schema, error) {
	var s schema
	if err := json.Unmarshal(doc, &s); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	if err := s.checkTypes(); err != nil {
		return nil, err
	}
	return &s, nil
}

// checkTypes recursively rejects unrecognized type keywords at compile time.
func (s *schema) checkTypes() error {
	switch s.Type {
	case "", "object", "array", "string", "number", "integer", "boolean", "null":
	default:
		return fmt.Errorf("unsupported schema type %q", s.Type)
	}
	for name, sub := range s.Properties {
		if sub == nil {
			continue
		}
		if err := sub.checkTypes(); err != nil {
			return fmt.Errorf("properties.%s: %w", name, err)
		}
	}
	if s.Items != nil {
		if err := s.Items.checkTypes(); err != nil {
			return fmt.Errorf("items: %w", err)
		}
	}
	return nil
}

// validate decodes data as JSON and checks it against the schema, returning a
// *SchemaError naming the path and reason of the first violation, or nil.
func (s *schema) validate(data []byte) error {
	var v any
	if len(data) == 0 || json.Unmarshal(data, &v) != nil {
		return &SchemaError{Path: "", Msg: "final result is not valid JSON"}
	}
	// Return through a nil check so a nil *SchemaError does not become a
	// non-nil error interface (the typed-nil trap).
	if se := s.validateValue("", v); se != nil {
		return se
	}
	return nil
}

// validateValue checks v against the schema at the given JSON-pointer-ish path.
func (s *schema) validateValue(path string, v any) *SchemaError {
	if err := s.checkType(path, v); err != nil {
		return err
	}
	if len(s.Enum) > 0 && !enumContains(s.Enum, v) {
		return &SchemaError{Path: path, Msg: "value not in enum"}
	}
	if err := s.checkBounds(path, v); err != nil {
		return err
	}
	switch tv := v.(type) {
	case map[string]any:
		return s.validateObject(path, tv)
	case []any:
		return s.validateArray(path, tv)
	}
	return nil
}

// checkType enforces the type keyword against v.
func (s *schema) checkType(path string, v any) *SchemaError {
	if s.Type == "" {
		return nil
	}
	ok := false
	switch s.Type {
	case "object":
		_, ok = v.(map[string]any)
	case "array":
		_, ok = v.([]any)
	case "string":
		_, ok = v.(string)
	case "boolean":
		_, ok = v.(bool)
	case "null":
		ok = v == nil
	case "number":
		_, ok = v.(float64)
	case "integer":
		f, isNum := v.(float64)
		ok = isNum && f == math.Trunc(f)
	}
	if !ok {
		return &SchemaError{Path: path, Msg: fmt.Sprintf("expected %s, got %s", s.Type, jsonTypeName(v))}
	}
	return nil
}

// checkBounds enforces the numeric, string-length, and array-length bounds that
// apply to v's concrete type.
func (s *schema) checkBounds(path string, v any) *SchemaError {
	switch tv := v.(type) {
	case float64:
		if s.Minimum != nil && tv < *s.Minimum {
			return &SchemaError{Path: path, Msg: fmt.Sprintf("%v is less than minimum %v", tv, *s.Minimum)}
		}
		if s.Maximum != nil && tv > *s.Maximum {
			return &SchemaError{Path: path, Msg: fmt.Sprintf("%v is greater than maximum %v", tv, *s.Maximum)}
		}
	case string:
		n := len([]rune(tv))
		if s.MinLength != nil && n < *s.MinLength {
			return &SchemaError{Path: path, Msg: fmt.Sprintf("length %d is less than minLength %d", n, *s.MinLength)}
		}
		if s.MaxLength != nil && n > *s.MaxLength {
			return &SchemaError{Path: path, Msg: fmt.Sprintf("length %d is greater than maxLength %d", n, *s.MaxLength)}
		}
	case []any:
		n := len(tv)
		if s.MinItems != nil && n < *s.MinItems {
			return &SchemaError{Path: path, Msg: fmt.Sprintf("%d items is fewer than minItems %d", n, *s.MinItems)}
		}
		if s.MaxItems != nil && n > *s.MaxItems {
			return &SchemaError{Path: path, Msg: fmt.Sprintf("%d items is more than maxItems %d", n, *s.MaxItems)}
		}
	}
	return nil
}

// validateObject checks required properties, recurses into declared properties,
// and enforces additionalProperties=false.
func (s *schema) validateObject(path string, obj map[string]any) *SchemaError {
	for _, req := range s.Required {
		if _, present := obj[req]; !present {
			return &SchemaError{Path: childPath(path, req), Msg: "missing required property"}
		}
	}
	for name, val := range obj {
		if sub, declared := s.Properties[name]; declared && sub != nil {
			if err := sub.validateValue(childPath(path, name), val); err != nil {
				return err
			}
			continue
		}
		if s.AdditionalProperties != nil && !*s.AdditionalProperties {
			return &SchemaError{Path: childPath(path, name), Msg: "additional property not allowed"}
		}
	}
	return nil
}

// validateArray recurses the items subschema over every element.
func (s *schema) validateArray(path string, arr []any) *SchemaError {
	if s.Items == nil {
		return nil
	}
	for i, el := range arr {
		if err := s.Items.validateValue(childPath(path, fmt.Sprintf("%d", i)), el); err != nil {
			return err
		}
	}
	return nil
}

// childPath appends a JSON-pointer segment to a parent path.
func childPath(parent, key string) string { return parent + "/" + key }

// enumContains reports whether v deep-equals any entry.
func enumContains(entries []any, v any) bool {
	for _, e := range entries {
		if reflect.DeepEqual(e, v) {
			return true
		}
	}
	return false
}

// jsonTypeName names the JSON type of a decoded value for error messages.
func jsonTypeName(v any) string {
	switch tv := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		if tv == math.Trunc(tv) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}
