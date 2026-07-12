package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestReadJournalMissingFile asserts a nonexistent path is treated as an
// empty, freshly-started journal (not an error).
func TestReadJournalMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.jsonl")
	entries, err := readJournal(path, nil)
	if err != nil {
		t.Fatalf("readJournal: %v", err)
	}
	if entries != nil {
		t.Errorf("entries = %v, want nil", entries)
	}
}

// TestReadJournalTornTailRepairsFile asserts a torn (unparseable, newline-
// less) final line is dropped, logged, and the file is physically truncated
// to the last good line.
func TestReadJournalTornTailRepairsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	goodLine := `{"id":"e1","type":"message","time":"2025-01-01T00:00:00Z","payload":{"role":"user","content":"hi"}}` + "\n"
	tornLine := `{"id":"e2","type":"message","time":"2025-01-01T00` // truncated, no trailing newline

	if err := os.WriteFile(path, []byte(goodLine+tornLine), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var logged []string
	entries, err := readJournal(path, func(format string, args ...any) {
		logged = append(logged, format)
	})
	if err != nil {
		t.Fatalf("readJournal: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "e1" {
		t.Fatalf("entries = %+v, want just e1", entries)
	}
	if len(logged) != 1 {
		t.Fatalf("logged = %v, want exactly one warning", logged)
	}

	repaired, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(repaired) != goodLine {
		t.Errorf("repaired file = %q, want %q", repaired, goodLine)
	}
}

// TestReadJournalInteriorCorruptionErrors asserts an interior line that
// fails to parse is real corruption, not a torn write: readJournal returns
// ErrCorruptJournal and does not truncate the file.
func TestReadJournalInteriorCorruptionErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	content := `{"id":"e1","type":"message","time":"2025-01-01T00:00:00Z"}` + "\n" +
		`not json at all` + "\n" +
		`{"id":"e2","type":"message","time":"2025-01-01T00:00:01Z"}` + "\n"

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := readJournal(path, func(string, ...any) {})
	if !errors.Is(err, ErrCorruptJournal) {
		t.Fatalf("readJournal: err = %v, want ErrCorruptJournal", err)
	}

	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if string(after) != content {
		t.Errorf("file was modified on interior-corruption error; got %q, want unchanged %q", after, content)
	}
}

// TestReadJournalDefaultLogger asserts a nil logf defaults to something
// non-nil (log.Printf) rather than panicking on a torn tail.
func TestReadJournalDefaultLogger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	content := `{"id":"e1","type":"message","time":"2025-01-01T00:00:00Z"}` + "\n" + `{"id":"e2"`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	entries, err := readJournal(path, nil)
	if err != nil {
		t.Fatalf("readJournal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1", entries)
	}
}
