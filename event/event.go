package event

import (
	"encoding/json"
	"time"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// Event is one message in a session's ordered stream. It is a closed union:
// only the concrete types in this package implement it (the withMeta method is
// unexported), so clients can exhaustively type-switch over the contract.
//
// Every event serializes to the envelope {"type", "session_id", "seq", "time",
// ...payload}. Seq and Time are assigned by the [Broker] at publish time.
type Event interface {
	// Kind returns the event's kind identifier (e.g. "message.delta").
	Kind() string
	// SessionID returns the session the event belongs to.
	SessionID() string
	// Seq returns the per-session monotonic sequence number.
	Seq() uint64
	// Time returns the publish timestamp.
	Time() time.Time
	// Tier returns the event's delivery guarantee.
	Tier() Tier

	json.Marshaler

	// withMeta returns a copy of the event with seq and time set. It is
	// unexported so the union stays closed to this package.
	withMeta(seq uint64, ts time.Time) Event
}

// meta is the identity shared by every event. Its fields are populated by the
// broker; constructors set only the session id.
type meta struct {
	session string
	seq     uint64
	ts      time.Time
}

// SessionID returns the owning session id.
func (m meta) SessionID() string { return m.session }

// Seq returns the per-session sequence number.
func (m meta) Seq() uint64 { return m.seq }

// Time returns the publish timestamp.
func (m meta) Time() time.Time { return m.ts }

// envelope is the wire header shared by every event. Embedding it (untagged)
// inlines type/session_id/seq/time ahead of the payload fields.
type envelope struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Seq       uint64 `json:"seq"`
	Time      string `json:"time,omitempty"`
}

func baseEnvelope(e Event) envelope {
	return envelope{
		Type:      e.Kind(),
		SessionID: e.SessionID(),
		Seq:       e.Seq(),
		Time:      formatTime(e.Time()),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func marshalBare(e Event) ([]byte, error) { return json.Marshal(baseEnvelope(e)) }

// --- session lifecycle events (all bare, must-deliver) ---

// SessionCreated is emitted once when a session is constructed.
type SessionCreated struct{ meta }

// NewSessionCreated builds a session.created event for the given session.
func NewSessionCreated(session string) SessionCreated {
	return SessionCreated{meta{session: session}}
}

// Kind returns KindSessionCreated.
func (SessionCreated) Kind() string { return KindSessionCreated }

// Tier returns TierMustDeliver.
func (SessionCreated) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope.
func (e SessionCreated) MarshalJSON() ([]byte, error) { return marshalBare(e) }

func (e SessionCreated) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// SessionResumed is emitted when a persisted session is reloaded.
type SessionResumed struct{ meta }

// NewSessionResumed builds a session.resumed event.
func NewSessionResumed(session string) SessionResumed {
	return SessionResumed{meta{session: session}}
}

// Kind returns KindSessionResumed.
func (SessionResumed) Kind() string { return KindSessionResumed }

// Tier returns TierMustDeliver.
func (SessionResumed) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope.
func (e SessionResumed) MarshalJSON() ([]byte, error) { return marshalBare(e) }

func (e SessionResumed) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// SessionForked is emitted when a session branches from another.
type SessionForked struct{ meta }

// NewSessionForked builds a session.forked event.
func NewSessionForked(session string) SessionForked {
	return SessionForked{meta{session: session}}
}

// Kind returns KindSessionForked.
func (SessionForked) Kind() string { return KindSessionForked }

// Tier returns TierMustDeliver.
func (SessionForked) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope.
func (e SessionForked) MarshalJSON() ([]byte, error) { return marshalBare(e) }

func (e SessionForked) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// SessionCompacted is emitted when a session's history is compacted.
type SessionCompacted struct{ meta }

// NewSessionCompacted builds a session.compacted event.
func NewSessionCompacted(session string) SessionCompacted {
	return SessionCompacted{meta{session: session}}
}

// Kind returns KindSessionCompacted.
func (SessionCompacted) Kind() string { return KindSessionCompacted }

// Tier returns TierMustDeliver.
func (SessionCompacted) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope.
func (e SessionCompacted) MarshalJSON() ([]byte, error) { return marshalBare(e) }

func (e SessionCompacted) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// SessionKilled is emitted when a session is terminated.
type SessionKilled struct{ meta }

// NewSessionKilled builds a session.killed event.
func NewSessionKilled(session string) SessionKilled {
	return SessionKilled{meta{session: session}}
}

// Kind returns KindSessionKilled.
func (SessionKilled) Kind() string { return KindSessionKilled }

// Tier returns TierMustDeliver.
func (SessionKilled) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope.
func (e SessionKilled) MarshalJSON() ([]byte, error) { return marshalBare(e) }

func (e SessionKilled) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// SessionArchived is emitted when a session is archived.
type SessionArchived struct{ meta }

// NewSessionArchived builds a session.archived event.
func NewSessionArchived(session string) SessionArchived {
	return SessionArchived{meta{session: session}}
}

// Kind returns KindSessionArchived.
func (SessionArchived) Kind() string { return KindSessionArchived }

// Tier returns TierMustDeliver.
func (SessionArchived) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope.
func (e SessionArchived) MarshalJSON() ([]byte, error) { return marshalBare(e) }

func (e SessionArchived) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// SessionInfoUpdated is emitted when a session's mutable metadata changes —
// currently its human-readable title. It is the seam an embedder uses to push
// a session title to clients live (it projects to an ACP session_info_update).
//
// The title itself is application business logic: the embedder derives it from
// the first user prompt, an LLM summary, or a user rename, then calls
// [session.Session.SetTitle]. The SDK only carries and broadcasts the value —
// it never generates one.
type SessionInfoUpdated struct {
	meta
	Title string
}

// NewSessionInfoUpdated builds a session.info event carrying the session's new
// title.
func NewSessionInfoUpdated(session, title string) SessionInfoUpdated {
	return SessionInfoUpdated{meta: meta{session: session}, Title: title}
}

// Kind returns KindSessionInfo.
func (SessionInfoUpdated) Kind() string { return KindSessionInfo }

// Tier returns TierMustDeliver.
func (SessionInfoUpdated) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope plus {title}.
func (e SessionInfoUpdated) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		Title string `json:"title"`
	}{baseEnvelope(e), e.Title})
}

