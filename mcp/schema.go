package mcp

import (
	"encoding/json"

	"github.com/jedwards1230/agent-sdk-go/tool"
)

// schemaFromJSON converts an MCP tool's raw JSON Schema inputSchema into a
// [tool.Schema] — the flat type/properties/required/enum/items/default shape
// every builtin tool already speaks. This is a best-effort projection, not a
// full JSON Schema implementation: [tool.Schema] (shared by every builtin,
// not introduced by this package) has no representation for oneOf/anyOf,
// patternProperties, additionalProperties, or per-nested-object required
// lists, so those constructs are silently dropped rather than rejected —
// the same limitation every builtin tool's schema already lives with. A
// missing or unparseable schema degrades to an empty object schema (a tool
// that takes no meaningful input still needs SOME valid [tool.Schema]) rather
// than failing the whole projection over one tool's schema.
func schemaFromJSON(raw json.RawMessage) tool.Schema {
	if len(raw) == 0 {
		return tool.Schema{Type: "object"}
	}
	var wire wireSchema
	if err := json.Unmarshal(raw, &wire); err != nil {
		return tool.Schema{Type: "object"}
	}
	return wire.toSchema()
}

// wireSchema and wireProperty mirror the subset of JSON Schema draft
// 2020-12 that [tool.Schema]/[tool.Property] can represent.
type wireSchema struct {
	Type       string                  `json:"type"`
	Properties map[string]wireProperty `json:"properties"`
	Required   []string                `json:"required"`
}

type wireProperty struct {
	Type        string                  `json:"type"`
	Description string                  `json:"description"`
	Enum        []string                `json:"enum"`
	Items       *wireProperty           `json:"items"`
	Properties  map[string]wireProperty `json:"properties"`
	Default     any                     `json:"default"`
}

func (w wireSchema) toSchema() tool.Schema {
	s := tool.Schema{Type: w.Type, Required: w.Required}
	if s.Type == "" {
		s.Type = "object"
	}
	if len(w.Properties) > 0 {
		s.Properties = make(map[string]tool.Property, len(w.Properties))
		for name, p := range w.Properties {
			s.Properties[name] = p.toProperty()
		}
	}
	return s
}

func (w wireProperty) toProperty() tool.Property {
	p := tool.Property{Type: w.Type, Description: w.Description, Enum: w.Enum, Default: w.Default}
	if w.Items != nil {
		item := w.Items.toProperty()
		p.Items = &item
	}
	if len(w.Properties) > 0 {
		p.Properties = make(map[string]tool.Property, len(w.Properties))
		for name, sub := range w.Properties {
			p.Properties[name] = sub.toProperty()
		}
	}
	return p
}
