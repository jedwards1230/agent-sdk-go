package openai

import (
	"encoding/json"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// responsesRequest is the POST /responses body. Only the fields the SDK sets
// are modeled; the API tolerates omitted optionals.
type responsesRequest struct {
	Model           string           `json:"model"`
	Instructions    string           `json:"instructions,omitempty"`
	Input           []any            `json:"input"`
	Tools           []toolDef        `json:"tools,omitempty"`
	MaxOutputTokens int              `json:"max_output_tokens,omitempty"`
	Temperature     *float64         `json:"temperature,omitempty"`
	Reasoning       *reasoningConfig `json:"reasoning,omitempty"`
	Include         []string         `json:"include,omitempty"`
	Stream          bool             `json:"stream"`
	Store           bool             `json:"store"`
}

// reasoningConfig requests extended reasoning at an effort level and asks for
// streamed summaries.
type reasoningConfig struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

// toolDef is a Responses-API function tool. Unlike Chat Completions, the fields
// are flat (no nested "function" object).
type toolDef struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

// Input item shapes. The Responses API "input" array is heterogeneous:
// message items carry role+content parts, while function calls and their
// outputs are standalone top-level items.

type inputMessage struct {
	Type    string        `json:"type"`
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type functionCallItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type functionCallOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// reasoningItem replays a reasoning block carrying both its item id and
// encrypted content (see buildInput). Summary must marshal as [] (never
// null) — the API rejects a missing/null summary field.
type reasoningItem struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	EncryptedContent string `json:"encrypted_content"`
	Summary          []any  `json:"summary"`
}

// emptySchema is the default parameters for a tool that declares no input
// schema; the API requires a schema object for function tools.
var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// buildRequest projects a provider.Request down to the Responses-API wire body.
// reasoningModel reports whether the target model supports reasoning, which
// gates the reasoning config and temperature handling.
func buildRequest(model string, req provider.Request, reasoningModel bool) ([]byte, error) {
	out := responsesRequest{
		Model:           model,
		Instructions:    req.System,
		Input:           buildInput(req.Messages),
		Tools:           buildTools(req.Tools),
		MaxOutputTokens: req.Params.MaxTokens,
		Stream:          true,
		// The SDK owns session history; never let the backend persist turns.
		Store: false,
	}

	// Reasoning models take an effort level, not a temperature. Non-reasoning
	// models take temperature but reject reasoning config.
	if reasoningModel {
		if req.Params.Thinking.Enabled {
			effort := req.Params.Thinking.Effort
			if effort == "" {
				effort = "medium"
			}
			out.Reasoning = &reasoningConfig{Effort: effort, Summary: "auto"}
			// Opt into encrypted reasoning content so reasoning blocks can be
			// replayed on a later turn (see buildInput).
			out.Include = []string{"reasoning.encrypted_content"}
		}
	} else if req.Params.Temperature != nil {
		out.Temperature = req.Params.Temperature
	}

	return json.Marshal(out)
}

// buildInput flattens the internal message history into Responses input items.
// Consecutive textual blocks within a message collapse into one message item;
// tool-use and tool-result blocks become standalone function_call /
// function_call_output items in position.
func buildInput(messages []provider.Message) []any {
	items := make([]any, 0, len(messages))
	for _, m := range messages {
		role := wireRole(m.Role)
		partType := "input_text"
		if m.Role == provider.RoleAssistant {
			partType = "output_text"
		}

		var parts []contentPart
		flush := func() {
			if len(parts) > 0 {
				items = append(items, inputMessage{Type: "message", Role: role, Content: parts})
				parts = nil
			}
		}

		for _, b := range m.Content {
			switch b.Type {
			case provider.BlockText:
				parts = append(parts, contentPart{Type: partType, Text: b.Text})
			case provider.BlockToolUse:
				flush()
				args := string(b.ToolInput)
				if args == "" {
					args = "{}"
				}
				items = append(items, functionCallItem{
					Type: "function_call", CallID: b.ToolUseID, Name: b.ToolName, Arguments: args,
				})
			case provider.BlockToolResult:
				flush()
				items = append(items, functionCallOutputItem{
					Type: "function_call_output", CallID: b.ToolUseID, Output: b.ToolResult,
				})
			case provider.BlockReasoning:
				// Replay requires both the item id and its encrypted content
				// (opted in via the request's `include` field); a block missing
				// either is dropped rather than sent malformed.
				id, enc := b.Meta[metaItemID], b.Meta[metaEncrypted]
				if id == "" || enc == "" {
					continue
				}
				flush()
				items = append(items, reasoningItem{
					Type: "reasoning", ID: id, EncryptedContent: enc, Summary: []any{},
				})
			case provider.BlockImage:
				// Images are M1 placeholders and are dropped (documented).
			}
		}
		flush()
	}
	return items
}

// buildTools maps tool specs to Responses function tools.
func buildTools(specs []provider.ToolSpec) []toolDef {
	if len(specs) == 0 {
		return nil
	}
	tools := make([]toolDef, 0, len(specs))
	for _, s := range specs {
		params := s.InputSchema
		if len(params) == 0 {
			params = emptySchema
		}
		tools = append(tools, toolDef{
			Type: "function", Name: s.Name, Description: s.Description, Parameters: params, Strict: false,
		})
	}
	return tools
}

// wireRole maps an internal role to a Responses message role. System is folded
// to "developer", the Responses-API instruction role.
func wireRole(r provider.Role) string {
	switch r {
	case provider.RoleAssistant:
		return "assistant"
	case provider.RoleSystem:
		return "developer"
	default:
		return "user"
	}
}