func (e SessionInfoUpdated) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// ConfigOptionKind discriminates a [ConfigOption]'s type: a single-value select
// (dropdown) or a boolean on/off toggle. It mirrors the two ACP config-option
// kinds neutrally, so the event contract carries config options without
// depending on the acp package.
type ConfigOptionKind string

// The config-option kinds.
const (
	// ConfigOptionSelect is a single-value selector: SelectedValue names the
	// current choice out of Values.
	ConfigOptionSelect ConfigOptionKind = "select"
	// ConfigOptionBoolean is an on/off toggle: Enabled is its current state.
	ConfigOptionBoolean ConfigOptionKind = "boolean"
)

// ConfigSelectValue is one selectable value of a select-kind [ConfigOption].
type ConfigSelectValue struct {
	// Value is the value's unique id.
	Value string `json:"value"`
	// Name is the human-readable label.
	Name string `json:"name"`
	// Description is an optional description for a client to display.
	Description string `json:"description,omitempty"`
}

// ConfigOption is one session configuration selector and its current value,
// carried by a [ConfigOptionsUpdated] event. It is a neutral, transport-only
// snapshot of a config option: the rich protocol modeling (per-kind wire shape,
// forward-compat decode) lives in the acp package, which projects this into its
// own config-option type. A single flat struct with a Kind discriminator carries
// both variants' data without re-creating that type hierarchy here — Kind selects
// which fields are meaningful.
type ConfigOption struct {
	// ID uniquely identifies the option.
	ID string `json:"id"`
	// Name is the human-readable label.
	Name string `json:"name"`
	// Description is an optional description for a client to display.
	Description string `json:"description,omitempty"`
	// Category is an optional semantic category (UX hint only), e.g. "model".
	Category string `json:"category,omitempty"`
	// Kind selects which of the fields below are meaningful.
	Kind ConfigOptionKind `json:"kind"`

	// SelectedValue and Values apply to a select-kind option: the currently
	// selected value id and the set of selectable values. Both are empty for a
	// boolean option.
	SelectedValue string              `json:"selectedValue,omitempty"`
	Values        []ConfigSelectValue `json:"values,omitempty"`

	// Enabled is the current on/off state of a boolean-kind option; false (and
	// omitted) for a select option.
	Enabled bool `json:"enabled,omitempty"`
}

