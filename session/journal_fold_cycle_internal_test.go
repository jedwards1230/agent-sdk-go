package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// refChainFromHead is the PRE-FIX chainFromHead body, verbatim and unbounded.
// It is the oracle for the equivalence tests below: the step bound is only
// allowed to change behavior on a journal this reference cannot survive, so
// every acyclic case must agree with it exactly.
//
// Deliberately self-contained — it must not share code with the production
// walk, or it would track a broken change instead of catching it. Never call
// this on a cyclic journal: it is the implementation that spins forever.
func refChainFromHead(entries []Entry) []Entry {
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
			break
		}
		cur = entries[idx]
	}
	return chain
}

func assertChainEqual(t *testing.T, got, want []Entry, label string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: chain length = %d, want %d (bounded walk must not truncate an acyclic journal)",
			label, len(got), len(want))
	}
	for i := range got {
		if got[i].ID != want[i].ID {
			t.Fatalf("%s: chain[%d].ID = %q, want %q", label, i, got[i].ID, want[i].ID)
		}
	}
}

// TestChainFromHeadEquivalenceAcyclic is the guarantee that matters for Fold:
// the step bound changes NOTHING for a well-formed journal. Fold builds the
// model's entire context, so a bound that could truncate a real chain would
// silently change what the model sees.
//
// The generated corpus supplies breadth (forks, compaction boundaries, varied
// depths). It cannot reach the boundary cases — newGenJournal always roots a
// journal with a meta entry, so it never produces a one-entry journal, a
// dangling parent, or a chain whose length equals the entry count — so those
// are hand-built alongside it. That blind spot is structural, not statistical:
// no number of extra seeds would ever cover them.
func TestChainFromHeadEquivalenceAcyclic(t *testing.T) {
	t.Run("generated corpus", func(t *testing.T) {
		var maxChain, deep int
		for seed := uint64(1); seed <= 60; seed++ {
			j := newGenJournal(t, seed, 30)
			entries := j.Entries()

			got := chainFromHead(entries)
			want := refChainFromHead(entries)
			assertChainEqual(t, got, want, "seed")
			if len(got) > maxChain {
				maxChain = len(got)
			}
			if len(got) >= 5 {
				deep++
			}
		}
		// Guard against the corpus silently degrading to trivial journals. A
		// single deep chain is not enough: if the generator drifted to mostly
		// length-1 chains the walk would rarely step and the comparison above
		// would go quietly vacuous while staying green.
		if deep < 20 {
			t.Fatalf("corpus: only %d/60 seeds produced a chain >= 5 entries (max %d), want >= 20 — "+
				"the generator has drifted to journals too shallow to exercise the walk", deep, maxChain)
		}
		t.Logf("corpus: max chain length = %d, seeds with chain >= 5 = %d/60", maxChain, deep)
	})

	// Each case below pins a stop condition the bound must not disturb.
	cases := []struct {
		name    string
		entries []Entry
	}{
		{
			// The exact case where the walk's step count EQUALS len(entries).
			// An off-by-one in the bound truncates this and nothing else.
			name: "chain spans every entry",
			entries: []Entry{
				{ID: "a", Parent: "", Type: EntryMessage},
				{ID: "b", Parent: "a", Type: EntryMessage},
				{ID: "c", Parent: "b", Type: EntryMessage},
			},
		},
		{
			name:    "sole entry is the root",
			entries: []Entry{{ID: "only", Parent: "", Type: EntryMessage}},
		},
		{
			name: "compaction boundary stops the walk",
			entries: []Entry{
				{ID: "old", Parent: "", Type: EntryMessage},
				{ID: "cut", Parent: "old", Type: EntryCompaction},
				{ID: "new", Parent: "cut", Type: EntryMessage},
			},
		},
		{
			name: "dangling parent stops the walk",
			entries: []Entry{
				{ID: "a", Parent: "", Type: EntryMessage},
				{ID: "b", Parent: "ghost", Type: EntryMessage},
			},
		},
		{
			name: "fork-abandoned branch is not in the chain",
			entries: []Entry{
				{ID: "root", Parent: "", Type: EntryMessage},
				{ID: "abandoned", Parent: "root", Type: EntryMessage},
				{ID: "kept", Parent: "root", Type: EntryMessage},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chainFromHead(tc.entries)
			want := refChainFromHead(tc.entries)
			assertChainEqual(t, got, want, tc.name)
		})
	}

	t.Run("empty journal", func(t *testing.T) {
		if got := chainFromHead(nil); got != nil {
			t.Errorf("chainFromHead(nil) = %v, want nil", got)
		}
	})
}

