package acp

import (
	"encoding/json"
	"fmt"
)

// StopReason is why a prompt turn ended.
type StopReason string

// The ACP stop reasons.
const (
	StopReasonEndTurn         StopReason = "end_turn"
	StopReasonMaxTokens       StopReason = "max_tokens"
	StopReasonMaxTurnRequests StopReason = "max_turn_requests"
	StopReasonRefusal         StopReason = "refusal"
	StopReasonCancelled       StopReason = "cancelled"
)

// PromptRequest is the payload of a session/prompt request. The default json
// struct-tag encoding marshals it (encoding/json calls MarshalJSON on each
// ContentBlock element automatically); decoding needs the custom
// UnmarshalJSON below since ContentBlock is an interface.
type PromptRequest struct {
	// SessionID identifies the session to prompt.
	SessionID string `json:"sessionId"`
	// Prompt is the ordered content blocks making up the prompt.
	Prompt []ContentBlock `json:"prompt"`
}

// UnmarshalJSON decodes {"sessionId":...,"prompt":[...]}, resolving each
// prompt block's concrete [ContentBlock] variant.
func (r *PromptRequest) UnmarshalJSON(data []byte) error {
	var wire struct {
		SessionID string            `json:"sessionId"`
		Prompt    []json.RawMessage `json:"prompt"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("acp: decode PromptRequest: %w", err)
	}
	blocks, err := unmarshalContentBlocks(wire.Prompt)
	if err != nil {
		return err
	}
	r.SessionID = wire.SessionID
	r.Prompt = blocks
	return nil
}

// PromptResponse is the payload of a session/prompt response.
type PromptResponse struct {
	// StopReason is why the turn ended.
	StopReason StopReason `json:"stopReason"`
}