// ConfigOptionsUpdated is emitted when the embedder's session configuration
// options change — e.g. the current model, session mode, or a boolean toggle. It
// is the seam an embedder uses to advertise its config options to clients live
// (it projects to an ACP config_option_update). Options is the full current set
// as an authoritative snapshot (each event carries the whole set, not a delta),
// so a client replaces its config UI from it; a non-nil but empty slice is a
// "no options" snapshot.
//
// Config-option content is application business logic: WHICH options exist (that
// "model" is a selector, and its values) is the embedder's knowledge, not the
// SDK's. The embedder builds the snapshot and emits this event; the SDK only
// carries and broadcasts it.
type ConfigOptionsUpdated struct {
	meta
	Options []ConfigOption
}

// NewConfigOptionsUpdated builds a session.config event carrying the embedder's
// full current set of config options.
func NewConfigOptionsUpdated(session string, options []ConfigOption) ConfigOptionsUpdated {
	return ConfigOptionsUpdated{meta: meta{session: session}, Options: options}
}

// Kind returns KindSessionConfig.
func (ConfigOptionsUpdated) Kind() string { return KindSessionConfig }

// Tier returns TierMustDeliver: a config-options snapshot is authoritative
// session metadata.
func (ConfigOptionsUpdated) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope plus {options}. options is always present (an
// empty set marshals to []) so a client can distinguish a cleared set from an
// absent field.
func (e ConfigOptionsUpdated) MarshalJSON() ([]byte, error) {
	options := e.Options
	if options == nil {
		options = []ConfigOption{}
	}
	return json.Marshal(struct {
		envelope
		Options []ConfigOption `json:"options"`
	}{baseEnvelope(e), options})
}

func (e ConfigOptionsUpdated) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// PlanEntry is one item in an agent's task plan carried by a [PlanUpdated]
// event: what the step is, how it is prioritized, and where it stands. It
// projects to an ACP plan entry.
type PlanEntry struct {
	// Content is the human-readable description of the task.
	Content string `json:"content"`
	// Priority is one of "high", "medium", "low".
	Priority string `json:"priority"`
	// Status is one of "pending", "in_progress", "completed".
	Status string `json:"status"`
}

// PlanUpdated is emitted when the agent publishes or revises its task plan via
// the update_plan builtin tool. Entries is the full current plan as an
// authoritative snapshot (each call carries the whole plan, not a delta), so a
// client renders it as a live checklist; a non-nil but empty slice is a
// "plan cleared" snapshot. It projects to an ACP `plan` session/update.
//
// The plan's content is the model's business logic (the loop bridges it from
// the update_plan tool's result); the SDK only carries and broadcasts it.
type PlanUpdated struct {
	meta
	Entries []PlanEntry
}

// NewPlanUpdated builds a plan event carrying the agent's full current plan.
func NewPlanUpdated(session string, entries []PlanEntry) PlanUpdated {
	return PlanUpdated{meta: meta{session: session}, Entries: entries}
}

// Kind returns KindPlan.
func (PlanUpdated) Kind() string { return KindPlan }

// Tier returns TierMustDeliver: a plan snapshot is authoritative session state.
func (PlanUpdated) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope plus {entries}. entries is always present
// (an empty plan marshals to []) so a client can distinguish a cleared plan
// from an absent field.
func (e PlanUpdated) MarshalJSON() ([]byte, error) {
	entries := e.Entries
	if entries == nil {
		entries = []PlanEntry{}
	}
	return json.Marshal(struct {
		envelope
		Entries []PlanEntry `json:"entries"`
	}{baseEnvelope(e), entries})
}

func (e PlanUpdated) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// SessionError reports an error within a session. Fatal marks errors that end
// the session.
type SessionError struct {
	meta
	Err   string
	Fatal bool
}

// NewSessionError builds a session.error event.
func NewSessionError(session, err string, fatal bool) SessionError {
	return SessionError{meta: meta{session: session}, Err: err, Fatal: fatal}
}

// Kind returns KindSessionError.
func (SessionError) Kind() string { return KindSessionError }

// Tier returns TierMustDeliver.
func (SessionError) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope plus {error, fatal}.
func (e SessionError) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		Err   string `json:"error"`
		Fatal bool   `json:"fatal,omitempty"`
	}{baseEnvelope(e), e.Err, e.Fatal})
}

