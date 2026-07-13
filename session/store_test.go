package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/provider"
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

// TestFileStoreLiveJournalPinnedPastTTL asserts a journal with an open write
// handle stays pinned in the cache — and remains Appendable through the
// *Journal Open returns — even once the injected clock advances past TTL
// (finding 2's regression: TTL eviction must never close a live writer's
// journal out from under it). Only after the journal is explicitly Closed
// does a subsequent Open reload a fresh *Journal from disk, with equal
// contents.
func TestFileStoreLiveJournalPinnedPastTTL(t *testing.T) {
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
	if _, err := j1.Append(session.NewMessageEntry(provider.UserText("hi"))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Advance the clock well past TTL WITHOUT closing the journal: it must
	// stay pinned live in the cache.
	now = now.Add(ttl + time.Second)

	pinned, err := store.Open(ctx, j1.ID())
	if err != nil {
		t.Fatalf("Open (past TTL, still open): %v", err)
	}
	if pinned != j1 {
		t.Fatalf("Open past TTL on a still-open journal returned a different *Journal (expected pinned)")
	}
	// The journal returned by the past-TTL Open must still be a live,
	// Appendable handle — the finding-2 regression is a closed fd here.
	if _, err := pinned.Append(session.NewMessageEntry(provider.UserText("still alive"))); err != nil {
		t.Fatalf("Append through pinned journal after past-TTL Open: %v", err)
	}

	if err := j1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reloaded, err := store.Open(ctx, j1.ID())
	if err != nil {
		t.Fatalf("Open (after Close): %v", err)
	}
	if reloaded == j1 {
		t.Errorf("Open after Close returned the same *Journal (expected a fresh reload)")
	}
	entriesEqual(t, reloaded.Entries(), j1.Entries())
}

// TestFileStoreConcurrentOpenSameID asserts N concurrent Opens of the same
// uncached, on-disk session id single-flight onto one *Journal (finding 1):
// without mutual exclusion, two Opens racing a cache miss could each build
// an independent journal/fd for the same file, and the loser's live journal
// could be closed by the cache after Open already returned it.
func TestFileStoreConcurrentOpenSameID(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	// Seed an on-disk session via one store, then close it so the id is not
	// pre-cached anywhere: every goroutine below races a genuine cache miss.
	seed, err := session.NewFileStore(session.WithRoot(root), session.WithStoreIDGen(newCounterIDGen("s")))
	if err != nil {
		t.Fatalf("NewFileStore (seed): %v", err)
	}
	j, err := seed.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := j.ID()
	if err := seed.Close(); err != nil {
		t.Fatalf("seed.Close: %v", err)
	}

	store, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const n = 16
	journals := make([]*session.Journal, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			journals[i], errs[i] = store.Open(ctx, id)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Open goroutine %d: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if journals[i] != journals[0] {
			t.Fatalf("Open goroutine %d returned a different *Journal than goroutine 0 (single-flight broken)", i)
		}
	}

	// The single shared *Journal every Open returned must still be a live,
	// Appendable handle (no ErrJournalClosed from a raced-and-lost close).
	if _, err := journals[0].Append(session.NewMessageEntry(provider.UserText("after concurrent open"))); err != nil {
		t.Fatalf("Append through the shared journal: %v", err)
	}
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
	good, err := j.Append(session.NewMessageEntry(provider.UserText("good line")))
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
	next, err := reopened.Append(session.NewMessageEntry(provider.AssistantText("after repair")))
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

// TestReadEntries asserts session.ReadEntries reads a journal file's entries
// straight off disk — including a leading meta entry — without creating a
// live append handle or touching any store's cache.
func TestReadEntries(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	store, err := session.NewFileStore(session.WithRoot(root), session.WithStoreIDGen(newCounterIDGen("s")))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	j, err := store.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := j.Append(session.NewMetaEntry("/work/proj")); err != nil {
		t.Fatalf("Append meta: %v", err)
	}
	if _, err := j.Append(session.NewMessageEntry(provider.UserText("hi"))); err != nil {
		t.Fatalf("Append message: %v", err)
	}
	path := j.Path()
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	entries, err := session.ReadEntries(path)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ReadEntries: got %d entries, want 2: %+v", len(entries), entries)
	}
	if entries[0].Type != session.EntryMeta {
		t.Fatalf("entries[0].Type = %s, want %s", entries[0].Type, session.EntryMeta)
	}
	meta, err := entries[0].Meta()
	if err != nil {
		t.Fatalf("entries[0].Meta(): %v", err)
	}
	if meta.Cwd != "/work/proj" {
		t.Errorf("entries[0].Meta().Cwd = %q, want %q", meta.Cwd, "/work/proj")
	}
	if entries[1].Type != session.EntryMessage {
		t.Errorf("entries[1].Type = %s, want %s", entries[1].Type, session.EntryMessage)
	}
}

// TestReadEntriesMissingFile asserts ReadEntries on a nonexistent path
// returns (nil, nil), matching readJournal's contract for a not-yet-created
// session.
func TestReadEntriesMissingFile(t *testing.T) {
	entries, err := session.ReadEntries(filepath.Join(t.TempDir(), "missing.jsonl"))
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if entries != nil {
		t.Errorf("ReadEntries on missing file = %+v, want nil", entries)
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
	if _, err := j.Append(session.NewMessageEntry(provider.UserText("one"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	path := j.Path()
	if err := seed.Close(); err != nil {
		t.Fatalf("seed.Close: %v", err)
	}

	// Append a corrupt interior line, then a valid trailing line, directly to
	// the file — so the corruption is interior, not the torn final line.
	trailer := session.NewMessageEntry(provider.UserText("trailing valid line"))
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

// TestNewFileStoreRequiresRoot locks in the SDK-independence contract: the
// SDK invents no directory name, so NewFileStore with no WithRoot must fail
// clearly rather than fall back to a hardcoded default.
func TestNewFileStoreRequiresRoot(t *testing.T) {
	if _, err := session.NewFileStore(); err == nil {
		t.Fatal("NewFileStore() with no root: want error, got nil")
	}
}
