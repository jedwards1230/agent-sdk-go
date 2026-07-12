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
type TurnFinished struct {
	meta
	StopReason string
	Usage      provider.Usage
	// Cost is the turn's priced cost, or nil when the model is not in the
	// pricing registry (e.g. the faux provider).
	Cost *provider.Cost
}

// NewTurnFinished builds a turn.finished event without a cost. Use
// [NewTurnFinishedCost] to attach a priced cost.
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

// MarshalJSON encodes the envelope plus {stop_reason, usage, cost?}.
func (e TurnFinished) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		StopReason string         `json:"stop_reason"`
		Usage      provider.Usage `json:"usage"`
		Cost       *provider.Cost `json:"cost,omitempty"`
	}{baseEnvelope(e), e.StopReason, e.Usage, e.Cost})
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

// ToolCallFinished closes a tool call with its result, whether it errored, and
// optional diagnostics.
type ToolCallFinished struct {
	meta
	ID          string
	Result      string
	IsError     bool
	Diagnostics []string
}

// NewToolCallFinished builds a tool.call.finished event.
func NewToolCallFinished(session, id, result string, isError bool, diagnostics []string) ToolCallFinished {
	return ToolCallFinished{meta: meta{session: session}, ID: id, Result: result, IsError: isError, Diagnostics: diagnostics}
}

// Kind returns KindToolCallFinished.
func (ToolCallFinished) Kind() string { return KindToolCallFinished }

// Tier returns TierMustDeliver.
func (ToolCallFinished) Tier() Tier { return TierMustDeliver }

// MarshalJSON encodes the envelope plus {id, result, is_error?, diagnostics?}.
func (e ToolCallFinished) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		envelope
		ID          string   `json:"id"`
		Result      string   `json:"result"`
		IsError     bool     `json:"is_error,omitempty"`
		Diagnostics []string `json:"diagnostics,omitempty"`
	}{baseEnvelope(e), e.ID, e.Result, e.IsError, e.Diagnostics})
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
