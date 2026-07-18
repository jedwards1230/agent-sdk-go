package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// TestStoreContract runs the shared [session.Store] contract (Create starts
// an empty journal, Append + Open resumes it within the process, List orders
// newest first, Open of an unknown id fails with ErrSessionNotFound) against
// every Store implementation, so [session.FileStore] and [session.MemStore]
// stay behaviorally identical where the design promises they are.
func TestStoreContract(t *testing.T) {
	cases := []struct {
		name     string
		newStore func(t *testing.T) session.Store
	}{
		{
			name: "FileStore",
			newStore: func(t *testing.T) session.Store {
				store, err := session.NewFileStore(
					session.WithRoot(t.TempDir()),
					session.WithStoreIDGen(newCounterIDGen("s")),
				)
				if err != nil {
					t.Fatalf("NewFileStore: %v", err)
				}
				t.Cleanup(func() { _ = store.Close() })
				return store
			},
		},
		{
			name: "MemStore",
			newStore: func(t *testing.T) session.Store {
				store := session.NewMemStore(session.WithStoreIDGen(newCounterIDGen("m")))
				t.Cleanup(func() { _ = store.Close() })
				return store
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := tc.newStore(t)

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

			if _, err := j1.Append(session.NewMessageEntry(provider.UserText("hi"))); err != nil {
				t.Fatalf("Append: %v", err)
			}
			if _, err := j1.Append(session.NewMessageEntry(provider.AssistantText("hello"))); err != nil {
				t.Fatalf("Append: %v", err)
			}

			j2, err := store.Create(ctx, "proj")
			if err != nil {
				t.Fatalf("Create (second): %v", err)
			}

			// Open resumes j1's context within the process.
			opened, err := store.Open(ctx, j1.ID())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if opened.Len() != 2 {
				t.Errorf("Open().Len() = %d, want 2", opened.Len())
			}
			if fold := opened.Fold(); len(fold) != 2 {
				t.Errorf("Open().Fold() = %d messages, want 2: %+v", len(fold), fold)
			}

			ids, err := store.List(ctx, "proj")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(ids) != 2 || ids[0] != j2.ID() || ids[1] != j1.ID() {
				t.Fatalf("List() = %v, want [%q %q] (newest first)", ids, j2.ID(), j1.ID())
			}

			if _, err := store.Open(ctx, "does-not-exist"); !errors.Is(err, session.ErrSessionNotFound) {
				t.Errorf("Open(unknown): err = %v, want ErrSessionNotFound", err)
			}
		})
	}
}

// TestStoreCreateWithID runs the shared [session.Store.CreateWithID] contract
// against every Store implementation: a non-empty id is used verbatim (and
// round-trips through Open/List), an empty id falls back to a generated one,
// entry-id generation is unaffected by pinning the session id, and an unsafe id
// is rejected with ErrInvalidID.
func TestStoreCreateWithID(t *testing.T) {
	cases := []struct {
		name     string
		idPrefix string // the counter idGen prefix this store is built with
		newStore func(t *testing.T) session.Store
	}{
		{
			name:     "FileStore",
			idPrefix: "s",
			newStore: func(t *testing.T) session.Store {
				store, err := session.NewFileStore(
					session.WithRoot(t.TempDir()),
					session.WithStoreIDGen(newCounterIDGen("s")),
				)
				if err != nil {
					t.Fatalf("NewFileStore: %v", err)
				}
				t.Cleanup(func() { _ = store.Close() })
				return store
			},
		},
		{
			name:     "MemStore",
			idPrefix: "m",
			newStore: func(t *testing.T) session.Store {
				store := session.NewMemStore(session.WithStoreIDGen(newCounterIDGen("m")))
				t.Cleanup(func() { _ = store.Close() })
				return store
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := tc.newStore(t)

			// A pinned id is used verbatim, and pinning it does NOT consume the
			// store's id generator: the first Append entry gets the generator's
			// first value, proving entry-id generation is unaffected.
			j, err := store.CreateWithID(ctx, "proj", "pinned-id")
			if err != nil {
				t.Fatalf("CreateWithID: %v", err)
			}
			if j.ID() != "pinned-id" {
				t.Errorf("ID() = %q, want pinned-id", j.ID())
			}
			ent, err := j.Append(session.NewMessageEntry(provider.UserText("hi")))
			if err != nil {
				t.Fatalf("Append: %v", err)
			}
			if want := tc.idPrefix + "-000001"; ent.ID != want {
				t.Errorf("first entry id = %q, want %q (pinning the session id must not consume the id generator)", ent.ID, want)
			}

			// The pinned session round-trips through Open and List.
			opened, err := store.Open(ctx, "pinned-id")
			if err != nil {
				t.Fatalf("Open(pinned-id): %v", err)
			}
			if opened.ID() != "pinned-id" {
				t.Errorf("Open().ID() = %q, want pinned-id", opened.ID())
			}
			ids, err := store.List(ctx, "proj")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(ids) != 1 || ids[0] != "pinned-id" {
				t.Errorf("List() = %v, want [pinned-id]", ids)
			}

			// An empty id is equivalent to Create: a fresh id is generated (the
			// generator's next value here).
			gen, err := store.CreateWithID(ctx, "proj", "")
			if err != nil {
				t.Fatalf("CreateWithID(empty): %v", err)
			}
			if want := tc.idPrefix + "-000002"; gen.ID() != want {
				t.Errorf("generated id = %q, want %q", gen.ID(), want)
			}

			// Unsafe ids (not a single path component) are rejected.
			for _, bad := range []string{"..", ".", "a/b", "/"} {
				if _, err := store.CreateWithID(ctx, "proj", bad); !errors.Is(err, session.ErrInvalidID) {
					t.Errorf("CreateWithID(%q): err = %v, want ErrInvalidID", bad, err)
				}
			}
		})
	}
}

