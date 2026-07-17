package acp

import "encoding/json"

// ToolKind classifies a tool call for client-side iconography. Unknown or
// unmodeled tool kinds project to [ToolKindOther].
type ToolKind string

// The ACP tool kinds.
const (
	ToolKindRead    ToolKind = "read"
	ToolKindEdit    ToolKind = "edit"
	ToolKindDelete  ToolKind = "delete"
	ToolKindMove    ToolKind = "move"
	ToolKindSearch  ToolKind = "search"
	ToolKindExecute ToolKind = "execute"
	ToolKindThink   ToolKind = "think"
	ToolKindFetch   ToolKind = "fetch"
	ToolKindOther   ToolKind = "other"
)

// ToolCallStatus is the lifecycle state of a tool call.
type ToolCallStatus string

// The ACP tool call statuses.
const (
	ToolCallStatusPending    ToolCallStatus = "pending"
	ToolCallStatusInProgress ToolCallStatus = "in_progress"
	ToolCallStatusCompleted  ToolCallStatus = "completed"
	ToolCallStatusFailed     ToolCallStatus = "failed"
)

// ToolCallContent is a tagged union of tool call output kinds. This package
// models the three ACP v1 variants: "content" (a wrapped [ContentBlock], the
// one the outbound projection emits), "diff" (a file patch), and "terminal" (an
// embedded terminal reference). These are emit-side types; the package does not
// unmarshal a ToolCallContent union.
type ToolCallContent interface {
	// Type returns the variant's "type" discriminator.
	Type() string

	json.Marshaler
}

// ToolCallContentBlock wraps a [ContentBlock] as tool call output. It is the
// ACP "content" variant.
type ToolCallContentBlock struct {
	// Content is the wrapped block.
	Content ContentBlock
}

// Type returns "content".
func (ToolCallContentBlock) Type() string { return "content" }

// MarshalJSON encodes {"type":"content","content":...}.
func (c ToolCallContentBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type    string       `json:"type"`
		Content ContentBlock `json:"content"`
	}{c.Type(), c.Content})
}

// ToolCallContentDiff is a file patch produced by a tool call. It is the ACP
// "diff" variant.
type ToolCallContentDiff struct {
	// Path is the path of the file being changed.
	Path string
	// OldText is the file's prior content, or empty when the file is new
	// (omitted from the wire payload).
	OldText string
	// NewText is the file's resulting content.
	NewText string
}

// Type returns "diff".
func (ToolCallContentDiff) Type() string { return "diff" }

// MarshalJSON encodes {"type":"diff","path":...,"oldText"?:...,"newText":...}.
func (c ToolCallContentDiff) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type    string `json:"type"`
		Path    string `json:"path"`
		OldText string `json:"oldText,omitempty"`
		NewText string `json:"newText"`
	}{c.Type(), c.Path, c.OldText, c.NewText})
}

// ToolCallContentTerminal references an embedded terminal a tool call is driving.
// It is the ACP "terminal" variant.
type ToolCallContentTerminal struct {
	// TerminalID identifies the embedded terminal.
	TerminalID string
}

// Type returns "terminal".
func (ToolCallContentTerminal) Type() string { return "terminal" }

// MarshalJSON encodes {"type":"terminal","terminalId":...}.
func (c ToolCallContentTerminal) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type       string `json:"type"`
		TerminalID string `json:"terminalId"`
	}{c.Type(), c.TerminalID})
}

// ToolCall announces a new tool invocation as a session/update payload. Build
// one via [ToSessionUpdate] or directly for a manual notification.
type ToolCall struct {
	// ToolCallID identifies the call, matching a later [ToolCallUpdate].
	ToolCallID string
	// Title is a human-readable description of the call.
	Title string
	// ToolKind classifies the call for client iconography.
	ToolKind ToolKind
	// Status is the call's current lifecycle state.
	Status ToolCallStatus
	// RawInput is the tool's raw JSON arguments, or nil if none.
	RawInput json.RawMessage
	// Content is the call's output so far, or nil if none yet.
	Content []ToolCallContent
}

