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
	// EntryMeta carries durable session-create metadata (currently the cwd).
	// It is written as the first entry in a freshly created journal, so it is
	// always the tree's root. It contributes nothing to [Journal.Fold].
	EntryMeta EntryType = "session_meta"
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

// MessagePayload is the [Entry.Payload] shape for [EntryMessage] entries. It
// stores a [provider.Message]'s content blocks verbatim — including each
// block's Meta (e.g. an Anthropic reasoning signature) — so the round trip
// through the journal is lossless.
type MessagePayload struct {
	// Role is the speaker: [provider.RoleUser], [provider.RoleAssistant], or
	// [provider.RoleSystem].
	Role provider.Role `json:"role"`
	// Blocks is the message's content blocks, verbatim.
	Blocks []provider.ContentBlock `json:"blocks"`
}

// ToolRoundPayload is the [Entry.Payload] shape for [EntryToolRound] entries:
// the content blocks (typically tool_use/tool_result blocks) assembled for
// one round of tool use, stored verbatim — including each block's Meta.
type ToolRoundPayload struct {
	Blocks []provider.ContentBlock `json:"blocks"`
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

// MetaPayload is the [Entry.Payload] shape for [EntryMeta] entries: durable
// session-create metadata. Extensible — future session metadata rides here
// alongside Cwd.
type MetaPayload struct {
	// Cwd is the working directory the session was created for.
	Cwd string `json:"cwd,omitempty"`
}

// ErrEntryType indicates a typed accessor ([Entry.Message], [Entry.ToolRound],
// [Entry.Compaction], [Entry.Fork]) was called on an entry of a different
// [EntryType].
var ErrEntryType = errors.New("session: entry type mismatch")

// entryConfig collects [EntryOpt] settings before an entry is constructed.
type entryConfig struct {
	model string
	usage *provider.Usage
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

// NewMessageEntry constructs a message entry from msg, storing its content
// blocks verbatim — including each block's Meta — so a whole turn's blocks
// persist losslessly. ID, Parent, and Time are left zero; [Journal.Append]
// fills them in so ids stay monotonic and parents chain correctly.
func NewMessageEntry(msg provider.Message, opts ...EntryOpt) Entry {
	cfg := newEntryConfig(opts)
	payload := MessagePayload{Role: msg.Role, Blocks: msg.Content}
	return Entry{
		Type:    EntryMessage,
		Model:   cfg.model,
		Usage:   cfg.usage,
		Payload: marshalPayload(payload),
	}
}

// NewToolRoundEntry constructs a tool-round entry from blocks (typically the
// tool_use/tool_result content blocks assembled for one round of tool use),
// storing them verbatim — including each block's Meta.
func NewToolRoundEntry(blocks []provider.ContentBlock, opts ...EntryOpt) Entry {
	cfg := newEntryConfig(opts)
	payload := ToolRoundPayload{Blocks: blocks}
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

// NewMetaEntry constructs a session-metadata entry carrying cwd. It is
// intended to be the first entry appended to a freshly created journal (see
// [runner.New]), so it becomes the tree's root. ID, Parent, and Time are left
// zero; [Journal.Append] fills them in.
func NewMetaEntry(cwd string) Entry {
	payload := MetaPayload{Cwd: cwd}
	return Entry{
		Type:    EntryMeta,
		Payload: marshalPayload(payload),
	}
}

// Message unmarshals e's payload as a [MessagePayload] and returns it
// projected back to a [provider.Message], with every content block (and its
// Meta) intact. It returns [ErrEntryType] if e is not an [EntryMessage].
func (e Entry) Message() (provider.Message, error) {
	if e.Type != EntryMessage {
		return provider.Message{}, fmt.Errorf("session: entry %s is %s, not %s: %w", e.ID, e.Type, EntryMessage, ErrEntryType)
	}
	var p MessagePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return provider.Message{}, fmt.Errorf("session: unmarshal message payload of entry %s: %w", e.ID, err)
	}
	return provider.Message{Role: p.Role, Content: p.Blocks}, nil
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

// Meta unmarshals e's payload as a [MetaPayload]. It returns [ErrEntryType]
// if e is not an [EntryMeta].
func (e Entry) Meta() (MetaPayload, error) {
	var p MetaPayload
	if e.Type != EntryMeta {
		return p, fmt.Errorf("session: entry %s is %s, not %s: %w", e.ID, e.Type, EntryMeta, ErrEntryType)
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return MetaPayload{}, fmt.Errorf("session: unmarshal session_meta payload of entry %s: %w", e.ID, err)
	}
	return p, nil
}