func (e SessionError) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// --- turn events ---

// TurnStarted marks the beginning of a turn.
type TurnStarted struct{ meta }

// NewTurnStarted builds a turn.started event.
func NewTurnStarted(session string) TurnStarted {
	return TurnStarted{meta{session: session}}
}

// Kind returns KindTurnStarted.
func (TurnStarted) Kind() string { return KindTurnStarted }

// Tier returns TierMustDeliver.
func (TurnStarted) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope.
func (e TurnStarted) MarshalJSON() ([]byte, error) { return marshalBare(e) }

func (e TurnStarted) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// TurnFinished marks the end of a turn, carrying the stop reason, the turn's
// normalized token usage, and (when the model is priced) its cost.
//
// Ordinarily each TurnFinished pairs with a preceding TurnStarted. The one
// exception is the loop's iteration cap: when a run stops at the cap while the
// model is still requesting tools, the loop emits a terminal TurnFinished
// carrying provider.StopMaxTurns ("max_turns") with NO matching TurnStarted, so
// a client that maps TurnFinished to a settled response sees a run end instead
// of hanging. Clients that pair TurnStarted/TurnFinished to track active turns
// must tolerate this unmatched terminal.
type TurnFinished struct {
	meta
	StopReason string
	Usage      provider.Usage
	// Cost is the turn's priced cost, or nil when the model is not in the
	// pricing registry (e.g. the faux provider).
	Cost *provider.Cost
	// ContextWindow is the model's total context-window size in tokens, or 0
	// when unknown (an unregistered model, or no model). It is sourced from the
	// model registry (provider.Lookup) at emit time, alongside pricing; a
	// consumer projecting context-usage state reads it here.
	ContextWindow int
}

// NewTurnFinished builds a turn.finished event without a cost. Use
// [NewTurnFinishedCost] to attach a priced cost, or set ContextWindow directly
// on the returned value to carry the model's context-window size.
func NewTurnFinished(session, stopReason string, usage provider.Usage) TurnFinished {
	return TurnFinished{meta: meta{session: session}, StopReason: stopReason, Usage: usage}
}

// NewTurnFinishedCost builds a turn.finished event carrying a priced cost. A nil
// cost is equivalent to [NewTurnFinished].
func NewTurnFinishedCost(session, stopReason string, usage provider.Usage, cost *provider.Cost) TurnFinished {
	return TurnFinished{meta: meta{session: session}, StopReason: stopReason, Usage: usage, Cost: cost}
}

// Kind returns KindTurnFinished.
func (TurnFinished) Kind() string { return KindTurnFinished }

// Tier returns TierMustDeliver.
func (TurnFinished) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope plus {stop_reason, usage, cost?,
// context_window?}. context_window is omitempty so a zero value (unknown /
// unregistered model) leaves the payload identical to a turn with no window.
func (e TurnFinished) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		StopReason    string         `json:"stop_reason"`
		Usage         provider.Usage `json:"usage"`
		Cost          *provider.Cost `json:"cost,omitempty"`
		ContextWindow int            `json:"context_window,omitempty"`
	}{baseEnvelope(e), e.StopReason, e.Usage, e.Cost, e.ContextWindow})
}

func (e TurnFinished) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// --- message events ---

