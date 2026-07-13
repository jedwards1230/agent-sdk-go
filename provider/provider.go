// Package provider defines the LLM provider interface and the normalized event
// stream the agent loop consumes. A provider turns a [Request] — the internal
// message model projected down at the call boundary — into a [StreamHandle] of
// [StreamEvent] values; the loop translates that stream into the typed event
// contract.
//
// The interface is provider-agnostic by design: Anthropic and OpenAI are peers
// with full parity (streaming, tool calls, reasoning, usage, API-key + OAuth
// credentials). Nothing here is vendor-specific.
package provider

import (
	"context"
	"encoding/json"
	"io"
	"iter"
	"maps"
)

// Provider is an LLM backend. Stream begins one model turn and returns a
// normalized stream; Info reports the model's metadata (context window, limits,
// pricing, capabilities). Implementations must be safe for the caller to range
// a returned handle to completion and Close it.
type Provider interface {
	// Stream starts one model call. Events arrive on the returned handle; the
	// final StreamFinished event carries the settled stop reason and usage.
	Stream(ctx context.Context, req Request) (StreamHandle, error)
	// Info returns metadata for the model this provider is configured to use.
	Info() ModelInfo
}

// Request is the input to one model turn: the internal message model projected
// down at the call boundary. The session owns a richer history than any provider
// speaks; project down here (convertToLLM), never up.
type Request struct {
	// Model is the model identifier, or empty to use the provider default.
	Model string
	// System is the system prompt, or empty.
	System string
	// Messages is the conversation so far, oldest first.
	Messages []Message
	// Tools are the tool specifications offered to the model.
	Tools []ToolSpec
	// Params carries sampling and reasoning controls.
	Params Params
}

// Role identifies the speaker of a [Message].
type Role string

// The conversation roles.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// Message is one conversation entry. Content is a sequence of typed blocks so a
// single message can mix text, reasoning, tool calls, and tool results.
type Message struct {
	// Role is the speaker.
	Role Role
	// Content is the ordered list of content blocks in the message.
	Content []ContentBlock
}

// UserText builds a user message carrying a single text block.
func UserText(text string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{TextBlock(text)}}
}

// AssistantText builds an assistant message carrying a single text block.
func AssistantText(text string) Message {
	return Message{Role: RoleAssistant, Content: []ContentBlock{TextBlock(text)}}
}

// Text returns the concatenation of the message's text blocks, ignoring other
// block kinds. It is a convenience for the common single-text-block case.
func (m Message) Text() string {
	var s string
	for _, b := range m.Content {
		if b.Type == BlockText {
			s += b.Text
		}
	}
	return s
}

// BlockType tags the variant of a [ContentBlock].
type BlockType string

// The content block variants. Which fields are meaningful is determined by Type.
const (
	// BlockText is assistant- or user-authored text. Text is set.
	BlockText BlockType = "text"
	// BlockReasoning is model reasoning/thinking content. Text is set.
	BlockReasoning BlockType = "reasoning"
	// BlockToolUse is a tool invocation. ToolUseID, ToolName, ToolInput are set.
	BlockToolUse BlockType = "tool_use"
	// BlockToolResult is a tool's result. ToolUseID, ToolResult, IsError are set.
	BlockToolResult BlockType = "tool_result"
	// BlockImage is an image placeholder. M1 carries the reference only.
	BlockImage BlockType = "image"
)

