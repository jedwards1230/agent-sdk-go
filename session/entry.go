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
	// EntryMeta carries durable session-create metadata (currently the cwd and
	// an optional embedder-owned role). It is written as the first entry in a
	// freshly created journal, so it is always the tree's root. It contributes
	// nothing to [Journal.Fold].
	EntryMeta EntryType = "session_meta"
	// EntryCheckpoint names a point in the journal so it can be addressed
	// later by label (see [Checkpoints] and runner.Rewind). It is a marker,
	// like [EntryForkPoint] and [EntryMeta]: it contributes nothing to
	// [Journal.Fold], so a checkpoint never enters the model's context.
	EntryCheckpoint EntryType = "checkpoint"
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
	// [Entry.ToolRound], [Entry.Compaction], [Entry.Fork], [Entry.Meta], or
	// [Entry.Checkpoint].
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

// CheckpointPayload is the [Entry.Payload] shape for [EntryCheckpoint]
// entries: the human-readable name given to this point in the journal. The
// name lives in the append-only log rather than a consumer-side sidecar, so it
// cannot desync from the entries it addresses.
type CheckpointPayload struct {
	// Label is the checkpoint's name, opaque to the SDK.
	Label string `json:"label"`
}

// MetaPayload is the [Entry.Payload] shape for [EntryMeta] entries: durable
// session-create metadata. Extensible — future session metadata rides here
// alongside Cwd, as optional omitempty fields set through a [MetaOpt], so an
// older journal reads back unchanged.
type MetaPayload struct {
	// Cwd is the working directory the session was created for.
	Cwd string `json:"cwd,omitempty"`
	// ParentID is the id of the session that spawned this one, or empty for a
	// root session. Set through [WithMetaParent]; see [Depth].
	ParentID string `json:"parent_id,omitempty"`
	// Depth is this session's parent-chain length: 0 for a root session, 1 for a
	// session spawned by a root, and so on. It is persisted rather than derived
	// so a spawn-depth cap survives a process restart without walking the chain
	// (each ancestor journal would have to be opened to recompute it). Set
	// through [WithMetaParent].
	Depth int `json:"depth,omitempty"`
	// Role is an opaque, embedder-owned classification of what kind of thing
	// this session is (e.g. "monitor"), or empty when unclassified. It lets a
	// roster classify a session from [ReadEntries] alone — without resuming or
	// folding it.
	//
	// The SDK defines no vocabulary of roles and attaches no behavior to any
	// value: it carries and surfaces the string, exactly as it does for the
	// session title and runner.Options.Agent. The embedder owns its meaning.
	Role string `json:"role,omitempty"`
}

// MetaOpt sets an optional field on the [MetaPayload] a [NewMetaEntry] builds.
// Metadata fields are additive: each rides here as its own option so a new
// field never changes NewMetaEntry's signature.
type MetaOpt func(*MetaPayload)

// WithMetaParent records that the session was spawned by parentID and sits at
// depth in the session tree. The two travel as one option because they are one
// fact — a child's link to its parent and how far down the chain that link sits.
// Setting either alone is meaningless: a parent id with no depth cannot be
// capped without re-walking the chain, and a depth with no parent id names no
// tree.
//
// Both fields are omitempty, so a root session (the caller simply does not pass
// this option) writes exactly the metadata it wrote before these fields existed,
// and an older journal reads back unchanged.
func WithMetaParent(parentID string, depth int) MetaOpt {
	return func(p *MetaPayload) {
		p.ParentID = parentID
		p.Depth = depth
	}
}

// WithMetaRole sets the session's opaque, embedder-owned role (see
// [MetaPayload.Role]). An empty role is the unclassified default and is
// omitted from the journal.
func WithMetaRole(role string) MetaOpt {
	return func(p *MetaPayload) { p.Role = role }
}

// ErrEntryType indicates a typed accessor ([Entry.Message], [Entry.ToolRound],
// [Entry.Compaction], [Entry.Fork], [Entry.Meta], [Entry.Checkpoint]) was
// called on an entry of a different [EntryType].
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
// to and including replacesThrough. opts optionally attach the model and
// token usage the summarization call spent producing summary (see
// [WithEntryModel], [WithEntryUsage]) — the same per-entry accounting a
// message or tool-round entry carries, so a compaction's own cost folds into
// [Journal.Cost] like any other turn rather than being invisible. Both are
// left zero when the summarization strategy made no model call.
func NewCompactionEntry(summary, replacesThrough string, opts ...EntryOpt) Entry {
	cfg := newEntryConfig(opts)
	payload := CompactionPayload{Summary: summary, ReplacesThrough: replacesThrough}
	return Entry{
		Type:    EntryCompaction,
		Model:   cfg.model,
		Usage:   cfg.usage,
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

// NewCheckpointEntry constructs a checkpoint entry naming the point in the
// journal it is appended at. ID, Parent, and Time are left zero;
// [Journal.Append] fills them in, so the checkpoint's parent is whatever HEAD
// was when it was appended — that entry is the one a later rewind branches
// from. It contributes nothing to [Journal.Fold].
func NewCheckpointEntry(label string) Entry {
	payload := CheckpointPayload{Label: label}
	return Entry{
		Type:    EntryCheckpoint,
		Payload: marshalPayload(payload),
	}
}

// NewMetaEntry constructs a session-metadata entry carrying cwd, plus
// whatever optional metadata opts set. It is intended to be the first entry
// appended to a freshly created journal (see [runner.New]), so it becomes the
// tree's root. ID, Parent, and Time are left zero; [Journal.Append] fills them
// in.
func NewMetaEntry(cwd string, opts ...MetaOpt) Entry {
	payload := MetaPayload{Cwd: cwd}
	for _, o := range opts {
		o(&payload)
	}
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

// Checkpoint unmarshals e's payload as a [CheckpointPayload]. It returns
// [ErrEntryType] if e is not an [EntryCheckpoint].
func (e Entry) Checkpoint() (CheckpointPayload, error) {
	var p CheckpointPayload
	if e.Type != EntryCheckpoint {
		return p, fmt.Errorf("session: entry %s is %s, not %s: %w", e.ID, e.Type, EntryCheckpoint, ErrEntryType)
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return CheckpointPayload{}, fmt.Errorf("session: unmarshal checkpoint payload of entry %s: %w", e.ID, err)
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
