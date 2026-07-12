package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/session"
)

// TestFileStoreCreateOpenList exercises the basic Store contract: Create
// starts an empty journal, Open resumes it by id alone (no project slug
// needed), and List returns ids newest first.
func TestFileStoreCreateOpenList(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(
		session.WithRoot(t.TempDir()),
		session.WithStoreIDGen(newCounterIDGen("s")),
	)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	j1, err := store.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if j1.Len() != 0 {
		t.Errorf("new journal Len() = %d, want 0", j1.Len())
	}
	if j1.ProjectSlug() != "proj" {
		t.Errorf("ProjectSlug() = %q, want proj", j1.ProjectSlug())
	}

	j2, err := store.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Open by id alone must find it without the caller naming the project.
	opened, err := store.Open(ctx, j1.ID())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.ID() != j1.ID() {
		t.Errorf("Open() id = %q, want %q", opened.ID(), j1.ID())
	}

	ids, err := store.List(ctx, "proj")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 2 || ids[0] != j2.ID() || ids[1] != j1.ID() {
		t.Fatalf("List() = %v, want [%q %q] (newest first)", ids, j2.ID(), j1.ID())
	}
}

// TestFileStoreOpenUnknownID asserts opening a nonexistent session id
// returns ErrSessionNotFound.
func TestFileStoreOpenUnknownID(t *testing.T) {
	store, err := session.NewFileStore(session.WithRoot(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if _, err := store.Open(context.Background(), "does-not-exist"); !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("Open: err = %v, want ErrSessionNotFound", err)
	}
}

// TestFileStorePathSafety asserts a project slug that attempts path
// traversal is rejected and never escapes the store root.
func TestFileStorePathSafety(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	for _, slug := range []string{"../escape", "../../etc", "a/b", "..", ".", ""} {
		if _, err := store.Create(context.Background(), slug); !errors.Is(err, session.ErrInvalidSlug) {
			t.Errorf("Create(%q): err = %v, want ErrInvalidSlug", slug, err)
		}
		if _, err := store.List(context.Background(), slug); !errors.Is(err, session.ErrInvalidSlug) {
			t.Errorf("List(%q): err = %v, want ErrInvalidSlug", slug, err)
		}
	}

	// Nothing should have been written outside root.
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); !os.IsNotExist(err) {
		t.Errorf("path traversal escaped root: stat err = %v", err)
	}

	// Slugify produces something safe to pass straight into Create.
	slug := session.Slugify("/home/user/../../etc/My Project!!")
	if _, err := store.Create(context.Background(), slug); err != nil {
		t.Errorf("Create(Slugify(...)) = %v, want success for slug %q", err, slug)
	}
}

// TestFileStoreOpenCachesWithinTTL asserts repeated Opens of the same id
// return the identical *Journal while hot, and a fresh *Journal once the
// injected clock advances past TTL — with equal contents across the reload.
func TestFileStoreOpenCachesWithinTTL(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	ttl := 10 * time.Minute

	store, err := session.NewFileStore(
		session.WithRoot(root),
		session.WithStoreClock(clock),
		session.WithStoreIDGen(newCounterIDGen("s")),
		session.WithTTL(ttl),
	)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	j1, err := store.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := j1.Append(session.NewMessageEntry("user", "hi")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	again, err := store.Open(ctx, j1.ID())
	if err != nil {
		t.Fatalf("Open (within TTL): %v", err)
	}
	if again != j1 {
		t.Errorf("Open within TTL returned a different *Journal (cache miss unexpected)")
	}

	now = now.Add(ttl + time.Second)

	reloaded, err := store.Open(ctx, j1.ID())
	if err != nil {
		t.Fatalf("Open (past TTL): %v", err)
	}
	if reloaded == j1 {
		t.Errorf("Open past TTL returned the same *Journal (expected reload)")
	}
	entriesEqual(t, reloaded.Entries(), j1.Entries())
}

// TestFileStoreTornWriteResume builds a journal file with a valid entry
// followed by a torn (truncated, newline-less) line, then opens it through
// the store: the valid entry survives, the torn tail is dropped and the file
// is repaired so a subsequent Append produces a clean last line. It also
// covers interior corruption returning an error.
func TestFileStoreTornWriteResume(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	seed, err := session.NewFileStore(session.WithRoot(root), session.WithStoreIDGen(newCounterIDGen("e")))
	if err != nil {
		t.Fatalf("NewFileStore (seed): %v", err)
	}
	j, err := seed.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	good, err := j.Append(session.NewMessageEntry("user", "good line"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	path := j.Path()
	if err := seed.Close(); err != nil {
		t.Fatalf("seed.Close: %v", err)
	}

	// Append a deliberately truncated JSON line with no trailing newline.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open for corruption: %v", err)
	}
	if _, err := f.WriteString(`{"id":"e-000002","parent":"e-0000`); err != nil {
		t.Fatalf("write torn tail: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var logs []string
	store, err := session.NewFileStore(
		session.WithRoot(root),
		session.WithLogger(func(format string, args ...any) { logs = append(logs, format) }),
	)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	reopened, err := store.Open(ctx, j.ID())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	entries := reopened.Entries()
	if len(entries) != 1 || entries[0].ID != good.ID {
		t.Fatalf("Entries() after torn-write resume = %+v, want just %+v", entries, good)
	}
	if len(logs) == 0 {
		t.Error("expected a warning to be logged for the dropped torn tail")
	}

	// The file must be physically repaired: the next Append produces a
	// parseable last line.
	next, err := reopened.Append(session.NewMessageEntry("assistant", "after repair"))
	if err != nil {
		t.Fatalf("Append after repair: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	verify, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore (verify): %v", err)
	}
	defer func() { _ = verify.Close() }()
	reverified, err := verify.Open(ctx, j.ID())
	if err != nil {
		t.Fatalf("Open (verify): %v", err)
	}
	got := reverified.Entries()
	if len(got) != 2 || got[0].ID != good.ID || got[1].ID != next.ID {
		t.Fatalf("Entries() after repair+append = %+v, want [good next]", got)
	}
}

// TestFileStoreTornWriteInteriorCorruption asserts an interior corrupt line
// (not the final line) is treated as real corruption, not a torn write.
func TestFileStoreTornWriteInteriorCorruption(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	seed, err := session.NewFileStore(session.WithRoot(root), session.WithStoreIDGen(newCounterIDGen("e")))
	if err != nil {
		t.Fatalf("NewFileStore (seed): %v", err)
	}
	j, err := seed.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := j.Append(session.NewMessageEntry("user", "one")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	path := j.Path()
	if err := seed.Close(); err != nil {
		t.Fatalf("seed.Close: %v", err)
	}

	// Append a corrupt interior line, then a valid trailing line, directly to
	// the file — so the corruption is interior, not the torn final line.
	trailer := session.NewMessageEntry("user", "trailing valid line")
	trailer.ID = "e-000099"
	trailerJSON, err := json.Marshal(trailer)
	if err != nil {
		t.Fatalf("marshal trailing line: %v", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open for corruption: %v", err)
	}
	if _, err := f.WriteString("not json at all\n"); err != nil {
		t.Fatalf("write corrupt interior line: %v", err)
	}
	if _, err := f.Write(append(trailerJSON, '\n')); err != nil {
		t.Fatalf("write trailing line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if _, err := store.Open(ctx, j.ID()); !errors.Is(err, session.ErrCorruptJournal) {
		t.Errorf("Open with interior corruption: err = %v, want ErrCorruptJournal", err)
	}
}
