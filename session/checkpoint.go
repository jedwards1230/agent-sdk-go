package session

import "time"

// Checkpoint is one named point in a session's journal: the resolved
// projection of an [EntryCheckpoint] entry.
//
// It is a distinct value type rather than a bare [Entry] on purpose. A
// consumer listing checkpoints wants the label, and reading it off an Entry
// means calling [Entry.Checkpoint] and handling a per-entry error for a
// payload the extraction already had to decode. Checkpoint carries the three
// fields a roster or a rewind picker needs and nothing else.
type Checkpoint struct {
	// ID is the checkpoint entry's id — the value to fork at to rewind here.
	ID string
	// Label is the human-readable name the checkpoint was created with.
	Label string
	// Time is when the checkpoint was appended.
	Time time.Time
}

// Checkpoints extracts every [EntryCheckpoint] in entries, in append order —
// the order the slice is already in, which is the order [Journal.Entries] and
// [ReadEntries] return.
//
// It works over a plain entry slice so the read path needs no live session:
// ReadEntries(path) → Checkpoints(entries) lists a persisted session's
// checkpoints without opening a journal for append, resuming it, or folding
// it. That is the path a roster uses.
//
// An entry whose payload fails to decode is skipped rather than failing the
// whole listing, matching how [Journal.Fold] treats a malformed payload; it
// cannot happen for entries built through [NewCheckpointEntry].
func Checkpoints(entries []Entry) []Checkpoint {
	var out []Checkpoint
	for _, e := range entries {
		if e.Type != EntryCheckpoint {
			continue
		}
		p, err := e.Checkpoint()
		if err != nil {
			continue
		}
		out = append(out, Checkpoint{ID: e.ID, Label: p.Label, Time: e.Time})
	}
	return out
}
