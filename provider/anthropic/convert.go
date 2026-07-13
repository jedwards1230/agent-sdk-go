package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// messagesRequest is the wire shape of a POST /v1/messages body. Streaming is
// always enabled; max_tokens is required by the API.
type messagesRequest struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	System      []systemBlock   `json:"system,omitempty"`
	Messages    []wireMessage   `json:"messages"`
	Tools       []wireTool      `json:"tools,omitempty"`
	Thinking    *thinkingConfig `json:"thinking,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	Stream      bool            `json:"stream"`
}

// systemBlock is one entry in the system prompt array. The API also accepts a
// bare string, but the array form lets OAuth requests carry the mandatory Claude
// Code identity block ahead of the caller's prompt.
type systemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// wireMessage is a conversation entry in Anthropic wire form.
type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

// wireBlock is a content block in Anthropic wire form. Which fields are set
// depends on Type; omitempty keeps each variant to its own fields.
type wireBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// wireTool is a tool specification in Anthropic wire form.
type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// thinkingConfig enables extended thinking with a token budget.
type thinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// emptySchema is the fallback input schema for a tool that declares none: the
// API requires input_schema to be a JSON Schema object.
var emptySchema = json.RawMessage(`{"type":"object"}`)

// minThinkingBudget is the Anthropic-mandated floor for an extended-thinking
// budget; a smaller (or zero) budget is raised to it.
const minThinkingBudget = 1024

// buildBody projects a provider.Request down to the Messages API wire format and
// returns it as a ready-to-send JSON reader. credKind selects whether the OAuth
// identity system block is prepended.
func (p *Provider) buildBody(req provider.Request, credKind provider.CredKind) (io.Reader, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	wire := messagesRequest{
		Model:    model,
		System:   p.buildSystem(req.System, credKind),
		Messages: convertMessages(req.Messages),
		Tools:    convertTools(req.Tools),
		Stream:   true,
	}

	wire.MaxTokens = p.maxTokens(model, req.Params)

	if req.Params.Thinking.Enabled {
		budget := req.Params.Thinking.BudgetTokens
		if budget < minThinkingBudget {
			budget = minThinkingBudget
		}
		// max_tokens must exceed the thinking budget; leave room for output.
		if wire.MaxTokens <= budget {
			wire.MaxTokens = budget + defaultMaxTokens
		}
		wire.Thinking = &thinkingConfig{Type: "enabled", BudgetTokens: budget}
		// The API rejects an explicit temperature alongside extended thinking.
	} else if req.Params.Temperature != nil {
		wire.Temperature = req.Params.Temperature
	}

	buf, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}
	return bytes.NewReader(buf), nil
}

// maxTokens resolves the output cap: the caller's value, else the model's
// registry max, else a conservative default.
func (p *Provider) maxTokens(model string, params provider.Params) int {
	if params.MaxTokens > 0 {
		return params.MaxTokens
	}
	if info, ok := provider.Lookup(model); ok && info.MaxOutput > 0 {
		return info.MaxOutput
	}
	return defaultMaxTokens
}

// buildSystem assembles the system prompt block array. For OAuth credentials the
// mandatory Claude Code identity block is prepended before the caller's prompt.
func (p *Provider) buildSystem(system string, credKind provider.CredKind) []systemBlock {
	var blocks []systemBlock
	if credKind == provider.CredOAuth {
		blocks = append(blocks, systemBlock{Type: "text", Text: systemIdentity})
	}
	if system != "" {
		blocks = append(blocks, systemBlock{Type: "text", Text: system})
	}
	return blocks
}

// convertMessages maps the internal message model to Anthropic wire messages.
// A reasoning block is replayed as a signed thinking block when it carries the
// Anthropic signature in its Meta (required when extended thinking and tool use
// combine across turns); an unsigned reasoning block is dropped, since the API
// rejects a thinking block without its signature.
func convertMessages(msgs []provider.Message) []wireMessage {
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		blocks := make([]wireBlock, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case provider.BlockText:
				blocks = append(blocks, wireBlock{Type: "text", Text: b.Text})
			case provider.BlockReasoning:
				// Replay a reasoning block as a signed thinking block only when it
				// carries BOTH the Anthropic signature AND its thinking text. The
				// API requires a thinking block's `thinking` field (and the
				// signature signs that text), so a signature-only block — Anthropic
				// streamed a signature_delta with no thinking_delta text, folding to
				// an empty-text reasoning block — cannot be replayed as a valid
				// thinking block: emitting one drops the required `thinking` field
				// (wireBlock.Thinking is omitempty on the shared union) and the API
				// rejects it with "messages.N.content.M.thinking.thinking: Field
				// required". Drop it, exactly as unsigned reasoning is dropped.
				// (OpenAI-style empty-summary reasoning, replayed via
				// encrypted_content Meta, never reaches this Anthropic encoder.)
				if sig := b.Meta[metaSignatureKey]; sig != "" && b.Text != "" {
					blocks = append(blocks, wireBlock{Type: "thinking", Thinking: b.Text, Signature: sig})
				}
			case provider.BlockToolUse:
				input := b.ToolInput
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, wireBlock{
					Type: "tool_use", ID: b.ToolUseID, Name: b.ToolName, Input: input,
				})
			case provider.BlockToolResult:
				blocks = append(blocks, wireBlock{
					Type: "tool_result", ToolUseID: b.ToolUseID, Content: b.ToolResult, IsError: b.IsError,
				})
			case provider.BlockImage:
				// Image blocks are an M1 placeholder, omitted from the request.
			}
		}
		if len(blocks) == 0 {
			continue
		}
		out = append(out, wireMessage{Role: string(m.Role), Content: blocks})
	}
	return out
}

// convertTools maps tool specs to Anthropic wire tools, substituting an empty
// object schema when a spec declares none.
func convertTools(tools []provider.ToolSpec) []wireTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]wireTool, 0, len(tools))
	for _, t := range tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = emptySchema
		}
		out = append(out, wireTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return out
}

// apiError reads a non-2xx response and returns an [Error] carrying the status
// and any parsed Anthropic error payload. The body is drained and closed.
func apiError(resp *http.Response) error {
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	// Drain any remainder past the cap so the connection can return to the pool.
	_, _ = io.Copy(io.Discard, resp.Body)

	e := &Error{StatusCode: resp.StatusCode}
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &parsed) == nil && parsed.Error.Message != "" {
		e.Type = parsed.Error.Type
		e.Message = parsed.Error.Message
	} else {
		e.Message = string(bytes.TrimSpace(data))
	}
	return e
}

// Error is an Anthropic API error carrying the HTTP status and, when present,
// the parsed error type and message.
type Error struct {
	StatusCode int
	Type       string
	Message    string
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("anthropic: http %d: %s: %s", e.StatusCode, e.Type, e.Message)
	}
	return fmt.Sprintf("anthropic: http %d: %s", e.StatusCode, e.Message)
}