// TestFoldCyclicJournalTerminates is the regression test for the hang itself.
//
// The journal is a struct literal because Append and Fork cannot produce this
// shape — a cycle only ever arrives from a corrupt or hand-edited file. The
// assertion runs behind a timeout rather than as a plain call: an unbounded
// walk does not fail, it spins while appending on every iteration, so a
// regression that merely "hangs" is indistinguishable from a slow suite and
// would wedge CI until something killed it. Behind the timeout it FAILS.
func TestFoldCyclicJournalTerminates(t *testing.T) {
	// x and y name each other as Parent. Neither is a root, neither is a
	// compaction boundary, and neither parent id is dangling, so no stop
	// condition can end this walk except the step bound.
	entries := []Entry{
		{ID: "x", Parent: "y", Type: EntryMessage},
		{ID: "y", Parent: "x", Type: EntryMessage},
	}
	j := &Journal{entries: entries, byID: map[string]int{"x": 0, "y": 1}}

	done := make(chan []Entry, 1)
	go func() { done <- chainFromHead(j.entries) }()

	select {
	case chain := <-done:
		// Bounded by entry count: the walk cannot report more entries than
		// the journal holds, which is what stops the unbounded append.
		if len(chain) > len(entries) {
			t.Errorf("chain length = %d, want <= %d — the walk is revisiting cycle entries",
				len(chain), len(entries))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("chainFromHead did not terminate on a cyclic journal within 2s: the " +
			"len(entries) step bound is missing, so Journal.Fold spins forever and " +
			"allocates without bound")
	}

	// Journal.Fold is the reachable entry point, and it must terminate too.
	folded := make(chan int, 1)
	go func() { folded <- len(j.Fold()) }()

	select {
	case <-folded:
	case <-time.After(2 * time.Second):
		t.Fatal("Journal.Fold did not terminate on a cyclic journal within 2s")
	}
}

// TestValidateAcyclic pins which shapes are refused and — just as important —
// which are not. Rejecting a file makes a session unopenable, so a false
// positive here is destructive; the accept cases carry as much weight as the
// reject ones.
func TestValidateAcyclic(t *testing.T) {
	reject := []struct {
		name    string
		entries []Entry
	}{
		{
			// Distinct ids, each naming the other as parent. Duplicate ids are
			// the common cause of a cycle but not the only one, and this has none.
			name: "mutual parents",
			entries: []Entry{
				{ID: "x", Parent: "y", Type: EntryMessage},
				{ID: "y", Parent: "x", Type: EntryMessage},
			},
		},
		{
			// byID keeps the LAST index for a repeated id, which splices the
			// chain back onto itself even though each link looks locally sane.
			// An id-keyed visited set would miss this; an index-keyed one does not.
			name: "duplicate id splices the chain",
			entries: []Entry{
				{ID: "a", Parent: "", Type: EntryMessage},
				{ID: "b", Parent: "a", Type: EntryMessage},
				{ID: "a", Parent: "b", Type: EntryMessage},
			},
		},
		{
			name:    "entry is its own parent",
			entries: []Entry{{ID: "self", Parent: "self", Type: EntryMessage}},
		},
	}
	for _, tc := range reject {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			err := validateAcyclic(tc.entries)
			if !errors.Is(err, ErrCorruptJournal) {
				t.Fatalf("validateAcyclic = %v, want ErrCorruptJournal", err)
			}
		})
	}

	accept := []struct {
		name    string
		entries []Entry
	}{
		{name: "empty journal", entries: nil},
		{name: "empty non-nil journal", entries: []Entry{}},
		{
			name: "plain chain",
			entries: []Entry{
				{ID: "a", Parent: "", Type: EntryMessage},
				{ID: "b", Parent: "a", Type: EntryMessage},
			},
		},
		{
			// A dangling parent is corruption the walkers deliberately survive
			// by stopping. It must not escalate into a load failure.
			name: "dangling parent",
			entries: []Entry{
				{ID: "a", Parent: "", Type: EntryMessage},
				{ID: "b", Parent: "ghost", Type: EntryMessage},
			},
		},
		{
			// The cycle sits on a branch HEAD never walks, so it cannot wedge a
			// reader. Refusing the whole session over it would be wrong — this
			// is the deliberate scoping of the check, pinned so it stays.
			name: "cycle on an unreachable branch",
			entries: []Entry{
				{ID: "p", Parent: "q", Type: EntryMessage},
				{ID: "q", Parent: "p", Type: EntryMessage},
				{ID: "head", Parent: "", Type: EntryMessage},
			},
		},
		{
			// A compaction boundary ends the walk before it could revisit.
			name: "compaction boundary short-circuits a cycle",
			entries: []Entry{
				{ID: "a", Parent: "b", Type: EntryMessage},
				{ID: "b", Parent: "a", Type: EntryMessage},
				{ID: "c", Parent: "b", Type: EntryCompaction},
			},
		},
	}
	for _, tc := range accept {
		t.Run("accept/"+tc.name, func(t *testing.T) {
			if err := validateAcyclic(tc.entries); err != nil {
				t.Fatalf("validateAcyclic = %v, want nil — this shape is walkable "+
					"and must not cost the user their session", err)
			}
		})
	}
}