// Kind returns "tool_call", the session/update discriminator value.
func (ToolCall) Kind() string { return "tool_call" }

// MarshalJSON encodes the tagged tool_call session/update payload.
func (t ToolCall) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SessionUpdate string            `json:"sessionUpdate"`
		ToolCallID    string            `json:"toolCallId"`
		Title         string            `json:"title"`
		Kind          ToolKind          `json:"kind"`
		Status        ToolCallStatus    `json:"status"`
		RawInput      json.RawMessage   `json:"rawInput,omitempty"`
		Content       []ToolCallContent `json:"content,omitempty"`
	}{t.Kind(), t.ToolCallID, t.Title, t.ToolKind, t.Status, t.RawInput, t.Content})
}

// ToolCallUpdateFields are the mutable fields of a tool call, shared by the
// bare [ToolCallUpdate] (the toolCall member of a permission request) and the
// [ToolCallUpdated] session/update variant. Every field is optional; a zero
// value means "unchanged" and is omitted from the wire payload.
type ToolCallUpdateFields struct {
	// Status is the call's updated status, or "" if unchanged.
	Status ToolCallStatus `json:"status,omitempty"`
	// Title is the call's updated title, or "" if unchanged.
	Title string `json:"title,omitempty"`
	// Kind is the call's updated tool kind, or "" if unchanged.
	Kind ToolKind `json:"kind,omitempty"`
	// Content is the call's updated output, or nil if unchanged.
	Content []ToolCallContent `json:"content,omitempty"`
	// RawInput is the tool's raw JSON arguments, or nil if unchanged/absent. A
	// call announced with a placeholder input (an empty "{}" while the arguments
	// were still streaming) reconciles to its authoritative input here.
	RawInput json.RawMessage `json:"rawInput,omitempty"`
	// RawOutput is the tool's raw JSON result, or nil if unchanged/absent.
	RawOutput json.RawMessage `json:"rawOutput,omitempty"`
}

// ToolCallUpdate carries an incremental update to a previously announced tool
// call as a BARE object: the toolCall member of a [RequestPermissionRequest].
// It carries NO sessionUpdate discriminator — that tag belongs only to the
// [ToolCallUpdated] session/update variant, added there by the SessionUpdate
// union's tagging, not by the update object itself.
type ToolCallUpdate struct {
	// ToolCallID identifies the call being updated.
	ToolCallID string
	// Fields are the updated fields; zero-valued fields are omitted.
	Fields ToolCallUpdateFields
}

// MarshalJSON encodes {"toolCallId":..., ...fields} with the fields flattened
// and no sessionUpdate discriminator.
func (t ToolCallUpdate) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ToolCallID string `json:"toolCallId"`
		ToolCallUpdateFields
	}{t.ToolCallID, t.Fields})
}

// ToolCallUpdated is the tool_call_update session/update variant: an update to
// a previously announced tool call, tagged for the [SessionUpdate] union. It
// carries the same [ToolCallUpdateFields] as the bare [ToolCallUpdate] plus
// the sessionUpdate discriminator.
type ToolCallUpdated struct {
	// ToolCallID identifies the call being updated.
	ToolCallID string
	// Fields are the updated fields; zero-valued fields are omitted.
	Fields ToolCallUpdateFields
}

// Kind returns "tool_call_update", the session/update discriminator value.
func (ToolCallUpdated) Kind() string { return "tool_call_update" }

// MarshalJSON encodes the tagged tool_call_update session/update payload with
// the fields flattened after the discriminator and toolCallId.
func (t ToolCallUpdated) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SessionUpdate string `json:"sessionUpdate"`
		ToolCallID    string `json:"toolCallId"`
		ToolCallUpdateFields
	}{t.Kind(), t.ToolCallID, t.Fields})
}
