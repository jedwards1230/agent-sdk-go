package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// EntryType tags the kind of node stored in a session's journal.
type EntryType string

// The journal entry type variants. See [Journal] for the tree/fold model
// they participate in.
const (
	// EntryMessage is a single conversation turn (user, assistant, or system).
	EntryMessage EntryType = "message"
	// EntryToolRound is one round of tool calls and their settled results.
	EntryToolRound EntryType = "tool_round"
	// EntryForkPoint marks a branch: its Parent is an older entry rather than
	// the entry that preceded it in append order. It contributes nothing to
	// [Journal.Fold].
	EntryForkPoint EntryType = "fork_point"
	// EntryCompaction summarizes every ancestor entry before it; [Journal.Fold]
	// stops walking ancestors once it reaches one.
	EntryCompaction EntryType = "compaction"
)

// Entry is one immutable node in a session's tree — the unit of one JSONL
// line. Parent links between entries form the tree; HEAD is always the last
// entry in append order, never separately persisted.
type Entry struct {
	// ID is the entry's UUIDv7, time-ordered identifier.
	ID string `json:"id"`
	// Parent is the id of this entry's parent, or "" for the root entry.
	Parent string `json:"parent,omitempty"`
	// Type tags which payload shape this entry carries.
	Type EntryType `json:"type"`
	// Time is when the entry was appended.
	Time time.Time `json:"time"`
	// Model is the model that produced a turn-bearing entry, if any.
	Model string `json:"model,omitempty"`
	// Usage is the token usage a turn-bearing entry consumed, if any.
	Usage *provider.Usage `json:"usage,omitempty"`
	// Payload is the type-specific body; unmarshal it via [Entry.Message],
	// [Entry.ToolRound], [Entry.Compaction], or [Entry.Fork].
	Payload json.RawMessage `json:"payload,omitempty"`
}

// MessagePayload is the [Entry.Payload] shape for [EntryMessage] entries.
type MessagePayload struct {
	// Role is the speaker: "user", "assistant", or "system".
	Role string `json:"role"`
	// Content is the settled message text.
	Content string `json:"content"`
	// Reasoning is the settled reasoning text, if any.
	Reasoning string `json:"reasoning,omitempty"`
}

// ToolRoundPayload is the [Entry.Payload] shape for [EntryToolRound] entries.
type ToolRoundPayload struct {
	Calls []ToolCallRecord `json:"calls"`
}

