package acp

import (
	"encoding/json"
	"fmt"
)

// PermissionOptionKind classifies a [PermissionOption] the client can choose
// when answering a [RequestPermissionRequest].
type PermissionOptionKind string

// The ACP permission option kinds.
const (
	PermissionAllowOnce    PermissionOptionKind = "allow_once"
	PermissionAllowAlways  PermissionOptionKind = "allow_always"
	PermissionRejectOnce   PermissionOptionKind = "reject_once"
	PermissionRejectAlways PermissionOptionKind = "reject_always"
)

// PermissionOption is one choice offered to the client in a
// [RequestPermissionRequest].
type PermissionOption struct {
	// OptionID identifies the option; a [RequestPermissionResponse] with a
	// selected outcome references it.
	OptionID string `json:"optionId"`
	// Name is a human-readable label for the option.
	Name string `json:"name"`
	// Kind classifies the option's effect.
	Kind PermissionOptionKind `json:"kind"`
}

// RequestPermissionRequest is the payload of a session/request_permission
// request, sent agent-to-client to ask the user to decide a tool call.
type RequestPermissionRequest struct {
	// SessionID identifies the session the tool call belongs to.
	SessionID string `json:"sessionId"`
	// ToolCall describes the call awaiting a decision.
	ToolCall ToolCallUpdate `json:"toolCall"`
	// Options are the choices offered to the client.
	Options []PermissionOption `json:"options"`
}

// PermissionOutcome is the tagged union carried by a
// [RequestPermissionResponse]: either the client selected an option, or the
// request was cancelled (e.g. because the underlying turn was interrupted).
type PermissionOutcome interface {
	// Outcome returns the variant's "outcome" discriminator value.
	Outcome() string

	json.Marshaler
}

// PermissionOutcomeSelected reports the option the client chose.
type PermissionOutcomeSelected struct {
	// OptionID is the chosen [PermissionOption.OptionID].
	OptionID string
}

// Outcome returns "selected".
func (PermissionOutcomeSelected) Outcome() string { return "selected" }

// MarshalJSON encodes {"outcome":"selected","optionId":...}.
func (o PermissionOutcomeSelected) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Outcome  string `json:"outcome"`
		OptionID string `json:"optionId"`
	}{o.Outcome(), o.OptionID})
}

// PermissionOutcomeAmended reports that the client approved the call after
// editing its input: it selected an allow option AND supplied replacement tool
// input to run in place of the model's original arguments. It is the
// amend-before-approve variant (a consuming TUI renders it as "Tab" on the
// approval row). The chosen option's [PermissionOptionKind] still decides
// whether the amended allow is remembered, resolved by the daemon against the
// original option set the same way a plain [PermissionOutcomeSelected] is.
type PermissionOutcomeAmended struct {
	// OptionID is the chosen allow [PermissionOption.OptionID].
	OptionID string
	// RawInput is the replacement tool input the call runs with instead of the
	// model's original arguments.
	RawInput json.RawMessage
}

// Outcome returns "amended".
func (PermissionOutcomeAmended) Outcome() string { return "amended" }

// MarshalJSON encodes {"outcome":"amended","optionId":...,"rawInput":...}.
func (o PermissionOutcomeAmended) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Outcome  string          `json:"outcome"`
		OptionID string          `json:"optionId"`
		RawInput json.RawMessage `json:"rawInput"`
	}{o.Outcome(), o.OptionID, o.RawInput})
}

// PermissionOutcomeCancelled reports that the permission request was
// cancelled before the client answered.
type PermissionOutcomeCancelled struct{}

// Outcome returns "cancelled".
func (PermissionOutcomeCancelled) Outcome() string { return "cancelled" }

// MarshalJSON encodes {"outcome":"cancelled"}.
func (o PermissionOutcomeCancelled) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Outcome string `json:"outcome"`
	}{o.Outcome()})
}

// UnmarshalPermissionOutcome decodes a [PermissionOutcome] from its "outcome"
// discriminator.
func UnmarshalPermissionOutcome(data []byte) (PermissionOutcome, error) {
	var disc struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(data, &disc); err != nil {
		return nil, fmt.Errorf("acp: decode permission outcome: %w", err)
	}
	switch disc.Outcome {
	case "selected":
		var v struct {
			OptionID string `json:"optionId"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("acp: decode selected outcome: %w", err)
		}
		return PermissionOutcomeSelected{OptionID: v.OptionID}, nil
	case "amended":
		var v struct {
			OptionID string          `json:"optionId"`
			RawInput json.RawMessage `json:"rawInput"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("acp: decode amended outcome: %w", err)
		}
		return PermissionOutcomeAmended{OptionID: v.OptionID, RawInput: v.RawInput}, nil
	case "cancelled":
		return PermissionOutcomeCancelled{}, nil
	default:
		return nil, fmt.Errorf("acp: unknown permission outcome %q", disc.Outcome)
	}
}

// RequestPermissionResponse is the payload of a session/request_permission
// response, sent client-to-agent with the resolved [PermissionOutcome].
type RequestPermissionResponse struct {
	// Outcome is the resolved outcome.
	Outcome PermissionOutcome
}

// MarshalJSON encodes {"outcome":...}.
func (r RequestPermissionResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Outcome PermissionOutcome `json:"outcome"`
	}{r.Outcome})
}

// UnmarshalJSON decodes {"outcome":...}, resolving the outcome's concrete
// [PermissionOutcome] variant.
func (r *RequestPermissionResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		Outcome json.RawMessage `json:"outcome"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("acp: decode RequestPermissionResponse: %w", err)
	}
	outcome, err := UnmarshalPermissionOutcome(wire.Outcome)
	if err != nil {
		return err
	}
	r.Outcome = outcome
	return nil
}
