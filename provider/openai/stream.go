package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// metaItemID is the provider-namespaced StreamEvent.Meta key under which a
// reasoning block's Responses-API item id is journaled.
const metaItemID = "openai.item_id"

// metaEncrypted is the provider-namespaced StreamEvent.Meta key under which a
// reasoning block's encrypted content is journaled (opted in via the request's
// `include: ["reasoning.encrypted_content"]`), enabling full reasoning replay
// on a later turn.
const metaEncrypted = "openai.encrypted_content"

// stream adapts a Responses-API SSE body to a [provider.StreamHandle]. One SSE
// frame yields zero or one normalized events; produced events are returned one
// per Next call.
type stream struct {
	ctx  context.Context
	body io.ReadCloser
	dec  *sseDecoder

	// toolByIndex and toolByItem correlate a function call's argument deltas and
	// terminal item back to its call id. The Responses API keys streaming
	// argument deltas by item_id and lifecycle events by output_index; tracking
	// both makes the lookup robust to whichever a given event carries.
	toolByIndex map[int]*toolCallState
	toolByItem  map[string]*toolCallState
	sawTool     bool
	sawRefusal  bool

	// reasoningByIndex maps a reasoning item's output_index to its item id, so
	// summary deltas that omit item_id can still be tagged with it.
	reasoningByIndex map[int]string

	pending  provider.StreamEvent // one buffered event when a frame yields output
	hasNext  bool
	terminal bool // a terminal event (Finished/cancel) has been emitted
}

type toolCallState struct {
	callID string
	name   string
}

// lookupTool resolves a function call's state by item id (preferred) or its
// output index, tolerating events that carry only one of the two.
func (s *stream) lookupTool(itemID string, outputIndex int) *toolCallState {
	if itemID != "" {
		if st := s.toolByItem[itemID]; st != nil {
			return st
		}
	}
	return s.toolByIndex[outputIndex]
}

func newStream(ctx context.Context, body io.ReadCloser) *stream {
	return &stream{
		ctx:              ctx,
		body:             body,
		dec:              newSSEDecoder(body),
		toolByIndex:      map[int]*toolCallState{},
		toolByItem:       map[string]*toolCallState{},
		reasoningByIndex: map[int]string{},
	}
}

// Close releases the underlying body. Safe to call more than once.
func (s *stream) Close() error {
	return s.body.Close()
}

// Next returns the next normalized event, or io.EOF at end of stream. On
// context cancellation it emits a single terminal StreamFinished with
// StopCancelled, then io.EOF.
func (s *stream) Next() (provider.StreamEvent, error) {
	if s.hasNext {
		s.hasNext = false
		return s.pending, nil
	}
	if s.terminal {
		return provider.StreamEvent{}, io.EOF
	}

	for {
		if err := s.ctx.Err(); err != nil {
			s.terminal = true
			return provider.StreamEvent{Type: provider.StreamFinished, StopReason: provider.StopCancelled}, nil
		}

		frame, err := s.dec.next()
		if err == io.EOF {
			s.terminal = true
			return provider.StreamEvent{}, io.EOF
		}
		if err != nil {
			if s.ctx.Err() != nil {
				s.terminal = true
				return provider.StreamEvent{Type: provider.StreamFinished, StopReason: provider.StopCancelled}, nil
			}
			return provider.StreamEvent{}, err
		}

		ev, ok, err := s.handle(frame)
		if err != nil {
			return provider.StreamEvent{}, err
		}
		if ok {
			return ev, nil
		}
		// frame produced no event (e.g. lifecycle/added/done bookkeeping) — read on.
	}
}

// respEvent is the union of Responses-API SSE payload fields the adapter reads.
// The event's own "type" selects which fields are meaningful.
type respEvent struct {
	Type        string      `json:"type"`
	Delta       string      `json:"delta"`
	OutputIndex int         `json:"output_index"`
	ItemID      string      `json:"item_id"`
	Item        *respItem   `json:"item"`
	Response    *respObject `json:"response"`
	// Top-level "error" frames.
	Code    string `json:"code"`
	Message string `json:"message"`
}

type respItem struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	EncryptedContent string `json:"encrypted_content"`
}

type respObject struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Usage *respUsage `json:"usage"`
}