// MessageStarted opens a text or reasoning message.
type MessageStarted struct {
	meta
	MessageKind MessageKind
}

// NewMessageStarted builds a message.started event.
func NewMessageStarted(session string, kind MessageKind) MessageStarted {
	return MessageStarted{meta: meta{session: session}, MessageKind: kind}
}

// Kind returns KindMessageStarted.
func (MessageStarted) Kind() string { return KindMessageStarted }

// Tier returns TierMustDeliver.
func (MessageStarted) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope plus {kind}.
func (e MessageStarted) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		MessageKind MessageKind `json:"kind"`
	}{baseEnvelope(e), e.MessageKind})
}

func (e MessageStarted) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// MessageDelta carries an incremental chunk of a message. It rides the lossy
// tier; the matching MessageFinished carries the authoritative content.
type MessageDelta struct {
	meta
	MessageKind MessageKind
	Text        string
}

// NewMessageDelta builds a message.delta event.
func NewMessageDelta(session string, kind MessageKind, text string) MessageDelta {
	return MessageDelta{meta: meta{session: session}, MessageKind: kind, Text: text}
}

// Kind returns KindMessageDelta.
func (MessageDelta) Kind() string { return KindMessageDelta }

// Tier returns TierLossy.
func (MessageDelta) Tier() Tier { return TierLossy }

// MarshalJSON encodes the envelope plus {kind, text}.
func (e MessageDelta) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		MessageKind MessageKind `json:"kind"`
		Text        string      `json:"text"`
	}{baseEnvelope(e), e.MessageKind, e.Text})
}

func (e MessageDelta) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// MessageFinished closes a message and carries its full settled content, which
// reconciles any dropped deltas. Meta carries opaque, provider-namespaced
// per-block metadata accumulated over the message's deltas (e.g.
// "anthropic.signature", "openai.item_id") that a client journaling from the
// event stream must round-trip verbatim on a later turn; the event layer never
// interprets it.
type MessageFinished struct {
	meta
	MessageKind MessageKind
	Content     string
	Meta        map[string]string
}

// NewMessageFinished builds a message.finished event with no per-block
// metadata. Use [NewMessageFinishedMeta] to attach it.
func NewMessageFinished(session string, kind MessageKind, content string) MessageFinished {
	return NewMessageFinishedMeta(session, kind, content, nil)
}

// NewMessageFinishedMeta builds a message.finished event carrying opaque
// provider-namespaced per-block metadata (e.g. "anthropic.signature",
// "openai.item_id") for a client to round-trip verbatim.
func NewMessageFinishedMeta(session string, kind MessageKind, content string, blockMeta map[string]string) MessageFinished {
	return MessageFinished{meta: meta{session: session}, MessageKind: kind, Content: content, Meta: blockMeta}
}

// Kind returns KindMessageFinished.
func (MessageFinished) Kind() string { return KindMessageFinished }

// Tier returns TierMustDeliver.
func (MessageFinished) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope plus {kind, content, meta?}.
func (e MessageFinished) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		MessageKind MessageKind       `json:"kind"`
		Content     string            `json:"content"`
		Meta        map[string]string `json:"meta,omitempty"`
	}{baseEnvelope(e), e.MessageKind, e.Content, e.Meta})
}

func (e MessageFinished) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// --- tool call events ---

// ToolCallStarted announces a tool invocation with its raw input.
type ToolCallStarted struct {
	meta
	ID    string
	Name  string
	Input json.RawMessage
}

// NewToolCallStarted builds a tool.call.started event.
func NewToolCallStarted(session, id, name string, input json.RawMessage) ToolCallStarted {
	return ToolCallStarted{meta: meta{session: session}, ID: id, Name: name, Input: input}
}

// Kind returns KindToolCallStarted.
func (ToolCallStarted) Kind() string { return KindToolCallStarted }

// Tier returns TierMustDeliver.
func (ToolCallStarted) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope plus {id, name, input}.
func (e ToolCallStarted) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input,omitempty"`
	}{baseEnvelope(e), e.ID, e.Name, e.Input})
}

