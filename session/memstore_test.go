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
