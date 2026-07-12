package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
)

// ErrCorruptJournal indicates an interior journal line failed to parse —
// data loss beyond a torn-write tail, so the journal cannot be trusted.
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
