package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// JournalWriter is the append sink a Journal fsyncs each entry to. *os.File is
// the on-disk implementation used by FileStore; an in-memory writer (memWriter,
// used by MemStore) discards writes so an ephemeral session persists nothing.
//
// It is exported so a [MemStore] can be given a substitute sink via
// [WithMemJournalWriter] — the seam for exercising how code above the journal
// behaves when a durable append fails (ENOSPC, EIO), which is otherwise
// unreachable without a real disk fault.
type JournalWriter interface {
	io.Writer
	Sync() error
	Close() error
}

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
	w       JournalWriter // append handle; nil once closed

	idGen func() string
	clock func() time.Time
}

// newJournal constructs a Journal bound to path with pre-loaded entries
// (from [readJournal], possibly empty) and a writable append handle.
// Unexported: built only by a [Store]'s Create/Open.
func newJournal(id, projectSlug, path string, entries []Entry, w JournalWriter, idGen func() string, clock func() time.Time) *Journal {
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

// Dir returns the session's on-disk directory — a sibling of the journal file,
// named <id> alongside the <id>.jsonl journal (e.g.
// <root>/sessions/<slug>/<id>) — for per-session artifacts such as tool-output
// spill files. The directory coexists with the journal file and is invisible to
// the store's <id>.jsonl globs. Dir does not create the directory; a consumer
// makes it lazily when it first writes.
func (j *Journal) Dir() string { return strings.TrimSuffix(j.path, ".jsonl") }

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
// result — and no further ancestors are walked. [EntryForkPoint], [EntryMeta],
// and [EntryCheckpoint] entries are markers and contribute nothing — a
// checkpoint's label never enters the model's context. A malformed payload
// (which should not occur for entries built through the typed constructors)
// is skipped rather than causing Fold to fail. Every content block's Meta
// (e.g. a reasoning signature) is preserved verbatim, since it is stored
// verbatim in the journal.
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
	// The two halves of cost aggregation sit on opposite sides of this unlock,
	// on purpose. sumUsage is the journal-sized O(n) part and is pure — no
	// caller-supplied code is reachable from it — so it runs over j.entries in
	// place, replacing what used to be a journal-sized copy of 120-byte structs
	// with a walk of pointer dereferences and integer adds. priceUsage is the
	// half that invokes reg, a caller-supplied PriceLookup, and it only ever
	// touches the per-model map (bounded by the number of distinct models, not
	// by the journal length).
	//
	// Do NOT re-inline these into one locked call: reg is arbitrary caller code,
	// and a PriceLookup that reaches back into this journal (j.Len, j.Head,
	// j.Cost) would then deadlock on j.mu. Holding the lock across reg is the
	// regression this split exists to prevent — see the re-entrancy test.
	j.mu.Lock()
	usageByModel, total := sumUsage(j.entries)
	j.mu.Unlock()
	return priceUsage(usageByModel, total, reg)
}

// LastUsage returns the model and token usage of the most recently completed
// turn-bearing entry within the session's CURRENT folded context — the same
// walk [Journal.Fold] performs (HEAD back to root, or a compaction boundary),
// so a fork-abandoned branch is never reported here even though it still
// counts toward [Journal.Cost].
//
// It is the synchronous complement to a live turn-finished event on the
// broker (which carries the identical usage the moment a turn settles, plus
// the model's context-window size via the provider registry): a caller that
// has not observed one yet in this process — most notably right after a
// resume, before any new turn has run — reads it here instead. Pair the
// returned model with the provider registry for a context-window size, the
// same way a live turn-finished event derives one.
//
// ok is false when no entry in the current context carries usage: a fresh
// session, or one whose only turns were dropped by a fork.
func (j *Journal) LastUsage() (model string, usage provider.Usage, ok bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	// This walks exactly the chain [chainFromHead] would materialize — from the
	// last entry in append order back along Parent links, stopping at (but
	// including) a compaction boundary, the root, or a dangling parent — but in
	// place, over j.entries and the journal's OWN j.byID index rather than a
	// copy of the entries and a rebuilt id map. Child→root order means the
	// first entry carrying usage IS the most recent one, so the walk also
	// early-exits there, which on a live session is at or near the tail.
	//
	// Nothing reachable from this walk is caller-supplied, so unlike
	// [Journal.Cost] there is no re-entrancy hazard in holding j.mu throughout.
	//
	// The len(j.entries) step bound cannot change the answer for a well-formed
	// journal: parent links are acyclic and ids unique, so the walk visits each
	// entry at most once and terminates in at most len(j.entries) steps anyway.
	// It only stops a corrupt journal (a parent cycle, or duplicate ids forming
	// one) from spinning forever — which, under the lock, would wedge every
	// other operation on the session rather than just this call.
	i := len(j.entries) - 1
	for steps := len(j.entries); steps > 0; steps-- {
		e := &j.entries[i]
		if e.Usage != nil {
			return e.Model, *e.Usage, true
		}
		if e.Type == EntryCompaction || e.Parent == "" {
			break
		}
		parent, found := j.byID[e.Parent]
		if !found {
			break // dangling parent: stop walking defensively
		}
		i = parent
	}
	return "", provider.Usage{}, false
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

// reopen re-arms a Closed journal with a fresh append sink so an in-memory
// store can resume it within the process. It is a no-op on a journal that is
// still open. Only [MemStore.Open] uses it; [FileStore] resumes by rebuilding
// a *Journal from disk instead.
func (j *Journal) reopen(w JournalWriter) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.w == nil {
		j.w = w
	}
}

// chainFromHead walks parent links from HEAD back toward the root, collecting
// in child→root order, and stops at (but includes) a compaction boundary —
// exactly the entries that make up the session's CURRENT context. It
// materializes that chain for [fold], now its only caller. Returns nil for an
// empty journal.
//
// [Journal.LastUsage] no longer calls this: it walks the same links in place,
// under the journal lock and without allocating. So the two agree on what
// "current context" means BY CONVENTION rather than by construction — change a
// stop condition here and it must change there too, or Fold and LastUsage will
// silently disagree about which entries are in context.
//
// Unlike LastUsage's walk, this one is unbounded. A journal whose ids are not
// unique — a corrupt or concatenated file, carrying ids this process did not
// generate — can form a parent cycle that spins here forever, appending on
// every iteration. LastUsage's len(entries) step bound is the fixed form;
// applying it here is deliberately left to a follow-up, since fold's contract
// has its own equivalence surface.
func chainFromHead(entries []Entry) []Entry {
	if len(entries) == 0 {
		return nil
	}

	byID := make(map[string]int, len(entries))
	for i, e := range entries {
		byID[e.ID] = i
	}

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
	return chain
}

// fold implements the pure, lock-free half of [Journal.Fold] over a snapshot
// of entries in append order.
func fold(entries []Entry) []provider.Message {
	chain := chainFromHead(entries)
	if len(chain) == 0 {
		return nil
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
// rendering rules. ok is false for the marker entry types — fork_point,
// session_meta, checkpoint — and for entries whose payload fails to
// unmarshal. The default case skips any type this build does not know, so a
// journal written by a newer producer degrades to dropping the unknown entry
// rather than mis-rendering it into the model's context; the marker types are
// still listed explicitly, because a contract should not rest on falling
// through.
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
	case EntryMeta:
		return provider.Message{}, false
	case EntryCheckpoint:
		return provider.Message{}, false
	default:
		return provider.Message{}, false
	}
}