func (e ToolCallStarted) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// ToolCallDelta carries an incremental chunk of a tool call's streaming output.
// It rides the lossy tier.
type ToolCallDelta struct {
	meta
	ID    string
	Delta string
}

// NewToolCallDelta builds a tool.call.delta event.
func NewToolCallDelta(session, id, delta string) ToolCallDelta {
	return ToolCallDelta{meta: meta{session: session}, ID: id, Delta: delta}
}

// Kind returns KindToolCallDelta.
func (ToolCallDelta) Kind() string { return KindToolCallDelta }

// Tier returns TierLossy.
func (ToolCallDelta) Tier() Tier { return TierLossy }

// MarshalJSON encodes the envelope plus {id, delta}.
func (e ToolCallDelta) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		ID    string `json:"id"`
		Delta string `json:"delta"`
	}{baseEnvelope(e), e.ID, e.Delta})
}

func (e ToolCallDelta) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// ToolCallFinished closes a tool call with the authoritative input the call ran
// with, a bounded excerpt of its result, whether it errored, optional
// diagnostics, and — when the output was streamed to a durable spill file — a
// reference to that file.
//
// Input is the complete, assembled tool input the call executed with — the
// authoritative payload a client should reconcile against. It is distinct from
// tool.call.started's Input, which carries only the start-of-block seed (an
// empty "{}" when a provider streams the arguments as input_json_delta
// fragments). A consumer that needs the real arguments — to journal the
// tool_use block, or to surface them in a UI — must read them here, not from the
// started event.
//
// Result is a bounded head+tail excerpt of the tool's output, not the full
// payload: the full, untruncated output lives in the spill file named by
// SpillPath, and the excerpt is a preview old consumers can still read. SpillPath
// is relative to the session store root (e.g.
// "sessions/<slug>/<id>/calls/<call-id>.log"), never an absolute host path, so
// the event stays portable when serialized; SpillBytes and SpillSHA256 describe
// the full on-disk content. The three Spill fields are empty when no file was
// written (e.g. a call pre-empted by cancellation, or a session with no store).
type ToolCallFinished struct {
	meta
	ID          string
	Input       json.RawMessage
	Result      string
	IsError     bool
	Diagnostics []string
	// Edits are the structured file mutations the call performed, or nil when it
	// changed no files. A file-editing tool (edit, write) populates them so a
	// client can render a before/after diff instead of the plain-text Result.
	// It is set on the built event at emit time (like ContextWindow on
	// TurnFinished), not through the constructors; additive and journal-portable.
	Edits []FileEdit
	// SpillPath is the spill file relative to the store root, or empty when the
	// output was not spilled to a file.
	SpillPath string
	// SpillBytes is the full byte length of the spilled output.
	SpillBytes int64
	// SpillSHA256 is the hex-encoded sha256 of the full spilled output.
	SpillSHA256 string
}

// FileEdit is a structured before/after record of one file a tool mutated: the
// file's path and its content before and after the change. OldText is empty
// when the file was created. It is the contract basis a client renders as a
// diff; it rides on [ToolCallFinished.Edits] and never enters the model's
// context.
type FileEdit struct {
	// Path is the file's path as the tool was asked to change it.
	Path string `json:"path"`
	// OldText is the file's content before the change, empty for a creation.
	OldText string `json:"old_text,omitempty"`
	// NewText is the file's content after the change.
	NewText string `json:"new_text"`
}

// NewToolCallFinished builds a tool.call.finished event with no spill-file
// reference (result carries the bounded excerpt directly). input is the
// authoritative assembled tool input the call ran with. Use
// [NewToolCallFinishedSpill] to attach a spill reference.
func NewToolCallFinished(session, id string, input json.RawMessage, result string, isError bool, diagnostics []string) ToolCallFinished {
	return ToolCallFinished{meta: meta{session: session}, ID: id, Input: input, Result: result, IsError: isError, Diagnostics: diagnostics}
}