// ToolCallRecord is one tool invocation and its settled result within a
// [ToolRoundPayload].
type ToolCallRecord struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input,omitempty"`
	Result  string          `json:"result,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
}

// CompactionPayload is the [Entry.Payload] shape for [EntryCompaction]
// entries: Summary stands in for every entry [Journal.Fold] would otherwise
// walk before it.
type CompactionPayload struct {
	// Summary is the rendered replacement for everything summarized.
	Summary string `json:"summary"`
	// ReplacesThrough is the id of the last entry the summary covers.
	ReplacesThrough string `json:"replaces_through,omitempty"`
}

// ForkPayload is the [Entry.Payload] shape for [EntryForkPoint] entries. From
// duplicates the entry's Parent field, kept explicit in the payload for
// audit.
type ForkPayload struct {
	From string `json:"from"`
}

// ErrEntryType indicates a typed accessor ([Entry.Message], [Entry.ToolRound],
// [Entry.Compaction], [Entry.Fork]) was called on an entry of a different
// [EntryType].
var ErrEntryType = errors.New("session: entry type mismatch")

// entryConfig collects [EntryOpt] settings before an entry is constructed.
type entryConfig struct {
	model     string
	usage     *provider.Usage
	reasoning string
}

// EntryOpt configures an entry at construction; see [NewMessageEntry] and
// [NewToolRoundEntry].
type EntryOpt func(*entryConfig)

// WithEntryModel sets the model that produced a turn-bearing entry.
func WithEntryModel(model string) EntryOpt {
	return func(c *entryConfig) { c.model = model }
}

// WithEntryUsage sets the token usage a turn-bearing entry consumed.
func WithEntryUsage(u provider.Usage) EntryOpt {
	return func(c *entryConfig) { c.usage = &u }
}

// WithReasoning sets a message entry's reasoning text. It has no effect on
// entry types other than [EntryMessage].
func WithReasoning(reasoning string) EntryOpt {
	return func(c *entryConfig) { c.reasoning = reasoning }
}

func newEntryConfig(opts []EntryOpt) entryConfig {
	var c entryConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// marshalPayload marshals v — one of the payload types above, all static
// structs of strings/slices/bools — into an [Entry.Payload]. These types
// cannot fail to marshal; the error path is unreachable in practice and
// degrades to a null payload rather than panicking.
func marshalPayload(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// NewMessageEntry constructs a message entry. ID, Parent, and Time are left
// zero; [Journal.Append] fills them in so ids stay monotonic and parents
// chain correctly.
func NewMessageEntry(role, content string, opts ...EntryOpt) Entry {
	cfg := newEntryConfig(opts)
	payload := MessagePayload{Role: role, Content: content, Reasoning: cfg.reasoning}
	return Entry{
		Type:    EntryMessage,
		Model:   cfg.model,
		Usage:   cfg.usage,
		Payload: marshalPayload(payload),
	}
}

// NewToolRoundEntry constructs a tool-round entry from calls.
func NewToolRoundEntry(calls []ToolCallRecord, opts ...EntryOpt) Entry {
	cfg := newEntryConfig(opts)
	payload := ToolRoundPayload{Calls: calls}
	return Entry{
		Type:    EntryToolRound,
		Model:   cfg.model,
		Usage:   cfg.usage,
		Payload: marshalPayload(payload),
	}
}

// NewCompactionEntry constructs a compaction entry summarizing everything up
// to and including replacesThrough.
func NewCompactionEntry(summary, replacesThrough string) Entry {
	payload := CompactionPayload{Summary: summary, ReplacesThrough: replacesThrough}
	return Entry{
		Type:    EntryCompaction,
		Payload: marshalPayload(payload),
	}
}

// newForkPointEntry constructs the fork_point entry [Journal.Fork] appends.
// Its Parent (filled in by Append) and Payload.From both equal at.
func newForkPointEntry(at string) Entry {
	payload := ForkPayload{From: at}
	return Entry{
		Type:    EntryForkPoint,
		Payload: marshalPayload(payload),
	}
}

// Message unmarshals e's payload as a [MessagePayload]. It returns
// [ErrEntryType] if e is not an [EntryMessage].
func (e Entry) Message() (MessagePayload, error) {
	var p MessagePayload
	if e.Type != EntryMessage {
		return p, fmt.Errorf("session: entry %s is %s, not %s: %w", e.ID, e.Type, EntryMessage, ErrEntryType)
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return MessagePayload{}, fmt.Errorf("session: unmarshal message payload of entry %s: %w", e.ID, err)
	}
	return p, nil
}

// ToolRound unmarshals e's payload as a [ToolRoundPayload]. It returns
// [ErrEntryType] if e is not an [EntryToolRound].
func (e Entry) ToolRound() (ToolRoundPayload, error) {
	var p ToolRoundPayload
	if e.Type != EntryToolRound {
		return p, fmt.Errorf("session: entry %s is %s, not %s: %w", e.ID, e.Type, EntryToolRound, ErrEntryType)
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return ToolRoundPayload{}, fmt.Errorf("session: unmarshal tool_round payload of entry %s: %w", e.ID, err)
	}
	return p, nil
}

// Compaction unmarshals e's payload as a [CompactionPayload]. It returns
// [ErrEntryType] if e is not an [EntryCompaction].
func (e Entry) Compaction() (CompactionPayload, error) {
	var p CompactionPayload
	if e.Type != EntryCompaction {
		return p, fmt.Errorf("session: entry %s is %s, not %s: %w", e.ID, e.Type, EntryCompaction, ErrEntryType)
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return CompactionPayload{}, fmt.Errorf("session: unmarshal compaction payload of entry %s: %w", e.ID, err)
	}
	return p, nil
}

// Fork unmarshals e's payload as a [ForkPayload]. It returns [ErrEntryType]
// if e is not an [EntryForkPoint].
func (e Entry) Fork() (ForkPayload, error) {
	var p ForkPayload
	if e.Type != EntryForkPoint {
		return p, fmt.Errorf("session: entry %s is %s, not %s: %w", e.ID, e.Type, EntryForkPoint, ErrEntryType)
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return ForkPayload{}, fmt.Errorf("session: unmarshal fork_point payload of entry %s: %w", e.ID, err)
	}
	return p, nil
}