// handle maps one SSE frame to at most one normalized event. The bool reports
// whether an event was produced.
func (s *stream) handle(frame sseFrame) (provider.StreamEvent, bool, error) {
	if bytes.Equal(bytes.TrimSpace(frame.data), []byte("[DONE]")) {
		return provider.StreamEvent{}, false, nil
	}
	var e respEvent
	if err := json.Unmarshal(frame.data, &e); err != nil {
		// Tolerate keep-alive / unparseable frames rather than aborting a turn.
		return provider.StreamEvent{}, false, nil
	}

	switch e.Type {
	case "response.output_text.delta":
		return provider.StreamEvent{Type: provider.StreamTextDelta, Text: e.Delta}, true, nil

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		ev := provider.StreamEvent{Type: provider.StreamReasoningDelta, Text: e.Delta}
		// Tag the reasoning delta with its item id so the loop journals it onto
		// the assembled reasoning block. Full reasoning replay (which also needs
		// reasoning.encrypted_content, populated from output_item.done and
		// consumed in request.go to rebuild the reasoning item) uses this id.
		// The id is on the event, or tracked from the reasoning item's
		// output_item.added by output_index.
		if id := e.ItemID; id != "" {
			ev.Meta = map[string]string{metaItemID: id}
		} else if id := s.reasoningByIndex[e.OutputIndex]; id != "" {
			ev.Meta = map[string]string{metaItemID: id}
		}
		return ev, true, nil

	case "response.refusal.delta":
		// Surface refusal text to the caller and remember it for the stop reason.
		s.sawRefusal = true
		return provider.StreamEvent{Type: provider.StreamTextDelta, Text: e.Delta}, true, nil

	case "response.output_item.added":
		if e.Item != nil && e.Item.Type == "function_call" {
			s.sawTool = true
			st := &toolCallState{callID: e.Item.CallID, name: e.Item.Name}
			s.toolByIndex[e.OutputIndex] = st
			if e.Item.ID != "" {
				s.toolByItem[e.Item.ID] = st
			}
			return provider.StreamEvent{
				Type: provider.StreamToolCallStart,
				Tool: &provider.ToolCall{ID: st.callID, Name: st.name, Input: initialArgs(e.Item.Arguments)},
			}, true, nil
		}
		if e.Item != nil && e.Item.Type == "reasoning" && e.Item.ID != "" {
			s.reasoningByIndex[e.OutputIndex] = e.Item.ID
		}
		return provider.StreamEvent{}, false, nil

	case "response.function_call_arguments.delta":
		// Drop a delta we can't correlate to a known call rather than emit it
		// with an empty ID: an unattributable delta would misroute downstream,
		// and the authoritative arguments arrive on the must-deliver
		// StreamToolCallEnd regardless — deltas are lossy live-display only.
		st := s.lookupTool(e.ItemID, e.OutputIndex)
		if st == nil {
			return provider.StreamEvent{}, false, nil
		}
		return provider.StreamEvent{
			Type: provider.StreamToolCallDelta,
			Tool: &provider.ToolCall{ID: st.callID, Delta: e.Delta},
		}, true, nil

	case "response.output_item.done":
		if e.Item != nil && e.Item.Type == "function_call" {
			st := s.lookupTool(e.Item.ID, e.OutputIndex)
			id, name := e.Item.CallID, e.Item.Name
			if st != nil {
				if id == "" {
					id = st.callID
				}
				if name == "" {
					name = st.name
				}
			}
			return provider.StreamEvent{
				Type: provider.StreamToolCallEnd,
				Tool: &provider.ToolCall{ID: id, Name: name, Input: initialArgs(e.Item.Arguments)},
			}, true, nil
		}
		if e.Item != nil && e.Item.Type == "reasoning" && e.Item.EncryptedContent != "" {
			id := e.Item.ID
			if id == "" {
				id = s.reasoningByIndex[e.OutputIndex]
			}
			return provider.StreamEvent{
				Type: provider.StreamReasoningDelta,
				Meta: map[string]string{metaEncrypted: e.Item.EncryptedContent, metaItemID: id},
			}, true, nil
		}
		return provider.StreamEvent{}, false, nil

	case "response.completed", "response.incomplete":
		s.terminal = true
		return s.finished(e.Response), true, nil

	case "response.failed":
		return provider.StreamEvent{}, false, responseError(e.Response)

	case "error":
		return provider.StreamEvent{}, false, &StreamError{Code: e.Code, Message: e.Message}

	default:
		// Lifecycle and part-boundary frames we do not surface.
		return provider.StreamEvent{}, false, nil
	}
}

