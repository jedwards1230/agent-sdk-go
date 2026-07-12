// Package provider defines the LLM provider interface and the normalized event
// stream the agent loop consumes. A provider turns a [Request] into a [Stream]
// of [StreamEvent] values; the session package translates that stream into the
// typed event contract.
package provider

import "context"

// Provider is an LLM backend. Stream begins one model turn and returns a
// normalized event stream. Implementations must be safe for the caller to
// range to completion and Close.
type Provider interface {
	Stream(ctx context.Context, req Request) (Stream, error)
}

// Request is the input to one model turn. M0 carries the essentials; later
// milestones extend it (tools, system prompt, sampling params).
type Request struct {
	// Model is the model identifier, or empty to use the provider default.
	Model string
	// Messages is the conversation so far, oldest first.
	Messages []Message
}

// Message is one conversation entry.
type Message struct {
	// Role is the speaker: "user", "assistant", or "system".
	Role string
	// Content is the message text.
	Content string
}

// Usage is normalized token accounting for a turn.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// StreamEventType tags the variant of a [StreamEvent].
type StreamEventType int

// The normalized stream event variants.
const (
	// StreamText is an incremental chunk of assistant text. Text is set.
	StreamText StreamEventType = iota
	// StreamReasoning is an incremental chunk of reasoning. Text is set.
	StreamReasoning
	// StreamToolCall is a tool-call chunk. ToolCall is set. Not exercised at M0.
	StreamToolCall
	// StreamUsage reports token usage for the turn. Usage is set.
	StreamUsage
	// StreamStop reports the turn's stop reason. StopReason is set.
	StreamStop
)

// StreamEvent is one item in a provider's normalized stream. Which fields are
// meaningful is determined by Type (see [StreamEventType]).
type StreamEvent struct {
	Type       StreamEventType
	Text       string
	Usage      Usage
	StopReason string
	ToolCall   *ToolCall
}

// ToolCall is a normalized tool invocation carried by a StreamToolCall event.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Stream is an iterator over a provider's normalized events. Next returns
// [io.EOF] when the stream is exhausted. Callers must Close every stream.
type Stream interface {
	// Next returns the next event, or io.EOF when the stream ends.
	Next() (StreamEvent, error)
	// Close releases the stream's resources. It is safe to call more than once.
	Close() error
}
