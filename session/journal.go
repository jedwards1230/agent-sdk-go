package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// ErrEntryNotFound is returned by [Journal.Fork] when the requested fork
// point does not exist in the journal.
var ErrEntryNotFound = errors.New("session: entry not found")

// ErrJournalClosed is returned by [Journal.Append] and [Journal.Fork] once
// the journal's file handle has been closed.
var ErrJournalClosed = errors.New("session: journal closed")

// Journal is one session's append-only, event-sourced tree: a JSONL file of
// [Entry] values whose Parent links form a tree. The journal is the single
// source of truth — HEAD (the last entry in append order), the folded
// context ([Journal.Fold]), and cost ([Journal.Cost]) are all derived from
// it. That derivation is what makes resuming a session from disk robust:
// there is no separate HEAD state to lose or desync.
//
// A Journal is constructed by a [Store]'s Create or Open, never directly.
// It is safe for concurrent use.
type Journal struct {
	id          string
	projectSlug string
	path        string

	mu      sync.Mutex
	entries []Entry
	byID    map[string]int
	w       *os.File // append handle; nil once closed

	idGen func() string
	clock func() time.Time
}

// newJournal constructs a Journal bound to path with pre-loaded entries
// (from [readJournal], possibly empty) and a writable append handle.
// Unexported: built only by a [Store]'s Create/Open.
func newJournal(id, projectSlug, path string, entries []Entry, w *os.File, idGen func() string, clock func() time.Time) *Journal {
	byID := make(map[string]int, len(entries))
	for i, e := range entries {
		byID[e.ID] = i
	}
	return &Journal{
		id:          id,
		projectSlug: projectSlug,
		path:        path,
		entries:     entries,
		byID:        byID,
		w:           w,
		idGen:       idGen,
		clock:       clock,
	}
}

// isOpen reports whether the journal still has a live append handle (i.e. has
// not been [Journal.Close]d). Used by [journalCache] to decide whether a
// cached journal is still usable — see the cache's type doc for why a live
// journal is never TTL-evicted.
func (j *Journal) isOpen() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.w != nil
}

// ID returns the journal's session id.
func (j *Journal) ID() string { return j.id }

// ProjectSlug returns the project the session belongs to.
func (j *Journal) ProjectSlug() string { return j.projectSlug }

// Path returns the journal's JSONL file path.
func (j *Journal) Path() string { return j.path }

// Head returns the id of the current HEAD entry (the last entry in append
// order), or "" if the journal is empty.
func (j *Journal) Head() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.headLocked()
}

// headLocked returns the current HEAD id. Callers must hold j.mu.
func (j *Journal) headLocked() string {
	if n := len(j.entries); n > 0 {
		return j.entries[n-1].ID
	}
	return ""
}

// Len returns the number of entries in the journal.
func (j *Journal) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.entries)
}

// Append fills id (via the journal's id generator), Time (via its clock),
// and Parent (= current HEAD) on a copy of e, writes it as one JSON line to
// the journal file (creating it if needed, 0600), fsyncs, updates in-memory
// state, advances HEAD to the new entry, and returns the stored entry.
// Append is safe for concurrent use.
func (j *Journal) Append(e Entry) (Entry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.appendLocked(e, j.headLocked())
}

// Fork appends a fork_point entry parented on at (which must already exist
// in the journal) and makes it HEAD. Subsequent appends chain onto it, so
// [Journal.Fold] now walks the branch through at instead of whatever
// followed at previously — those entries remain in the log (and still count
// toward [Journal.Cost]) but drop out of context.
func (j *Journal) Fork(at string) (Entry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if _, ok := j.byID[at]; !ok {
		return Entry{}, fmt.Errorf("session: fork point %s not found in journal %s: %w", at, j.id, ErrEntryNotFound)
	}
	return j.appendLocked(newForkPointEntry(at), at)
}

