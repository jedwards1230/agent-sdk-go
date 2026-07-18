package acp

import (
	"encoding/json"
	"fmt"
)

// ConfigOptionValue is the value carried by a session/set_config_option
// request: either a boolean toggle or a select value id. It is ACP's config
// value union, flattened onto the request object — {"type":"boolean",
// "value":true} for a boolean and {"value":"<id>"} (no "type") for a value id.
// Only the two types in this package implement it, so a consuming application
// can exhaustively type-switch on a decoded [SetConfigOptionRequest.Value].
type ConfigOptionValue interface {
	// configValueType returns the wire "type" discriminator, or "" for the
	// type-less value-id form.
	configValueType() string
}

// BooleanValue is a boolean session-config value (wire "type":"boolean").
type BooleanValue struct {
	// Value is the boolean the option is being set to.
	Value bool
}

func (BooleanValue) configValueType() string { return "boolean" }

// SelectValue is a select session-config value id: ACP's type-less value form
// whose "value" is the chosen option id (used by select-style options).
type SelectValue struct {
	// Value is the chosen option's id.
	Value string
}

func (SelectValue) configValueType() string { return "" }

// SetConfigOptionRequest is the payload of a session/set_config_option request:
// set the config option ConfigID on session SessionID to Value. This is the
// stable ACP v1 mechanism for model/mode/thought-level selectors and boolean
// toggles; the SDK carries ConfigID and Value opaquely and never interprets
// their semantics — a consuming application maps them to its own ops.
type SetConfigOptionRequest struct {
	// SessionID identifies the session whose option is being set.
	SessionID string
	// ConfigID names the configuration option to set.
	ConfigID string
	// Value is the new value: a [BooleanValue] or a [SelectValue].
	Value ConfigOptionValue
}

// MarshalJSON encodes {"sessionId","configId",...value}, flattening the value:
// {"type":"boolean","value":<bool>} for a boolean and {"value":"<id>"} (no
// "type") for a value id.
func (r SetConfigOptionRequest) MarshalJSON() ([]byte, error) {
	wire := struct {
		SessionID string `json:"sessionId"`
		ConfigID  string `json:"configId"`
		Type      string `json:"type,omitempty"`
		Value     any    `json:"value"`
	}{SessionID: r.SessionID, ConfigID: r.ConfigID}

	switch v := r.Value.(type) {
	case BooleanValue:
		wire.Type = "boolean"
		wire.Value = v.Value
	case SelectValue:
		wire.Value = v.Value
	case nil:
		return nil, fmt.Errorf("acp: marshal SetConfigOptionRequest %q: nil value", r.ConfigID)
	default:
		return nil, fmt.Errorf("acp: marshal SetConfigOptionRequest %q: unknown value type %T", r.ConfigID, r.Value)
	}
	return json.Marshal(wire)
}

// UnmarshalJSON decodes {"sessionId","configId","type"?,"value"}, resolving the
// value's concrete [ConfigOptionValue]. Per the ACP schema, type "boolean"
// yields a [BooleanValue]; an absent or unknown "type" with a string payload
// yields a [SelectValue].
func (r *SetConfigOptionRequest) UnmarshalJSON(data []byte) error {
	var wire struct {
		SessionID string          `json:"sessionId"`
		ConfigID  string          `json:"configId"`
		Type      string          `json:"type"`
		Value     json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("acp: decode SetConfigOptionRequest: %w", err)
	}
	r.SessionID = wire.SessionID
	r.ConfigID = wire.ConfigID

	// configId and value are required by the ACP schema; reject a missing or
	// null value (which json would otherwise decode to a silent zero value)
	// rather than accepting an under-specified request.
	if wire.ConfigID == "" {
		return fmt.Errorf("acp: set_config_option request missing required configId")
	}
	if len(wire.Value) == 0 || string(wire.Value) == "null" {
		return fmt.Errorf("acp: set_config_option request %q missing required value", wire.ConfigID)
	}

	switch wire.Type {
	case "boolean":
		var b bool
		if err := json.Unmarshal(wire.Value, &b); err != nil {
			return fmt.Errorf("acp: decode SetConfigOptionRequest %q boolean value: %w", wire.ConfigID, err)
		}
		r.Value = BooleanValue{Value: b}
	default:
		// Type-less (or unknown-type) value-id form; the value is a string id.
		var s string
		if err := json.Unmarshal(wire.Value, &s); err != nil {
			return fmt.Errorf("acp: decode SetConfigOptionRequest %q value id: %w", wire.ConfigID, err)
		}
		r.Value = SelectValue{Value: s}
	}
	return nil
}

// ConfigOptionCategory is the optional semantic category of a session config
// option — a UX hint only. Per the ACP schema a client must handle a missing
// or unknown category gracefully; the category never affects correctness.
type ConfigOptionCategory string

// The reserved ACP config-option categories. Categories beginning with "_" are
// free for custom use.
const (
	ConfigCategoryMode         ConfigOptionCategory = "mode"
	ConfigCategoryModel        ConfigOptionCategory = "model"
	ConfigCategoryModelConfig  ConfigOptionCategory = "model_config"
	ConfigCategoryThoughtLevel ConfigOptionCategory = "thought_level"
)

// SelectOption is one selectable value of a select-style [ConfigOption].
type SelectOption struct {
	// Value is the option value's id.
	Value string `json:"value"`
	// Name is the human-readable label for the value.
	Name string `json:"name"`
	// Description is an optional description for the client to display.
	Description string `json:"description,omitempty"`
}

