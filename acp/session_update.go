package acp

import "encoding/json"

// SessionUpdate is the tagged union carried by a [SessionNotification], the
// payload of a session/update notification. It is a closed union: only the
// concrete types in this package implement it, discriminated on the wire by
// their "sessionUpdate" field.
type SessionUpdate interface {
	// Kind returns the variant's "sessionUpdate" discriminator value.
	Kind() string

	json.Marshaler
}

// SessionNotification is the payload of a session/update notification: one
// incremental piece of session state, addressed to a session.
type SessionNotification struct {
	// SessionID is the session the update belongs to.
	SessionID string
	// Update is the tagged update payload.
	Update SessionUpdate
}

// MarshalJSON encodes {"sessionId":...,"update":...}.
func (n SessionNotification) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SessionID string        `json:"sessionId"`
		Update    SessionUpdate `json:"update"`
	}{n.SessionID, n.Update})
}

// contentChunk is the shared shape of the three message-chunk session/update
// variants: a single wrapped content block.
type contentChunk struct {
	kind    string
	Content ContentBlock
}

func (c contentChunk) marshal() ([]byte, error) {
	return json.Marshal(struct {
		SessionUpdate string       `json:"sessionUpdate"`
		Content       ContentBlock `json:"content"`
	}{c.kind, c.Content})
}

// UserMessageChunk carries an incremental chunk of a user message.
type UserMessageChunk struct {
	// Content is the chunk's content block.
	Content ContentBlock
}

// Kind returns "user_message_chunk".
func (UserMessageChunk) Kind() string { return "user_message_chunk" }

// MarshalJSON encodes the tagged user_message_chunk session/update payload.
func (c UserMessageChunk) MarshalJSON() ([]byte, error) {
	return contentChunk{kind: c.Kind(), Content: c.Content}.marshal()
}

// AgentMessageChunk carries an incremental chunk of an agent message.
type AgentMessageChunk struct {
	// Content is the chunk's content block.
	Content ContentBlock
}

// Kind returns "agent_message_chunk".
func (AgentMessageChunk) Kind() string { return "agent_message_chunk" }

// MarshalJSON encodes the tagged agent_message_chunk session/update payload.
func (c AgentMessageChunk) MarshalJSON() ([]byte, error) {
	return contentChunk{kind: c.Kind(), Content: c.Content}.marshal()
}

// SessionInfoUpdate carries a change to a session's metadata — currently its
// human-readable title and, optionally, the time it changed. It is the
// session_info_update session/update variant; a client updates its session
// list/title UI in place from it (the same fields [SessionInfo] carries in a
// session/list entry).
type SessionInfoUpdate struct {
	// Title is the session's new human-readable title.
	Title string
	// UpdatedAt is an optional ISO 8601 timestamp of when the title changed.
	UpdatedAt string
}

// Kind returns "session_info_update".
func (SessionInfoUpdate) Kind() string { return "session_info_update" }

// MarshalJSON encodes the tagged session_info_update session/update payload.
func (u SessionInfoUpdate) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SessionUpdate string `json:"sessionUpdate"`
		Title         string `json:"title"`
		UpdatedAt     string `json:"updatedAt,omitempty"`
	}{u.Kind(), u.Title, u.UpdatedAt})
}

// AgentThoughtChunk carries an incremental chunk of agent reasoning.
type AgentThoughtChunk struct {
	// Content is the chunk's content block.
	Content ContentBlock
}

// Kind returns "agent_thought_chunk".
func (AgentThoughtChunk) Kind() string { return "agent_thought_chunk" }

// MarshalJSON encodes the tagged agent_thought_chunk session/update payload.
func (c AgentThoughtChunk) MarshalJSON() ([]byte, error) {
	return contentChunk{kind: c.Kind(), Content: c.Content}.marshal()
}

// Cost is a priced monetary amount in a single ISO 4217 currency, as carried by
// a [UsageUpdate]. It is the ACP v1 Cost object.
type Cost struct {
	// Amount is the cost's value in the currency's major unit.
	Amount float64
	// Currency is the ISO 4217 currency code (e.g. "USD").
	Currency string
}

// MarshalJSON encodes {"amount":...,"currency":...}.
func (c Cost) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Amount   float64 `json:"amount"`
		Currency string  `json:"currency"`
	}{c.Amount, c.Currency})
}

// UsageUpdate reports a session's current context-window usage as a
// session/update variant: how many tokens currently occupy the context out of
// the model's total window, and optionally the turn's cost.
type UsageUpdate struct {
	// Used is the number of tokens currently in context.
	Used uint64
	// Size is the model's total context-window size in tokens.
	Size uint64
	// Cost is the priced cost, or nil when unpriced.
	Cost *Cost
}

// Kind returns "usage_update".
func (UsageUpdate) Kind() string { return "usage_update" }

// MarshalJSON encodes the tagged usage_update session/update payload
// {"sessionUpdate":"usage_update","used":...,"size":...,"cost"?:...}.
func (u UsageUpdate) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SessionUpdate string `json:"sessionUpdate"`
		Used          uint64 `json:"used"`
		Size          uint64 `json:"size"`
		Cost          *Cost  `json:"cost,omitempty"`
	}{u.Kind(), u.Used, u.Size, u.Cost})
}
