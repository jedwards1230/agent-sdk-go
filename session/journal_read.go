package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
)

// ErrCorruptJournal indicates a journal file cannot be trusted: an interior
// line failed to parse (data loss beyond a torn-write tail), or its Parent
// links form a cycle rather than a tree.
var ErrCorruptJournal = errors.New("session: corrupt journal")

// readJournal opens path and parses each line into an [Entry], in append
// order. If path does not exist, it returns (nil, nil) — a brand new session
// has no file yet.
//
// Torn-write safety: if the file's FINAL non-empty line fails to unmarshal —
// a partial write left by a process killed mid-[Journal.Append] — it is
// dropped, the file is physically truncated to the last good line, and a
// warning is logged via logf. An INTERIOR line that fails to parse is real
// corruption: readJournal returns [ErrCorruptJournal] rather than silently
// dropping data. A nil logf defaults to [log.Printf].
//
// Parsing is not the only way a file can be untrustworthy: entries read here
// carry ids and Parent links this process did not generate, so they can form a
// cycle instead of a tree. readJournal does NOT reject that — it parses, and
// [FileStore.Open] applies [validateAcyclic] before building a live *Journal.
// The split is deliberate, since [ReadEntries] shares this path only to scan
// metadata linearly and never follows a Parent link.
func readJournal(path string, logf func(string, ...any)) ([]Entry, error) {
	if logf == nil {
		logf = log.Printf
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: read journal %s: %w", path, err)
	}

	lines := bytes.Split(data, []byte("\n"))
	// A well-formed file ends "...}\n", so the split's final element is
	// empty; blank trailing lines collapse the same way. Trim them so the
	// last remaining element is the real last content line.
	for len(lines) > 0 && len(bytes.TrimSpace(lines[len(lines)-1])) == 0 {
		lines = lines[:len(lines)-1]
	}

	entries := make([]Entry, 0, len(lines))
	var offset int64
	for i, line := range lines {
		lineLen := int64(len(line)) + 1 // + the newline Append always writes
		if len(bytes.TrimSpace(line)) == 0 {
			offset += lineLen
			continue
		}

		var e Entry
		if unmarshalErr := json.Unmarshal(line, &e); unmarshalErr != nil {
			if i != len(lines)-1 {
				return nil, fmt.Errorf("session: journal %s: line %d: %w: %v", path, i+1, ErrCorruptJournal, unmarshalErr)
			}
			// Torn final write: drop it and repair the file in place so the
			// next Append produces a clean last line.
			logf("session: journal %s: dropping torn tail at line %d: %v", path, i+1, unmarshalErr)
			if truncErr := os.Truncate(path, offset); truncErr != nil {
				return nil, fmt.Errorf("session: truncate torn journal %s: %w", path, truncErr)
			}
			return entries, nil
		}
		entries = append(entries, e)
		offset += lineLen
	}
	return entries, nil
}

// validateAcyclic reports whether the Parent links reachable from the last
// entry form a cycle, returning [ErrCorruptJournal] if so.
//
// It walks exactly the chain [chainFromHead] and [Journal.LastUsage] walk —
// from the last entry in append order back along Parent links, stopping at a
// compaction boundary, a root, or a dangling parent — and fails if that walk
// ever revisits an entry. Checking the walk the readers actually perform, and
// not the whole parent forest, is deliberate: an unreachable branch cannot
// wedge a reader, and rejecting a file for corruption nobody would ever
// traverse would refuse sessions that are in practice still usable.
//
// The consequence of that scoping, stated plainly: this validates the chain AS
// LOADED, not the file forever after. [Journal.Fork] accepts any id in the
// index, including one on an unreachable cyclic branch, and re-parents HEAD
// onto it — after which Fold's walk does hit the cycle and stops on
// chainFromHead's step bound instead of a stop condition. That is bounded and
// safe, but the resulting context repeats an entry. Widening this to the whole
// forest would trade that narrow case for refusing sessions whose corruption is
// permanently unreachable; the step bound is what keeps the trade safe.
//
// Duplicate ids are the common cause but not the only one: byID keeps the LAST
// index for a repeated id, which can splice a chain back on itself, and two
// entries with distinct ids naming each other as Parent cycle just as readily.
// Tracking visited INDICES rather than ids catches both — the walk is
// deterministic, so revisiting any index proves a cycle.
func validateAcyclic(entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}

	byID := make(map[string]int, len(entries))
	for i, e := range entries {
		byID[e.ID] = i
	}

	seen := make([]bool, len(entries))
	i := len(entries) - 1
	for {
		if seen[i] {
			return fmt.Errorf("%w: entry %q: Parent links form a cycle", ErrCorruptJournal, entries[i].ID)
		}
		seen[i] = true

		e := &entries[i]
		if e.Type == EntryCompaction || e.Parent == "" {
			return nil
		}
		parent, ok := byID[e.Parent]
		if !ok {
			return nil // dangling parent: the same defensive stop the walkers make
		}
		i = parent
	}
}
