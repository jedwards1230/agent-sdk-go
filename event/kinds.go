// Package event defines the typed Event/Op contract that sits above the agent
// loop, plus the two-tier broker that delivers events to subscribers.
//
// Everything a client observes is an [Event]; everything a client sends is an
// [Op]. Both are concrete structs with a stable JSON envelope
// ({"type", "session_id", "seq", "time", ...payload}) so the same messages ride
// an in-process channel, a unix socket, or a network connection unchanged.
//
// Events are ordered per session by a monotonically increasing seq assigned at
// publish time. Two delivery tiers govern backpressure: stream deltas
// (message.delta, tool.call.delta) are lossy and may be dropped under load,
// while every terminal and lifecycle event is must-deliver. Settled *.finished
// payloads carry authoritative content, so a client that drops deltas still
// converges to the correct state.
package event

// Event kind identifiers. Each is the value returned by an event's Kind method
// and the "type" field of its JSON envelope.
const (
	KindSessionCreated   = "session.created"
	KindSessionResumed   = "session.resumed"
	KindSessionForked    = "session.forked"
	KindSessionCompacted = "session.compacted"
	KindSessionKilled    = "session.killed"
	KindSessionArchived  = "session.archived"
	KindSessionInfo      = "session.info"
	KindPlan             = "plan"

	KindTurnStarted  = "turn.started"
	KindTurnFinished = "turn.finished"

	KindMessageStarted  = "message.started"
	KindMessageDelta    = "message.delta"
	KindMessageFinished = "message.finished"

	KindToolCallStarted  = "tool.call.started"
	KindToolCallDelta    = "tool.call.delta"
	KindToolCallFinished = "tool.call.finished"

	KindPermissionRequested = "permission.requested"
	KindPermissionResolved  = "permission.resolved"

	KindSessionError = "session.error"
)

// MessageKind distinguishes the message channels carried by message.* events.
type MessageKind string

// The message kinds carried by message.* events.
const (
	MessageText      MessageKind = "text"
	MessageReasoning MessageKind = "reasoning"
	// MessageUser is the user's own prompt turn, published as a
	// MessageStarted/MessageFinished pair with no deltas (a user prompt isn't
	// streamed token-by-token) so every stream observer can render the user's
	// side of the transcript, not just the agent's reply.
	MessageUser MessageKind = "user"
)

// Verdict is a permission decision, using the allow/ask/deny grammar shared
// with the permission engine.
type Verdict string

// The permission verdicts.
const (
	VerdictAllow Verdict = "allow"
	VerdictAsk   Verdict = "ask"
	VerdictDeny  Verdict = "deny"
)

// Tier is a delivery guarantee. Lossy-tier events may be dropped under
// backpressure; must-deliver events are delivered with bounded blocking.
type Tier int

// The two delivery tiers.
const (
	// TierLossy events (stream deltas) are dropped when a subscriber's buffer
	// is full; the drop is counted, never hidden.
	TierLossy Tier = iota
	// TierMustDeliver events are never dropped: the broker blocks up to a bound
	// and force-unsubscribes a subscriber that stays wedged past it.
	TierMustDeliver
)

// String returns a human-readable tier name.
func (t Tier) String() string {
	switch t {
	case TierLossy:
		return "lossy"
	case TierMustDeliver:
		return "must-deliver"
	default:
		return "unknown"
	}
}

// TierOf reports the delivery tier for an event kind. Only stream deltas are
// lossy; every other kind is must-deliver.
func TierOf(kind string) Tier {
	switch kind {
	case KindMessageDelta, KindToolCallDelta:
		return TierLossy
	default:
		return TierMustDeliver
	}
}

// Filter selects which tiers a subscriber receives.
type Filter int

// The subscription filters.
const (
	// FilterAll delivers events of every tier.
	FilterAll Filter = iota
	// FilterMustDeliver delivers only must-deliver events, dropping lossy
	// deltas before they reach the subscriber.
	FilterMustDeliver
)

// accepts reports whether the filter admits an event of the given tier.
func (f Filter) accepts(t Tier) bool {
	if f == FilterMustDeliver {
		return t == TierMustDeliver
	}
	return true
}
