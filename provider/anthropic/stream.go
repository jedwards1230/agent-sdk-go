package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// streamHandle parses the Messages API SSE response into normalized provider
// events. Next drives the parser one normalized event at a time; several SSE
// frames (ping, message_start, content_block_start for text) produce no event
// and are consumed internally.
type streamHandle struct {
	ctx  context.Context
	body io.ReadCloser
	sse  *sseScanner

	// blocks tracks in-flight content blocks by their SSE index so tool-call
	// input fragments can be reassembled at content_block_stop.
	blocks map[int]*blockState

	usage      provider.Usage
	stopReason provider.StopReason
	seenDelta  bool // a message_delta set the stop reason / final usage

	finishedEmitted bool
	closed          bool
}

// blockState is the accumulated state of one streaming content block.
type blockState struct {
	kind     string          // "text", "thinking", "tool_use", …
	toolID   string          // tool_use id
	toolName string          // tool_use name
	input    strings.Builder // assembled tool-use input JSON
}

func newStreamHandle(ctx context.Context, body io.ReadCloser) *streamHandle {
	return &streamHandle{
		ctx:    ctx,
		body:   body,
		sse:    newSSEScanner(body),
		blocks: make(map[int]*blockState),
	}
}

// Next returns the next normalized event, or io.EOF when the stream is done.
func (h *streamHandle) Next() (provider.StreamEvent, error) {
	if h.finishedEmitted {
		return provider.StreamEvent{}, io.EOF
	}

	for {
		frame, err := h.sse.next()
		if err != nil {
			return h.handleReadError(err)
		}

		ev, emit, err := h.dispatch(frame)
		if err != nil {
			return provider.StreamEvent{}, err
		}
		if emit {
			return ev, nil
		}
	}
}

// handleReadError maps a read/scan error onto a terminal event. A clean EOF
// (the full response arrived) flushes a finished event with the settled stop
// reason, even if the context was cancelled in the same instant; a cancelled
// context on a non-EOF error yields a finished event with StopReason
// "cancelled" so partial usage is still accounted.
func (h *streamHandle) handleReadError(err error) (provider.StreamEvent, error) {
	if errors.Is(err, io.EOF) {
		return h.finish()
	}
	if ctxErr := h.ctx.Err(); ctxErr != nil {
		h.finishedEmitted = true
		return provider.StreamEvent{
			Type: provider.StreamFinished, StopReason: provider.StopCancelled, Usage: h.usage,
		}, nil
	}
	return provider.StreamEvent{}, err
}

// finish emits the single terminal finished event. On a second call it returns
// io.EOF. A stream that ended without any stop reason is reported as an error
// stop.
func (h *streamHandle) finish() (provider.StreamEvent, error) {
	if h.finishedEmitted {
		return provider.StreamEvent{}, io.EOF
	}
	h.finishedEmitted = true
	stop := h.stopReason
	if !h.seenDelta && stop == "" {
		stop = provider.StopError
	}
	return provider.StreamEvent{Type: provider.StreamFinished, StopReason: stop, Usage: h.usage}, nil
}

// sseEvent is the minimal envelope shared by every Messages API SSE frame; only
// the fields relevant to a given type are populated.
type sseEvent struct {
	Type         string          `json:"type"`
	Message      *sseMessage     `json:"message,omitempty"`
	Index        int             `json:"index,omitempty"`
	ContentBlock *sseBlock       `json:"content_block,omitempty"`
	Delta        *sseDelta       `json:"delta,omitempty"`
	Usage        *sseUsage       `json:"usage,omitempty"`
	Error        *sseErrorDetail `json:"error,omitempty"`
}

type sseMessage struct {
	Usage *sseUsage `json:"usage,omitempty"`
}

type sseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type sseDelta struct {
	Type         string `json:"type,omitempty"`
	Text         string `json:"text,omitempty"`
	Thinking     string `json:"thinking,omitempty"`
	Signature    string `json:"signature,omitempty"`
	PartialJSON  string `json:"partial_json,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
}

type sseErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// dispatch decodes one SSE frame and advances stream state. It returns an event
// and emit=true only for frames that map to a normalized event.
func (h *streamHandle) dispatch(frame []byte) (provider.StreamEvent, bool, error) {
	var e sseEvent
	if err := json.Unmarshal(frame, &e); err != nil {
		// A malformed frame is skipped rather than aborting the turn; the
		// Messages API interleaves ping frames and may add fields over time.
		return provider.StreamEvent{}, false, nil
	}

	switch e.Type {
	case "message_start":
		if e.Message != nil {
			h.mergeUsage(e.Message.Usage)
		}
		return provider.StreamEvent{}, false, nil

	case "content_block_start":
		return h.onBlockStart(e)

	case "content_block_delta":
		return h.onBlockDelta(e)

	case "content_block_stop":
		return h.onBlockStop(e)

	case "message_delta":
		if e.Delta != nil && e.Delta.StopReason != "" {
			h.stopReason = mapStopReason(e.Delta.StopReason)
			h.seenDelta = true
		}
		h.mergeUsage(e.Usage)
		return provider.StreamEvent{}, false, nil

	case "message_stop":
		ev, err := h.finish()
		return ev, true, err

	case "error":
		return provider.StreamEvent{}, false, streamError(e.Error)

	default:
		// ping and any unrecognized frame types are ignored.
		return provider.StreamEvent{}, false, nil
	}
}

func (h *streamHandle) onBlockStart(e sseEvent) (provider.StreamEvent, bool, error) {
	if e.ContentBlock == nil {
		return provider.StreamEvent{}, false, nil
	}
	st := &blockState{kind: e.ContentBlock.Type}
	h.blocks[e.Index] = st

	if e.ContentBlock.Type == "tool_use" {
		st.toolID = e.ContentBlock.ID
		st.toolName = e.ContentBlock.Name
		input := e.ContentBlock.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		return provider.StreamEvent{
			Type: provider.StreamToolCallStart,
			Tool: &provider.ToolCall{ID: st.toolID, Name: st.toolName, Input: input},
		}, true, nil
	}
	// text / thinking blocks announce nothing on their own.
	return provider.StreamEvent{}, false, nil
}

func (h *streamHandle) onBlockDelta(e sseEvent) (provider.StreamEvent, bool, error) {
	if e.Delta == nil {
		return provider.StreamEvent{}, false, nil
	}
	switch e.Delta.Type {
	case "text_delta":
		return provider.StreamEvent{Type: provider.StreamTextDelta, Text: e.Delta.Text}, true, nil
	case "thinking_delta":
		return provider.StreamEvent{Type: provider.StreamReasoningDelta, Text: e.Delta.Thinking}, true, nil
	case "signature_delta":
		// The signature seals the preceding thinking block; the API requires it
		// replayed on a later turn when extended thinking and tool use combine.
		// Carry it as opaque block metadata on an (empty-text) reasoning delta so
		// the loop merges it onto the assembled reasoning block.
		if e.Delta.Signature == "" {
			return provider.StreamEvent{}, false, nil
		}
		return provider.StreamEvent{
			Type: provider.StreamReasoningDelta,
			Meta: map[string]string{metaSignatureKey: e.Delta.Signature},
		}, true, nil
	case "input_json_delta":
		st := h.blocks[e.Index]
		if st != nil {
			st.input.WriteString(e.Delta.PartialJSON)
		}
		id := ""
		if st != nil {
			id = st.toolID
		}
		return provider.StreamEvent{
			Type: provider.StreamToolCallDelta,
			Tool: &provider.ToolCall{ID: id, Delta: e.Delta.PartialJSON},
		}, true, nil
	default:
		// signature_delta and any future delta types carry no normalized event.
		return provider.StreamEvent{}, false, nil
	}
}

func (h *streamHandle) onBlockStop(e sseEvent) (provider.StreamEvent, bool, error) {
	st := h.blocks[e.Index]
	delete(h.blocks, e.Index)
	if st == nil || st.kind != "tool_use" {
		return provider.StreamEvent{}, false, nil
	}
	input := strings.TrimSpace(st.input.String())
	if input == "" {
		input = "{}"
	}
	return provider.StreamEvent{
		Type: provider.StreamToolCallEnd,
		Tool: &provider.ToolCall{ID: st.toolID, Name: st.toolName, Input: json.RawMessage(input)},
	}, true, nil
}

// Close releases the underlying response body. It is safe to call more than once.
func (h *streamHandle) Close() error {
	if h.closed {
		return nil
	}
	h.closed = true
	return h.body.Close()
}

// mergeUsage folds an SSE usage report into the accumulated normalized usage.
// input and cache counters arrive on message_start; output_tokens is reported
// cumulatively and later message_delta values overwrite earlier ones.
func (h *streamHandle) mergeUsage(u *sseUsage) {
	if u == nil {
		return
	}
	if u.InputTokens > 0 {
		h.usage.InputTokens = u.InputTokens
	}
	if u.CacheReadInputTokens > 0 {
		h.usage.CacheReadTokens = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		h.usage.CacheWriteTokens = u.CacheCreationInputTokens
	}
	if u.OutputTokens > 0 {
		h.usage.OutputTokens = u.OutputTokens
	}
	h.retainRaw(u)
}

// retainRaw records the provider's verbatim token fields in Usage.Raw for audit.
func (h *streamHandle) retainRaw(u *sseUsage) {
	if h.usage.Raw == nil {
		h.usage.Raw = make(map[string]int, 4)
	}
	if u.InputTokens > 0 {
		h.usage.Raw["input_tokens"] = u.InputTokens
	}
	if u.OutputTokens > 0 {
		h.usage.Raw["output_tokens"] = u.OutputTokens
	}
	if u.CacheReadInputTokens > 0 {
		h.usage.Raw["cache_read_input_tokens"] = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		h.usage.Raw["cache_creation_input_tokens"] = u.CacheCreationInputTokens
	}
}

// sseUsage is the Messages API usage payload (message_start and message_delta).
type sseUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// mapStopReason normalizes an Anthropic stop reason. Unknown reasons pass
// through as-is rather than being mislabeled, since StopReason is a string.
func mapStopReason(raw string) provider.StopReason {
	switch raw {
	case "end_turn":
		return provider.StopEndTurn
	case "tool_use":
		return provider.StopToolUse
	case "max_tokens":
		return provider.StopMaxTokens
	case "stop_sequence":
		return provider.StopStopSequence
	case "refusal":
		return provider.StopRefusal
	default:
		return provider.StopReason(raw)
	}
}

// streamError converts an SSE error frame into an [Error].
func streamError(d *sseErrorDetail) error {
	if d == nil {
		return &Error{Message: "anthropic: stream error"}
	}
	return &Error{Type: d.Type, Message: d.Message}
}