// TestStoreRoot asserts the store-specific half of the Root() contract: a
// FileStore's root is the non-empty directory it was constructed with; a
// MemStore, persisting nothing, has none.
func TestStoreRoot(t *testing.T) {
	fileStore, err := session.NewFileStore(session.WithRoot(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = fileStore.Close() }()
	if fileStore.Root() == "" {
		t.Error("FileStore.Root() = \"\", want non-empty")
	}

	memStore := session.NewMemStore()
	defer func() { _ = memStore.Close() }()
	if got := memStore.Root(); got != "" {
		t.Errorf("MemStore.Root() = %q, want \"\"", got)
	}
}

// TestMemStoreCreateWithIDLastWriteWins asserts the documented MemStore
// collision behavior: reusing a pinned id replaces the prior in-memory journal
// (last write wins) with a fresh empty one, and Open then returns the
// replacement. (FileStore rejects the same collision — see
// TestFileStoreCreateWithIDCollision — so the two stores deliberately differ
// here; MemStore is the ephemeral, single-process store.)
func TestMemStoreCreateWithIDLastWriteWins(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemStore(session.WithStoreIDGen(newCounterIDGen("m")))
	defer func() { _ = store.Close() }()

	first, err := store.CreateWithID(ctx, "proj", "dup")
	if err != nil {
		t.Fatalf("CreateWithID (first): %v", err)
	}
	if _, err := first.Append(session.NewMessageEntry(provider.UserText("in first"))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	second, err := store.CreateWithID(ctx, "proj", "dup")
	if err != nil {
		t.Fatalf("CreateWithID (second): %v", err)
	}
	if second == first {
		t.Fatal("second CreateWithID returned the same *Journal; want a fresh replacement (last write wins)")
	}
	if second.Len() != 0 {
		t.Errorf("replacement Len() = %d, want 0 (a fresh empty journal)", second.Len())
	}

	opened, err := store.Open(ctx, "dup")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != second {
		t.Error("Open returned the prior journal; want the replacement (last write wins)")
	}
}

// TestMemStoreResumeAfterClose asserts a MemStore re-arms a Closed journal on
// Open: the runner always Closes its journal in Close, so an in-memory store
// resuming within the process must hand back an Appendable journal, not a
// permanently closed one.
func TestMemStoreResumeAfterClose(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemStore(session.WithStoreIDGen(newCounterIDGen("m")))
	defer func() { _ = store.Close() }()

	j, err := store.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := j.Append(session.NewMessageEntry(provider.UserText("first"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resumed, err := store.Open(ctx, j.ID())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if resumed != j {
		t.Fatalf("Open returned a different *Journal than Create built")
	}
	if _, err := resumed.Append(session.NewMessageEntry(provider.AssistantText("second"))); err != nil {
		t.Fatalf("Append after reopen: %v", err)
	}
	if fold := resumed.Fold(); len(fold) != 2 {
		t.Fatalf("Fold() = %d messages, want 2: %+v", len(fold), fold)
	}
}