// appendLocked performs the shared write path for Append and Fork. Callers
// must hold j.mu.
func (j *Journal) appendLocked(e Entry, parent string) (Entry, error) {
	if j.w == nil {
		return Entry{}, fmt.Errorf("session: journal %s: %w", j.id, ErrJournalClosed)
	}

	e.ID = j.idGen()
	e.Time = j.clock()
	e.Parent = parent

	line, err := json.Marshal(e)
	if err != nil {
		return Entry{}, fmt.Errorf("session: marshal entry for journal %s: %w", j.id, err)
	}
	line = append(line, '\n')

	if _, err := j.w.Write(line); err != nil {
		return Entry{}, fmt.Errorf("session: append entry to journal %s: %w", j.id, err)
	}
	if err := j.w.Sync(); err != nil {
		return Entry{}, fmt.Errorf("session: sync journal %s: %w", j.id, err)
	}

	j.byID[e.ID] = len(j.entries)
	j.entries = append(j.entries, e)
	return e, nil
}

// Entries returns a copy of the full append-order log — every branch, not
// just the folded path.
func (j *Journal) Entries() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Entry, len(j.entries))
	copy(out, j.entries)
	return out
}

// Fold returns the session's context as a [] provider.Message ready to hand a
// provider directly: fold(root→HEAD). It walks parent links from HEAD back
// toward the root, then renders in root-to-head order. An [EntryCompaction]
// entry encountered while walking backward is the boundary: it is included —
// rendered as a user-role message carrying its summary text, first in the
// result — and no further ancestors are walked. [EntryForkPoint] entries are
// markers and contribute nothing. A malformed payload (which should not occur
// for entries built through the typed constructors) is skipped rather than
// causing Fold to fail. Every content block's Meta (e.g. a reasoning
// signature) is preserved verbatim, since it is stored verbatim in the
// journal.
func (j *Journal) Fold() []provider.Message {
	j.mu.Lock()
	entries := make([]Entry, len(j.entries))
	copy(entries, j.entries)
	j.mu.Unlock()
	return fold(entries)
}

// Cost aggregates token usage over ALL entries — every branch, including
// ones dropped from Fold by a fork — priced via reg (pass
// [RegistryPricing] for the built-in provider model registry, or nil to sum
// tokens without pricing). See cost.go.
func (j *Journal) Cost(reg PriceLookup) CostReport {
	j.mu.Lock()
	entries := make([]Entry, len(j.entries))
	copy(entries, j.entries)
	j.mu.Unlock()
	return cost(entries, reg)
}

// Close closes the journal's append handle. It is idempotent: closing an
// already-closed journal returns nil. Once closed, Append and Fork return
// [ErrJournalClosed].
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.w == nil {
		return nil
	}
	err := j.w.Close()
	j.w = nil
	if err != nil {
		return fmt.Errorf("session: close journal %s: %w", j.id, err)
	}
	return nil
}

// fold implements the pure, lock-free half of [Journal.Fold] over a snapshot
// of entries in append order.
func fold(entries []Entry) []provider.Message {
	if len(entries) == 0 {
		return nil
	}

	byID := make(map[string]int, len(entries))
	for i, e := range entries {
		byID[e.ID] = i
	}

	// Walk parent links from HEAD back toward the root, collecting in
	// child→root order; stop at (but include) a compaction boundary.
	chain := make([]Entry, 0, len(entries))
	cur := entries[len(entries)-1]
	for {
		chain = append(chain, cur)
		if cur.Type == EntryCompaction || cur.Parent == "" {
			break
		}
		idx, ok := byID[cur.Parent]
		if !ok {
			break // dangling parent: stop walking defensively
		}
		cur = entries[idx]
	}

	out := make([]provider.Message, 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		if m, ok := renderContext(chain[i]); ok {
			out = append(out, m)
		}
	}
	return out
}

// renderContext renders one entry into a [provider.Message] per the Fold
// rendering rules. ok is false for fork_point entries (skipped) and for
// entries whose payload fails to unmarshal.
func renderContext(e Entry) (provider.Message, bool) {
	switch e.Type {
	case EntryMessage:
		msg, err := e.Message()
		if err != nil {
			return provider.Message{}, false
		}
		return msg, true
	case EntryToolRound:
		p, err := e.ToolRound()
		if err != nil {
			return provider.Message{}, false
		}
		return provider.Message{Role: provider.RoleUser, Content: p.Blocks}, true
	case EntryCompaction:
		p, err := e.Compaction()
		if err != nil {
			return provider.Message{}, false
		}
		return provider.Message{Role: provider.RoleUser, Content: []provider.ContentBlock{provider.TextBlock(p.Summary)}}, true
	case EntryForkPoint:
		return provider.Message{}, false
	default:
		return provider.Message{}, false
	}
}