// ConfigOptionKind is the type-specific payload of a [ConfigOption], flattened
// onto the option object under a "type" discriminator. Only [SelectKind] and
// [BooleanKind] implement it.
type ConfigOptionKind interface {
	// configKindType returns the wire "type" discriminator ("select" or
	// "boolean").
	configKindType() string
}

// SelectKind is a single-value selector (dropdown) config option.
//
// Grouped select options (the ACP grouped variant) are not modeled yet; this
// carries the flat, ungrouped option list, which is additive-compatible with a
// later grouped variant.
type SelectKind struct {
	// CurrentValue is the id of the currently selected [SelectOption].
	CurrentValue string
	// Options is the set of selectable values.
	Options []SelectOption
}

func (SelectKind) configKindType() string { return "select" }

// BooleanKind is a boolean on/off toggle config option.
type BooleanKind struct {
	// CurrentValue is the current boolean value.
	CurrentValue bool
}

func (BooleanKind) configKindType() string { return "boolean" }

// ConfigOption is one entry in a [SetConfigOptionResponse]: a config selector
// and its current value. Kind carries the type-specific payload flattened onto
// the object under a "type" discriminator.
type ConfigOption struct {
	// ID uniquely identifies the option.
	ID string
	// Name is the human-readable label.
	Name string
	// Description is an optional description for the client to display.
	Description string
	// Category is an optional semantic category (UX hint only).
	Category ConfigOptionCategory
	// Kind is the type-specific payload: a [SelectKind] or [BooleanKind].
	Kind ConfigOptionKind
}

// MarshalJSON encodes {"id","name","description"?,"category"?, ...kind}, with
// the kind's fields flattened onto the object under a "type" discriminator.
func (o ConfigOption) MarshalJSON() ([]byte, error) {
	// A map keeps the flattened, kind-dependent shape simple; encoding/json
	// emits its keys in sorted order, so the output is deterministic.
	wire := map[string]any{
		"id":   o.ID,
		"name": o.Name,
	}
	if o.Description != "" {
		wire["description"] = o.Description
	}
	if o.Category != "" {
		wire["category"] = o.Category
	}

	switch k := o.Kind.(type) {
	case SelectKind:
		wire["type"] = k.configKindType()
		wire["currentValue"] = k.CurrentValue
		opts := k.Options
		if opts == nil {
			opts = []SelectOption{}
		}
		wire["options"] = opts
	case BooleanKind:
		wire["type"] = k.configKindType()
		wire["currentValue"] = k.CurrentValue
	case nil:
		return nil, fmt.Errorf("acp: marshal ConfigOption %q: nil kind", o.ID)
	default:
		return nil, fmt.Errorf("acp: marshal ConfigOption %q: unknown kind %T", o.ID, o.Kind)
	}
	return json.Marshal(wire)
}

// UnmarshalJSON decodes a [ConfigOption], resolving its concrete
// [ConfigOptionKind] from the "type" discriminator.
func (o *ConfigOption) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID           string               `json:"id"`
		Name         string               `json:"name"`
		Description  string               `json:"description"`
		Category     ConfigOptionCategory `json:"category"`
		Type         string               `json:"type"`
		CurrentValue json.RawMessage      `json:"currentValue"`
		Options      []SelectOption       `json:"options"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("acp: decode ConfigOption: %w", err)
	}
	o.ID = wire.ID
	o.Name = wire.Name
	o.Description = wire.Description
	o.Category = wire.Category

	switch wire.Type {
	case "select":
		var cv string
		if err := json.Unmarshal(wire.CurrentValue, &cv); err != nil {
			return fmt.Errorf("acp: decode ConfigOption %q select currentValue: %w", wire.ID, err)
		}
		o.Kind = SelectKind{CurrentValue: cv, Options: wire.Options}
	case "boolean":
		var cv bool
		if err := json.Unmarshal(wire.CurrentValue, &cv); err != nil {
			return fmt.Errorf("acp: decode ConfigOption %q boolean currentValue: %w", wire.ID, err)
		}
		o.Kind = BooleanKind{CurrentValue: cv}
	default:
		return fmt.Errorf("acp: decode ConfigOption %q: unknown type %q", wire.ID, wire.Type)
	}
	return nil
}

// SetConfigOptionResponse is the payload of a session/set_config_option
// response: the full set of config options and their current values after the
// change. Callers pass a non-nil (possibly empty) slice so it marshals as "[]"
// rather than "null".
type SetConfigOptionResponse struct {
	// ConfigOptions is the complete set of config options after the change.
	ConfigOptions []ConfigOption `json:"configOptions"`
}

// UnmarshalJSON decodes a [SetConfigOptionResponse], skipping any option entry
// it cannot parse. The ACP v1 schema annotates the response option array with
// x-deserialize-skip-invalid-items, so a client decoding an agent's response
// drops option kinds it does not understand (a forward-compat agent adding a
// new option type) rather than failing the whole decode. Each entry is decoded
// with the strict [ConfigOption.UnmarshalJSON]; entries it rejects are dropped
// and the known ones kept.
func (r *SetConfigOptionResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		ConfigOptions []json.RawMessage `json:"configOptions"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("acp: decode SetConfigOptionResponse: %w", err)
	}
	opts := make([]ConfigOption, 0, len(wire.ConfigOptions))
	for _, raw := range wire.ConfigOptions {
		var opt ConfigOption
		if err := json.Unmarshal(raw, &opt); err != nil {
			// Unknown/unparseable option kind: skip it (skip-invalid-items).
			continue
		}
		opts = append(opts, opt)
	}
	r.ConfigOptions = opts
	return nil
}