// finished builds the terminal StreamFinished event from the final response
// object, resolving the normalized stop reason and usage.
func (s *stream) finished(r *respObject) provider.StreamEvent {
	ev := provider.StreamEvent{Type: provider.StreamFinished, StopReason: provider.StopEndTurn}
	if r == nil {
		if s.sawTool {
			ev.StopReason = provider.StopToolUse
		}
		return ev
	}
	ev.Usage = normalizeUsage(r.Usage)
	ev.StopReason = s.stopReason(r)
	return ev
}

// stopReason maps the final response status onto a normalized stop reason.
func (s *stream) stopReason(r *respObject) provider.StopReason {
	if r.Status == "incomplete" && r.IncompleteDetails != nil {
		switch r.IncompleteDetails.Reason {
		case "max_output_tokens":
			return provider.StopMaxTokens
		case "content_filter":
			return provider.StopRefusal
		}
	}
	if s.sawRefusal {
		return provider.StopRefusal
	}
	if s.sawTool {
		return provider.StopToolUse
	}
	return provider.StopEndTurn
}

// initialArgs normalizes a possibly-empty arguments string to valid JSON.
func initialArgs(args string) json.RawMessage {
	if args == "" {
		return nil
	}
	return json.RawMessage(args)
}

// responseError extracts a StreamError from a failed response object.
func responseError(r *respObject) error {
	if r != nil && r.Error != nil {
		return &StreamError{Code: r.Error.Code, Message: r.Error.Message}
	}
	return &StreamError{Message: "response failed"}
}

// --- SSE decoding ---

// sseFrame is one server-sent event's coalesced data payload.
type sseFrame struct {
	data []byte
}

// sseDecoder reads SSE frames from a body, coalescing consecutive data: lines
// and delimiting on blank lines. It uses a bufio.Reader (not Scanner) so a
// single very large data line — e.g. the final response object — is not capped.
type sseDecoder struct {
	r   *bufio.Reader
	buf bytes.Buffer
}

func newSSEDecoder(r io.Reader) *sseDecoder {
	return &sseDecoder{r: bufio.NewReaderSize(r, 64<<10)}
}

// next returns the next frame carrying data, or io.EOF at end of stream.
func (d *sseDecoder) next() (sseFrame, error) {
	d.buf.Reset()
	for {
		line, err := d.readLine()
		if err != nil {
			if err == io.EOF && d.buf.Len() > 0 {
				return sseFrame{data: append([]byte(nil), d.buf.Bytes()...)}, nil
			}
			return sseFrame{}, err
		}

		// Blank line: dispatch the accumulated event if it carried data.
		if len(line) == 0 {
			if d.buf.Len() > 0 {
				return sseFrame{data: append([]byte(nil), d.buf.Bytes()...)}, nil
			}
			continue // event with no data (e.g. comment-only) — keep reading
		}

		// Comments (":" prefix) and non-data fields (event:, id:, retry:) are
		// ignored; the data payload's own "type" drives normalization.
		if line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte(":"))
		if !found {
			continue
		}
		if !bytes.Equal(field, []byte("data")) {
			continue
		}
		// Strip a single leading space after the colon per the SSE spec.
		value = bytes.TrimPrefix(value, []byte(" "))
		if d.buf.Len() > 0 {
			d.buf.WriteByte('\n')
		}
		d.buf.Write(value)
	}
}

// readLine reads one line without the trailing CR/LF, handling lines longer
// than the reader's buffer.
func (d *sseDecoder) readLine() ([]byte, error) {
	var full []byte
	for {
		frag, err := d.r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			full = append(full, frag...)
			continue
		}
		if err != nil {
			if err == io.EOF && len(frag) > 0 {
				full = append(full, frag...)
				return trimEOL(full), nil
			}
			return nil, err
		}
		full = append(full, frag...)
		return trimEOL(full), nil
	}
}

// trimEOL removes a trailing \n and optional preceding \r.
func trimEOL(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	b = bytes.TrimSuffix(b, []byte("\r"))
	return b
}

// compile-time guard: stream satisfies the handle interface.
var _ provider.StreamHandle = (*stream)(nil)
