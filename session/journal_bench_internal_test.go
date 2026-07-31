package session

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// Benchmarks for the two journal derivations an embedder polls on a timer:
// [Journal.Cost] and [Journal.LastUsage]. Both used to copy the whole entry
// slice out from under j.mu before deriving anything, so their cost scaled with
// journal length no matter how little of it mattered.
//
// Every benchmark is swept over journal length, and every one has a
// b.RunParallel twin. The parallel twins are the ones that matter for the lock
// change: Cost now runs its O(n) summation INSIDE the critical section (it used
// to run an O(n) memcpy there instead) and LastUsage holds the lock across its
// whole walk, so contention, not just serial time, is what has to stay flat.

// benchSizes spans small (a fresh session) to large (a long-running one). Read
// the alloc/byte claims off the larger rows: at small sizes size-class rounding
// dominates and per-op alloc counts vary by platform.
var benchSizes = []int{17, 129, 513, 2049}

// benchmark sinks, to keep the compiler from eliminating the calls under test.
var (
	sinkReport CostReport
	sinkModel  string
	sinkUsage  provider.Usage
	sinkOK     bool
)

// newBenchJournal builds an in-memory journal of exactly n entries.
//
// The chain is kept full length on purpose: fork points parent onto the current
// HEAD (so they are typed fork_point entries without shortening the chain), and
// there are no compaction entries, since a compaction boundary would truncate
// the LastUsage walk and make the large sizes measure a short walk.
//
// withUsage=true gives roughly two in three entries their own usage, the shape
// a live session has — LastUsage then early-exits within a step or two of HEAD.
// withUsage=false gives the journal no usage at all, forcing the walk all the
// way to the root: the worst case, and the one that shows the walk stays
// allocation-free even when it cannot early-exit.
func newBenchJournal(b *testing.B, n int, withUsage bool) *Journal {
	b.Helper()

	var seq atomic.Int64
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	store := NewMemStore(
		WithStoreIDGen(func() string { return fmt.Sprintf("b-%08d", seq.Add(1)) }),
		WithStoreClock(func() time.Time { return base.Add(time.Duration(seq.Load()) * time.Second) }),
	)
	j, err := store.Create(context.Background(), "bench")
	if err != nil {
		b.Fatalf("MemStore.Create: %v", err)
	}

	// Three models — two registry-priced, one not — so Cost's pricing half has
	// a realistic number of buckets to walk.
	models := []string{"claude-sonnet-5", "claude-opus-4-8", "unregistered-bench-model"}

	for i := range n {
		if i > 0 && i%16 == 0 {
			if _, err := j.Fork(j.Head()); err != nil {
				b.Fatalf("Fork: %v", err)
			}
			continue
		}
		opts := []EntryOpt{WithEntryModel(models[i%len(models)])}
		if withUsage && i%3 != 0 {
			opts = append(opts, WithEntryUsage(provider.Usage{
				InputTokens: 100 + i, OutputTokens: 20 + i%7,
				CacheReadTokens: 10 * i, CacheWriteTokens: i % 13,
			}))
		}
		if _, err := j.Append(NewMessageEntry(provider.AssistantText("m"), opts...)); err != nil {
			b.Fatalf("Append: %v", err)
		}
	}
	if got := j.Len(); got != n {
		b.Fatalf("bench journal has %d entries, want %d", got, n)
	}
	return j
}

// benchMixedParallel runs read-heavy Cost/LastUsage traffic across goroutines
// with an occasional Append mixed in, which is what the embedder's per-session
// polling actually looks like: several sessions' rosters refreshing while turns
// land. It is the contention check on Cost's and LastUsage's critical sections.
func benchMixedParallel(b *testing.B, j *Journal) {
	b.Helper()
	b.ReportAllocs()

	// Appends run against a small FIXED budget rather than a fixed fraction of
	// iterations. A fraction would make a slower implementation grow the journal
	// less (fewer iterations, so fewer appends) and thereby flatter its own
	// numbers; a fixed budget leaves the journal the same length in every run,
	// which is what makes an old-vs-new comparison meaningful.
	const appendBudget = 64
	var spent atomic.Int64

	// The results are discarded rather than parked in the package-level sinks:
	// several goroutines run this body at once, and racing on a shared sink
	// would be a genuine data race under `go test -race -bench`. Cost and
	// LastUsage both take j.mu, so the compiler cannot elide the calls anyway.
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			switch {
			case i%64 == 63 && spent.Add(1) <= appendBudget:
				// Append errors are irrelevant here; a journal that could not
				// accept writes would have failed newBenchJournal already.
				_, _ = j.Append(NewMessageEntry(provider.AssistantText("live"),
					WithEntryModel("claude-sonnet-5"),
					WithEntryUsage(provider.Usage{InputTokens: 1, OutputTokens: 1})))
			case i%2 == 0:
				_ = j.Cost(RegistryPricing{})
			default:
				_, _, _ = j.LastUsage()
			}
			i++
		}
	})
}

// BenchmarkJournalCost measures the full cost aggregation over the whole
// journal — every branch — priced through the real provider registry.
func BenchmarkJournalCost(b *testing.B) {
	for _, n := range benchSizes {
		j := newBenchJournal(b, n, true)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sinkReport = j.Cost(RegistryPricing{})
			}
		})
		b.Run(fmt.Sprintf("n=%d/parallel", n), func(b *testing.B) {
			benchMixedParallel(b, newBenchJournal(b, n, true))
		})
	}
}

// BenchmarkJournalLastUsage measures the common live-session shape: HEAD, or an
// entry a step or two behind it, carries usage, so the walk early-exits.
func BenchmarkJournalLastUsage(b *testing.B) {
	for _, n := range benchSizes {
		j := newBenchJournal(b, n, true)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sinkModel, sinkUsage, sinkOK = j.LastUsage()
			}
		})
		b.Run(fmt.Sprintf("n=%d/parallel", n), func(b *testing.B) {
			benchMixedParallel(b, newBenchJournal(b, n, true))
		})
	}
}

// BenchmarkJournalLastUsageFullWalk is the worst case: no entry anywhere in the
// journal carries usage, so the walk cannot early-exit and traverses the entire
// chain to the root before reporting ok=false.
func BenchmarkJournalLastUsageFullWalk(b *testing.B) {
	for _, n := range benchSizes {
		j := newBenchJournal(b, n, false)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sinkModel, sinkUsage, sinkOK = j.LastUsage()
			}
		})
		b.Run(fmt.Sprintf("n=%d/parallel", n), func(b *testing.B) {
			benchMixedParallel(b, newBenchJournal(b, n, false))
		})
	}
}