// TestFileStoreOpenRejectsCyclicJournal is the end-to-end assertion: the check
// runs where a live *Journal is built, so a cyclic file cannot become a
// foldable session. The caller gets a typed ErrCorruptJournal it can act on
// rather than a context the journal never said.
func TestFileStoreOpenRejectsCyclicJournal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	store, err := NewFileStore(WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	j, err := store.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path, id := j.Path(), j.ID()
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cyclic := `{"id":"x","parent":"y","type":"message","time":"2025-01-01T00:00:00Z"}` + "\n" +
		`{"id":"y","parent":"x","type":"message","time":"2025-01-01T00:00:01Z"}` + "\n"
	if err := os.WriteFile(path, []byte(cyclic), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// A fresh store, so the rejection cannot be masked by the journal cache.
	reopened, err := NewFileStore(WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	if _, err := reopened.Open(ctx, id); !errors.Is(err, ErrCorruptJournal) {
		t.Fatalf("Open on a cyclic journal: err = %v, want ErrCorruptJournal", err)
	}
}

// TestReadEntriesToleratesCyclicJournal guards the blast radius of the check.
// ReadEntries exists to classify a session WITHOUT resuming it — MetaOf and
// Checkpoints scan the slice linearly and never follow a Parent link — so it
// must keep working on a file whose links cycle. Escalating there would drop a
// session's cwd and checkpoints from a roster over corruption it never reads.
func TestReadEntriesToleratesCyclicJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cycle.jsonl")
	content := `{"id":"x","parent":"y","type":"message","time":"2025-01-01T00:00:00Z"}` + "\n" +
		`{"id":"y","parent":"x","type":"message","time":"2025-01-01T00:00:01Z"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	entries, err := ReadEntries(path)
	if err != nil {
		t.Fatalf("ReadEntries on a cyclic file: %v — a metadata-only scan never "+
			"follows a Parent link and must not fail on one", err)
	}
	if len(entries) != 2 {
		t.Errorf("entries = %d, want 2", len(entries))
	}
}