// ContentBlock is one item in a [Message]'s content. It is a flat tagged union:
// Type selects which fields are meaningful.
type ContentBlock struct {
	Type BlockType `json:"type"`
	// Text is set for BlockText and BlockReasoning.
	Text string `json:"text,omitempty"`
	// ToolUseID identifies a tool call; on BlockToolResult it references the
	// BlockToolUse it answers.
	ToolUseID string `json:"tool_use_id,omitempty"`
	// ToolName is the invoked tool's name (BlockToolUse).
	ToolName string `json:"tool_name,omitempty"`
	// ToolInput is the tool's JSON arguments (BlockToolUse).
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	// ToolResult is the tool's textual result (BlockToolResult).
	ToolResult string `json:"tool_result,omitempty"`
	// IsError marks a failed tool result (BlockToolResult).
	IsError bool `json:"is_error,omitempty"`
	// ImageRef is an opaque image reference (BlockImage); M1 placeholder only.
	ImageRef string `json:"image_ref,omitempty"`
	// Meta carries opaque, provider-namespaced per-block metadata that must
	// round-trip verbatim through the session journal and be replayed on the
	// outgoing projection. It exists so a provider can preserve state it needs
	// on a later turn — e.g. Anthropic's reasoning-block cryptographic
	// `signature` (required when extended thinking and tool use combine) or
	// OpenAI's reasoning item id. Keys are provider-namespaced
	// (e.g. "anthropic.signature", "openai.item_id"); the loop and session
	// never interpret them.
	Meta map[string]string `json:"meta,omitempty"`
}

// TextBlock builds a text content block.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: BlockText, Text: text}
}

// ReasoningBlock builds a reasoning content block.
func ReasoningBlock(text string) ContentBlock {
	return ContentBlock{Type: BlockReasoning, Text: text}
}

// ToolUseBlock builds a tool-use content block.
func ToolUseBlock(id, name string, input json.RawMessage) ContentBlock {
	return ContentBlock{Type: BlockToolUse, ToolUseID: id, ToolName: name, ToolInput: input}
}

// ToolResultBlock builds a tool-result content block referencing a tool-use id.
func ToolResultBlock(toolUseID, result string, isError bool) ContentBlock {
	return ContentBlock{Type: BlockToolResult, ToolUseID: toolUseID, ToolResult: result, IsError: isError}
}

// ToolSpec describes a tool offered to the model: its name, a human/model-facing
// description, and a JSON Schema for its input.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// Params carries per-turn sampling and reasoning controls. Providers use what
// their API supports and ignore the rest (documented degradation).
type Params struct {
	// MaxTokens caps output tokens; 0 lets the provider choose a default.
	MaxTokens int
	// Temperature, when non-nil, requests a sampling temperature.
	Temperature *float64
	// Thinking configures extended reasoning.
	Thinking Thinking
}

// Thinking configures extended reasoning across vendors: Anthropic uses a token
// budget, OpenAI a reasoning effort level. A provider uses whichever applies.
type Thinking struct {
	// Enabled turns reasoning on.
	Enabled bool
	// BudgetTokens is the Anthropic extended-thinking token budget (0 = auto).
	BudgetTokens int
	// Effort is the OpenAI reasoning effort: "low", "medium", or "high".
	Effort string
}

// StopReason is a normalized turn-termination reason. Providers map their raw
// stop reason onto one of these; the underlying string type serializes as-is.
type StopReason string

// The normalized stop reasons.
const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	// StopMaxTurns is the loop's own terminal reason when it stops a run at the
	// model-call iteration cap while the model is still requesting tools. No
	// provider reports it; the loop emits it so a client (e.g. the ACP
	// projection) sees a settled, non-tool_use turn end instead of hanging.
	StopMaxTurns     StopReason = "max_turns"
	StopStopSequence StopReason = "stop_sequence"
	StopRefusal      StopReason = "refusal"
	StopError        StopReason = "error"
	StopCancelled    StopReason = "cancelled"
)

// Usage is normalized token accounting for a turn. Raw retains provider-reported
// fields verbatim for audit; because it is a map, compare Usage with [Usage.Equal]
// rather than ==.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	// Raw holds the provider-reported token fields verbatim, retained for audit
	// (adapters may mirror all raw counters here, including ones that also map
	// onto the normalized fields above). Nil when unused.
	Raw map[string]int `json:"raw,omitempty"`
}