// NewToolCallFinishedSpill builds a tool.call.finished event carrying a spill
// reference — the store-root-relative path, byte count, and sha256 of the durable
// file holding the full output — alongside the bounded excerpt. input is the
// authoritative assembled tool input the call ran with.
func NewToolCallFinishedSpill(session, id string, input json.RawMessage, excerpt string, isError bool, diagnostics []string, spillPath string, spillBytes int64, spillSHA256 string) ToolCallFinished {
	return ToolCallFinished{
		meta:        meta{session: session},
		ID:          id,
		Input:       input,
		Result:      excerpt,
		IsError:     isError,
		Diagnostics: diagnostics,
		SpillPath:   spillPath,
		SpillBytes:  spillBytes,
		SpillSHA256: spillSHA256,
	}
}

// Kind returns KindToolCallFinished.
func (ToolCallFinished) Kind() string { return KindToolCallFinished }

// Tier returns TierMustDeliver.
func (ToolCallFinished) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope plus {id, input?, result, is_error?,
// diagnostics?, edits?, spill_path?, spill_bytes?, spill_sha256?}. edits is
// omitempty so a call that changed no files leaves the payload identical to
// before this field existed.
func (e ToolCallFinished) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		ID          string          `json:"id"`
		Input       json.RawMessage `json:"input,omitempty"`
		Result      string          `json:"result"`
		IsError     bool            `json:"is_error,omitempty"`
		Diagnostics []string        `json:"diagnostics,omitempty"`
		Edits       []FileEdit      `json:"edits,omitempty"`
		SpillPath   string          `json:"spill_path,omitempty"`
		SpillBytes  int64           `json:"spill_bytes,omitempty"`
		SpillSHA256 string          `json:"spill_sha256,omitempty"`
	}{baseEnvelope(e), e.ID, e.Input, e.Result, e.IsError, e.Diagnostics, e.Edits, e.SpillPath, e.SpillBytes, e.SpillSHA256})
}

func (e ToolCallFinished) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// --- permission events ---

// PermissionRequested asks a client to decide a tool invocation, carrying the
// tool spec and the trace that led to the request.
type PermissionRequested struct {
	meta
	ID    string
	Tool  string
	Spec  map[string]any
	Trace []string
}

// NewPermissionRequested builds a permission.requested event.
func NewPermissionRequested(session, id, tool string, spec map[string]any, trace []string) PermissionRequested {
	return PermissionRequested{meta: meta{session: session}, ID: id, Tool: tool, Spec: spec, Trace: trace}
}

// Kind returns KindPermissionRequested.
func (PermissionRequested) Kind() string { return KindPermissionRequested }

// Tier returns TierMustDeliver.
func (PermissionRequested) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope plus {id, tool, spec, trace}.
func (e PermissionRequested) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		ID    string         `json:"id"`
		Tool  string         `json:"tool"`
		Spec  map[string]any `json:"spec,omitempty"`
		Trace []string       `json:"trace,omitempty"`
	}{baseEnvelope(e), e.ID, e.Tool, e.Spec, e.Trace})
}

func (e PermissionRequested) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}

// PermissionResolved reports the verdict for a permission request, naming the
// rule that decided it when one applied.
type PermissionResolved struct {
	meta
	ID      string
	Verdict Verdict
	Rule    string
}

// NewPermissionResolved builds a permission.resolved event.
func NewPermissionResolved(session, id string, verdict Verdict, rule string) PermissionResolved {
	return PermissionResolved{meta: meta{session: session}, ID: id, Verdict: verdict, Rule: rule}
}

// Kind returns KindPermissionResolved.
func (PermissionResolved) Kind() string { return KindPermissionResolved }

// Tier returns TierMustDeliver.
func (PermissionResolved) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope plus {id, verdict, rule?}.
func (e PermissionResolved) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		ID      string  `json:"id"`
		Verdict Verdict `json:"verdict"`
		Rule    string  `json:"rule,omitempty"`
	}{baseEnvelope(e), e.ID, e.Verdict, e.Rule})
}

func (e PermissionResolved) withMeta(seq uint64, ts time.Time) Event {
	e.seq, e.ts = seq, ts
	return e
}
