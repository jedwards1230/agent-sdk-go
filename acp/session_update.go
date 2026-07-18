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

// ConfigOptionUpdate is the config_option_update session/update variant: the
// agent advertising the full set of session configuration options and their
// current values to the client, so it renders live selectors (model, mode,
// thought-level, boolean toggles). It reuses the [ConfigOption] shape a
// [SetConfigOptionResponse] carries; a client replaces its config UI from this
// authoritative snapshot. An empty set marshals to an empty configOptions array
// (a valid "no options" state), not an absent field.
type ConfigOptionUpdate struct {
	// ConfigOptions is the full current set of config options.
	ConfigOptions []ConfigOption
}

// Kind returns "config_option_update".
func (ConfigOptionUpdate) Kind() string { return "config_option_update" }

// MarshalJSON encodes the tagged config_option_update session/update payload
// {"sessionUpdate":"config_option_update","configOptions":[...]}. A nil set
// marshals to "[]" so a client can distinguish a cleared set from an absent
// field.
func (u ConfigOptionUpdate) MarshalJSON() ([]byte, error) {
	opts := u.ConfigOptions
	if opts == nil {
		opts = []ConfigOption{}
	}
	return json.Marshal(struct {
		SessionUpdate string         `json:"sessionUpdate"`
		ConfigOptions []ConfigOption `json:"configOptions"`
	}{u.Kind(), opts})
}

// PlanEntry is one item in an agent's task plan: what the step is, its priority,
// and its status. It is the ACP v1 PlanEntry object carried by a [Plan] update;
// all three fields are required by the schema.
type PlanEntry struct {
	// Content is the human-readable description of the task.
	Content string
	// Priority is one of "high", "medium", "low".
	Priority string
	// Status is one of "pending", "in_progress", "completed".
	Status string
}

// MarshalJSON encodes {content, priority, status}.
func (e PlanEntry) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Content  string `json:"content"`
		Priority string `json:"priority"`
		Status   string `json:"status"`
	}{e.Content, e.Priority, e.Status})
}

// Plan is the `plan` session/update variant: the agent's current task plan as an
// ordered checklist of entries. The agent advertises the full plan on each
// update (not a delta), so a client renders it as a live to-do list. An empty
// plan marshals to an empty entries array — a valid "plan cleared" state, not an
// absent field.
type Plan struct {
	// Entries is the full current plan, in order.
	Entries []PlanEntry
}

// Kind returns "plan".
func (Plan) Kind() string { return "plan" }

// MarshalJSON encodes the tagged plan session/update payload.
func (p Plan) MarshalJSON() ([]byte, error) {
	entries := p.Entries
	if entries == nil {
		entries = []PlanEntry{}
	}
	return json.Marshal(struct {
		SessionUpdate string      `json:"sessionUpdate"`
		Entries       []PlanEntry `json:"entries"`
	}{p.Kind(), entries})
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