// Add returns the element-wise sum of the normalized counters. Raw maps are
// merged (o wins on key collisions); the result's Raw is nil if both are empty.
func (u Usage) Add(o Usage) Usage {
	sum := Usage{
		InputTokens:      u.InputTokens + o.InputTokens,
		OutputTokens:     u.OutputTokens + o.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens + o.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + o.CacheWriteTokens,
	}
	if len(u.Raw)+len(o.Raw) > 0 {
		sum.Raw = make(map[string]int, len(u.Raw)+len(o.Raw))
		maps.Copy(sum.Raw, u.Raw)
		maps.Copy(sum.Raw, o.Raw)
	}
	return sum
}

// Equal reports whether the normalized counters and the Raw map are equal.
func (u Usage) Equal(o Usage) bool {
	return u.InputTokens == o.InputTokens &&
		u.OutputTokens == o.OutputTokens &&
		u.CacheReadTokens == o.CacheReadTokens &&
		u.CacheWriteTokens == o.CacheWriteTokens &&
		maps.Equal(u.Raw, o.Raw)
}

// IsZero reports whether every normalized counter is zero and Raw is empty.
func (u Usage) IsZero() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 &&
		u.CacheReadTokens == 0 && u.CacheWriteTokens == 0 && len(u.Raw) == 0
}

// StreamEventType tags the variant of a [StreamEvent].
type StreamEventType int

// The normalized stream event variants.
const (
	// StreamTextDelta is an incremental chunk of assistant text. Text is set.
	StreamTextDelta StreamEventType = iota
	// StreamReasoningDelta is an incremental chunk of reasoning. Text is set.
	StreamReasoningDelta
	// StreamToolCallStart announces a tool call. Tool.ID and Tool.Name are set;
	// Tool.Input may carry initial (often empty) arguments.
	StreamToolCallStart
	// StreamToolCallDelta carries a fragment of a tool call's streaming input.
	// Tool.ID and Tool.Delta are set.
	StreamToolCallDelta
	// StreamToolCallEnd closes a tool call. Tool.ID and the assembled Tool.Input
	// are set.
	StreamToolCallEnd
	// StreamFinished terminates the turn. StopReason and Usage are set.
	StreamFinished
)

// StreamEvent is one item in a provider's normalized stream. Which fields are
// meaningful is determined by Type (see [StreamEventType]).
type StreamEvent struct {
	Type StreamEventType
	// Text is set for StreamTextDelta and StreamReasoningDelta.
	Text string
	// Tool is set for StreamToolCall{Start,Delta,End}.
	Tool *ToolCall
	// StopReason and Usage are set for StreamFinished.
	StopReason StopReason
	Usage      Usage
	// Meta carries opaque, provider-namespaced metadata for the block the event
	// belongs to (a reasoning/text message or a tool call). The loop merges it
	// onto the assembled [ContentBlock.Meta], so a provider can surface e.g. a
	// reasoning signature to be replayed on a later turn. It is dropped on
	// StreamFinished.
	Meta map[string]string
}

// ToolCall is the tool-related payload of a stream event. A single struct serves
// all three tool events: Start carries ID+Name (Input may be partial/empty),
// Delta carries a fragment in Delta, and End carries the assembled Input.
type ToolCall struct {
	ID    string          // stable across Start → Delta → End
	Name  string          // set on Start and End
	Input json.RawMessage // partial/empty on Start; complete assembled args on End
	Delta string          // a fragment of the streaming arguments JSON (Delta)
}

// StreamHandle is a live provider stream. Call Next repeatedly until it returns
// [io.EOF]; the terminal StreamFinished event (before EOF) carries the settled
// stop reason and usage. Callers must Close every handle. Use [Iter] to consume
// a handle as a range-over-func iterator.
type StreamHandle interface {
	// Next returns the next event, or io.EOF when the stream ends.
	Next() (StreamEvent, error)
	// Close releases the stream's resources. It is safe to call more than once.
	Close() error
}

// Iter adapts a [StreamHandle] to a range-over-func iterator that yields each
// event and any error, stopping at io.EOF. It does not Close the handle; the
// caller retains that responsibility.
func Iter(h StreamHandle) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		for {
			ev, err := h.Next()
			if err == io.EOF {
				return
			}
			if err != nil {
				yield(StreamEvent{}, err)
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
	}
}
