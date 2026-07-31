package session

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// This file pins [Journal.Cost] and [Journal.LastUsage] to the behavior they
// had when both worked over a journal-sized copy of the entry slice. Both now
// read j.entries in place — Cost under a lock it releases before invoking the
// caller-supplied PriceLookup, LastUsage under a lock it holds for an
// allocation-free parent walk over j.byID — so the tests here are differential:
// generate journals (including forked and compacted ones), and assert the new
// implementations agree exactly with a reference implementation of the old
// ones.
//
// refCost is a verbatim, self-contained copy of the pre-split cost() body. It
// deliberately does NOT call sumUsage/priceUsage/addUsage/addCost, so a bug
// introduced into the production aggregation cannot hide by being shared with
// the reference.

// refCost is the pre-refactor cost() body, reimplemented independently of the
// production helpers.
func refCost(entries []Entry, reg PriceLookup) CostReport {
	add := func(a, b provider.Usage) provider.Usage {
		a.InputTokens += b.InputTokens
		a.OutputTokens += b.OutputTokens
		a.CacheReadTokens += b.CacheReadTokens
		a.CacheWriteTokens += b.CacheWriteTokens
		return a
	}

	usageByModel := make(map[string]provider.Usage)
	var totalUsage provider.Usage
	for _, e := range entries {
		if e.Usage == nil {
			continue
		}
		usageByModel[e.Model] = add(usageByModel[e.Model], *e.Usage)
		totalUsage = add(totalUsage, *e.Usage)
	}

	report := CostReport{Usage: totalUsage, ByModel: make(map[string]ModelCost, len(usageByModel))}
	for model, u := range usageByModel {
		var c provider.Cost
		var priced bool
		if reg != nil {
			if p, ok := reg.Pricing(model); ok {
				c, priced = p.Cost(u), true
			}
		}
		report.ByModel[model] = ModelCost{Model: model, Usage: u, Cost: c, Priced: priced}
		report.Cost.USD += c.USD
		report.Cost.InputUSD += c.InputUSD
		report.Cost.OutputUSD += c.OutputUSD
		report.Cost.CacheReadUSD += c.CacheReadUSD
		report.Cost.CacheWriteUSD += c.CacheWriteUSD
		if !priced {
			report.Unpriced = append(report.Unpriced, model)
		}
	}
	slices.Sort(report.Unpriced)
	return report
}

// refLastUsage is the pre-refactor LastUsage body: materialize the chain via
// [chainFromHead] (which fold still uses, unchanged) and take the first entry
// carrying usage.
func refLastUsage(entries []Entry) (string, provider.Usage, bool) {
	for _, e := range chainFromHead(entries) {
		if e.Usage != nil {
			return e.Model, *e.Usage, true
		}
	}
	return "", provider.Usage{}, false
}

// predecessorLastUsage mirrors the classic wrong walk — follow the
// append-order predecessor instead of the Parent link. It is never asserted
// against; it exists only so the differential test can prove its generated
// corpus actually contains journals where the two walks disagree, i.e. that the
// fork coverage is real rather than incidental.
func predecessorLastUsage(entries []Entry) (string, provider.Usage, bool) {
	i := len(entries) - 1
	for steps := len(entries); steps > 0; steps-- {
		e := &entries[i]
		if e.Usage != nil {
			return e.Model, *e.Usage, true
		}
		if e.Type == EntryCompaction || e.Parent == "" || i == 0 {
			break
		}
		i--
	}
	return "", provider.Usage{}, false
}

// genModels is the model set generated journals draw from. It is chosen so that
// under EVERY PriceLookup the differential test uses, at most TWO models are
// priceable:
//
//	RegistryPricing -> claude-sonnet-5 and claude-opus-4-8 only
//	genFakePricing  -> claude-sonnet-5 and "" only
//	nil             -> none
//
// That bound matters. CostReport.Cost accumulates each model's cost while
// ranging a Go map, so its float addition order is randomized per run — in the
// OLD code just as much as the new one. Unpriced models contribute an exactly
// zero Cost, and float addition of at most two non-zero addends is order
// independent, so the total is bit-reproducible and the comparison below can be
// exact instead of approximate.
var genModels = []string{"claude-sonnet-5", "claude-opus-4-8", "unpriced-model-x", ""}

// genFakePricing is a PriceLookup that prices a different two of [genModels]
// than the real registry does, so the differential sweep exercises pricing of
// the empty-model bucket and a non-registry rate table.
var genFakePricing = fakePricing{
	"claude-sonnet-5": {Input: 7, Output: 21, CacheRead: 0.7, CacheWrite: 8.75},
	"":                {Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 1.25},
}

type fakePricing map[string]provider.Pricing

func (f fakePricing) Pricing(model string) (provider.Pricing, bool) {
	p, ok := f[model]
	return p, ok
}

// newGenJournal builds an in-memory journal from a deterministic pseudo-random
// operation sequence: messages and tool rounds with and without usage, marker
// entries, compaction boundaries, and forks onto random earlier entries. The
// same seed always produces the same journal, so nothing here is flaky in CI.
func newGenJournal(t testing.TB, seed uint64, steps int) *Journal {
	t.Helper()

	rng := rand.New(rand.NewPCG(seed, 0x9E3779B97F4A7C15))
	var n atomic.Int64
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	store := NewMemStore(
		WithStoreIDGen(func() string { return fmt.Sprintf("g%d-%06d", seed, n.Add(1)) }),
		WithStoreClock(func() time.Time { return base.Add(time.Duration(n.Load()) * time.Second) }),
	)
	j, err := store.Create(context.Background(), "gen")
	if err != nil {
		t.Fatalf("MemStore.Create: %v", err)
	}

	usage := func() provider.Usage {
		return provider.Usage{
			InputTokens:      rng.IntN(5_000),
			OutputTokens:     rng.IntN(2_000),
			CacheReadTokens:  rng.IntN(3_000),
			CacheWriteTokens: rng.IntN(1_000),
		}
	}
	model := func() string { return genModels[rng.IntN(len(genModels))] }

	// A meta entry roots the journal, exactly as runner.New writes it, so ids
	// is never empty when a fork point has to be chosen.
	root, err := j.Append(NewMetaEntry("/work/gen"))
	if err != nil {
		t.Fatalf("Append meta: %v", err)
	}
	ids := []string{root.ID}

	for range steps {
		var e Entry
		switch roll := rng.IntN(100); {
		case roll < 34: // turn-bearing message
			e = NewMessageEntry(provider.AssistantText("m"), WithEntryModel(model()), WithEntryUsage(usage()))
		case roll < 55: // message with no usage of its own
			e = NewMessageEntry(provider.UserText("u"))
		case roll < 68: // tool round, usually turn-bearing
			if rng.IntN(4) > 0 {
				e = NewToolRoundEntry([]provider.ContentBlock{provider.ToolResultBlock("c", "ok", false)},
					WithEntryModel(model()), WithEntryUsage(usage()))
			} else {
				e = NewToolRoundEntry([]provider.ContentBlock{provider.ToolResultBlock("c", "ok", false)})
			}
		case roll < 76: // marker: contributes nothing to either derivation
			e = NewCheckpointEntry("mark")
		case roll < 84: // compaction boundary, sometimes carrying its own cost
			if rng.IntN(2) == 0 {
				e = NewCompactionEntry("summary", j.Head(), WithEntryModel(model()), WithEntryUsage(usage()))
			} else {
				e = NewCompactionEntry("summary", j.Head())
			}
		default: // fork onto a random earlier entry
			got, err := j.Fork(ids[rng.IntN(len(ids))])
			if err != nil {
				t.Fatalf("Fork: %v", err)
			}
			ids = append(ids, got.ID)
			continue
		}

		got, err := j.Append(e)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		ids = append(ids, got.ID)
	}
	return j
}

// assertCostReportsEqual compares two reports field by field. Cost floats are
// compared exactly; see [genModels] for why that is reproducible here.
func assertCostReportsEqual(t *testing.T, got, want CostReport, ctx string) {
	t.Helper()
	if !got.Usage.Equal(want.Usage) {
		t.Errorf("%s: Usage = %+v, want %+v", ctx, got.Usage, want.Usage)
	}
	if got.Cost != want.Cost {
		t.Errorf("%s: Cost = %+v, want %+v", ctx, got.Cost, want.Cost)
	}
	if len(got.ByModel) != len(want.ByModel) {
		t.Errorf("%s: ByModel has %d models, want %d (got %+v, want %+v)", ctx, len(got.ByModel), len(want.ByModel), got.ByModel, want.ByModel)
	}
	for model, w := range want.ByModel {
		g, ok := got.ByModel[model]
		if !ok {
			t.Errorf("%s: ByModel[%q] missing, want %+v", ctx, model, w)
			continue
		}
		if g.Model != w.Model || !g.Usage.Equal(w.Usage) || g.Cost != w.Cost || g.Priced != w.Priced {
			t.Errorf("%s: ByModel[%q] = %+v, want %+v", ctx, model, g, w)
		}
	}
	if !slices.Equal(got.Unpriced, want.Unpriced) {
		t.Errorf("%s: Unpriced = %v, want %v (order included)", ctx, got.Unpriced, want.Unpriced)
	}
	if got.Complete() != want.Complete() {
		t.Errorf("%s: Complete() = %v, want %v", ctx, got.Complete(), want.Complete())
	}
}

// assertLastUsageEqual compares a (model, usage, ok) triple.
func assertLastUsageEqual(t *testing.T, gotModel string, gotUsage provider.Usage, gotOK bool,
	wantModel string, wantUsage provider.Usage, wantOK bool, ctx string,
) {
	t.Helper()
	if gotOK != wantOK {
		t.Errorf("%s: LastUsage ok = %v, want %v", ctx, gotOK, wantOK)
		return
	}
	if gotModel != wantModel {
		t.Errorf("%s: LastUsage model = %q, want %q", ctx, gotModel, wantModel)
	}
	if !gotUsage.Equal(wantUsage) {
		t.Errorf("%s: LastUsage usage = %+v, want %+v", ctx, gotUsage, wantUsage)
	}
}

// TestJournalDerivedDifferential asserts the in-place Cost/LastUsage walks
// return exactly what the old copy-based implementations returned, over a
// deterministic corpus of generated journals covering forks, compaction
// boundaries, marker entries, entries with and without usage, several models
// including the empty one, an unpriced model, and every PriceLookup shape
// (registry, custom, nil).
//
// It also asserts the corpus is meaningful: journals containing forks and
// compactions, journals where an append-order-predecessor walk would give a
// DIFFERENT answer than the parent walk (the case a naive backward scan gets
// wrong), and both ok=true and ok=false outcomes.
func TestJournalDerivedDifferential(t *testing.T) {
	// Premise: the model set must split the way genModels documents, or the
	// exact float comparison loses its justification.
	for _, m := range []string{"claude-sonnet-5", "claude-opus-4-8"} {
		if _, ok := provider.Lookup(m); !ok {
			t.Fatalf("provider.Lookup(%q) not registered — test premise broken", m)
		}
	}
	for _, m := range []string{"unpriced-model-x", ""} {
		if _, ok := provider.Lookup(m); ok {
			t.Fatalf("provider.Lookup(%q) IS registered — test premise broken", m)
		}
	}

	lookups := []struct {
		name string
		reg  PriceLookup
	}{
		{"registry", RegistryPricing{}},
		{"custom", genFakePricing},
		{"nil", nil},
	}

	var withFork, withCompaction, predecessorDiffers, usageFound, usageMissing int

	for seed := uint64(1); seed <= 60; seed++ {
		steps := 3 + int(seed%40)
		j := newGenJournal(t, seed, steps)
		entries := j.Entries()

		for _, e := range entries {
			switch e.Type {
			case EntryForkPoint:
				withFork++
			case EntryCompaction:
				withCompaction++
			}
		}

		wantModel, wantUsage, wantOK := refLastUsage(entries)
		gotModel, gotUsage, gotOK := j.LastUsage()
		assertLastUsageEqual(t, gotModel, gotUsage, gotOK, wantModel, wantUsage, wantOK,
			fmt.Sprintf("seed %d", seed))
		if wantOK {
			usageFound++
		} else {
			usageMissing++
		}

		pModel, pUsage, pOK := predecessorLastUsage(entries)
		if pOK != wantOK || pModel != wantModel || !pUsage.Equal(wantUsage) {
			predecessorDiffers++
		}

		for _, lk := range lookups {
			assertCostReportsEqual(t, j.Cost(lk.reg), refCost(entries, lk.reg),
				fmt.Sprintf("seed %d reg=%s", seed, lk.name))
		}
	}

	t.Logf("corpus: fork_point=%d compaction=%d parent-vs-append-order divergences=%d ok=true/%d ok=false/%d",
		withFork, withCompaction, predecessorDiffers, usageFound, usageMissing)

	// Corpus-quality gates. Without these the differential assertions above
	// could all pass over journals that never exercised the interesting shapes.
	if withFork < 20 {
		t.Errorf("corpus contains only %d fork_point entries; the fork case is undertested", withFork)
	}
	if withCompaction < 20 {
		t.Errorf("corpus contains only %d compaction entries; the compaction boundary is undertested", withCompaction)
	}
	if predecessorDiffers < 5 {
		t.Errorf("only %d generated journals distinguish a parent-link walk from an append-order walk; "+
			"the generator is not producing meaningful forks, so fork coverage is vacuous", predecessorDiffers)
	}
	if usageFound < 10 || usageMissing < 3 {
		t.Errorf("corpus outcomes are lopsided: ok=true %d, ok=false %d", usageFound, usageMissing)
	}
}

// TestJournalDerivedEdgeCases pins the hand-built shapes the generated corpus
// only reaches by chance: an empty journal, a HEAD that itself carries usage,
// usage reachable only through a branch a fork abandoned, and a compaction
// boundary hiding older usage. In every case the reference implementation of
// the old behavior IS the spec.
func TestJournalDerivedEdgeCases(t *testing.T) {
	newEmpty := func(t *testing.T) *Journal {
		t.Helper()
		var n atomic.Int64
		store := NewMemStore(
			WithStoreIDGen(func() string { return fmt.Sprintf("e-%06d", n.Add(1)) }),
			WithStoreClock(func() time.Time { return time.Unix(n.Load(), 0).UTC() }),
		)
		j, err := store.Create(context.Background(), "edge")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		return j
	}

	t.Run("empty journal", func(t *testing.T) {
		j := newEmpty(t)
		model, usage, ok := j.LastUsage()
		wModel, wUsage, wOK := refLastUsage(j.Entries())
		assertLastUsageEqual(t, model, usage, ok, wModel, wUsage, wOK, "empty")
		if ok {
			t.Error("LastUsage ok = true on an empty journal, want false")
		}
		assertCostReportsEqual(t, j.Cost(RegistryPricing{}), refCost(j.Entries(), RegistryPricing{}), "empty")
	})

	t.Run("head carries usage", func(t *testing.T) {
		j := newEmpty(t)
		if _, err := j.Append(NewMessageEntry(provider.UserText("a"),
			WithEntryModel("claude-sonnet-5"), WithEntryUsage(provider.Usage{InputTokens: 7, OutputTokens: 3}))); err != nil {
			t.Fatalf("Append: %v", err)
		}
		head := provider.Usage{InputTokens: 11, OutputTokens: 5}
		if _, err := j.Append(NewMessageEntry(provider.AssistantText("b"),
			WithEntryModel("claude-opus-4-8"), WithEntryUsage(head))); err != nil {
			t.Fatalf("Append: %v", err)
		}

		model, usage, ok := j.LastUsage()
		wModel, wUsage, wOK := refLastUsage(j.Entries())
		assertLastUsageEqual(t, model, usage, ok, wModel, wUsage, wOK, "head-usage")
		if !ok || model != "claude-opus-4-8" || !usage.Equal(head) {
			t.Errorf("LastUsage = (%q, %+v, %v), want the HEAD entry's own usage %+v", model, usage, ok, head)
		}
	})

	t.Run("only usage is behind a fork", func(t *testing.T) {
		j := newEmpty(t)
		// "a" carries no usage on purpose: the only usage in the journal sits on
		// "b", which the fork abandons. A walk that followed append order would
		// wrongly report b's usage.
		a, err := j.Append(NewMessageEntry(provider.UserText("a")))
		if err != nil {
			t.Fatalf("Append a: %v", err)
		}
		abandoned := provider.Usage{InputTokens: 99, OutputTokens: 99}
		if _, err := j.Append(NewMessageEntry(provider.AssistantText("b"),
			WithEntryModel("claude-sonnet-5"), WithEntryUsage(abandoned))); err != nil {
			t.Fatalf("Append b: %v", err)
		}
		if _, err := j.Fork(a.ID); err != nil {
			t.Fatalf("Fork: %v", err)
		}
		if _, err := j.Append(NewMessageEntry(provider.UserText("c"))); err != nil {
			t.Fatalf("Append c: %v", err)
		}

		entries := j.Entries()
		model, usage, ok := j.LastUsage()
		wModel, wUsage, wOK := refLastUsage(entries)
		assertLastUsageEqual(t, model, usage, ok, wModel, wUsage, wOK, "fork-abandoned")
		if ok {
			t.Errorf("LastUsage ok = true (%q, %+v); the only usage is on an abandoned branch", model, usage)
		}
		// Cost, unlike LastUsage, still counts the abandoned branch.
		report := j.Cost(RegistryPricing{})
		assertCostReportsEqual(t, report, refCost(entries, RegistryPricing{}), "fork-abandoned")
		if !report.Usage.Equal(abandoned) {
			t.Errorf("Cost().Usage = %+v, want the abandoned branch still counted %+v", report.Usage, abandoned)
		}
	})

	t.Run("compaction boundary hides older usage", func(t *testing.T) {
		j := newEmpty(t)
		older := provider.Usage{InputTokens: 1234, OutputTokens: 77}
		if _, err := j.Append(NewMessageEntry(provider.AssistantText("old"),
			WithEntryModel("claude-sonnet-5"), WithEntryUsage(older))); err != nil {
			t.Fatalf("Append old: %v", err)
		}
		// The compaction entry carries NO usage, so a walk that failed to stop
		// at it would keep going and report the pre-compaction turn.
		if _, err := j.Append(NewCompactionEntry("summary", j.Head())); err != nil {
			t.Fatalf("Append compaction: %v", err)
		}

		entries := j.Entries()
		model, usage, ok := j.LastUsage()
		wModel, wUsage, wOK := refLastUsage(entries)
		assertLastUsageEqual(t, model, usage, ok, wModel, wUsage, wOK, "compaction")
		if ok {
			t.Errorf("LastUsage ok = true (%q, %+v); the compaction boundary must hide the older turn", model, usage)
		}
		if got := j.Cost(nil).Usage; !got.Equal(older) {
			t.Errorf("Cost(nil).Usage = %+v, want the compacted-away turn still counted %+v", got, older)
		}
	})
}

// TestCostCompositionMatchesJournalCost pins the standalone cost() helper — the
// composition of sumUsage and priceUsage kept for callers that hold a plain
// entry slice — to what Journal.Cost produces from the two halves split across
// its unlock. If the split ever drifts from the composition, this fails.
func TestCostCompositionMatchesJournalCost(t *testing.T) {
	j := newGenJournal(t, 4242, 40)
	entries := j.Entries()
	for _, reg := range []PriceLookup{RegistryPricing{}, genFakePricing, nil} {
		assertCostReportsEqual(t, cost(entries, reg), j.Cost(reg), fmt.Sprintf("cost() vs Journal.Cost (%T)", reg))
	}
}

// reentrantPricing is a PriceLookup whose Pricing method reaches back into the
// journal being priced. It is legal caller code: PriceLookup is a public seam
// and the SDK documents no restriction on what an implementation may do.
type reentrantPricing struct {
	j     *Journal
	calls atomic.Int64
}

func (r *reentrantPricing) Pricing(model string) (provider.Pricing, bool) {
	r.calls.Add(1)
	// Every one of these takes j.mu. If Journal.Cost held the lock across this
	// call, all three would block forever.
	_ = r.j.Len()
	_ = r.j.Head()
	_ = r.j.Cost(nil) // nil lookup: terminates, no further re-entry
	return provider.Pricing{Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 1.25}, true
}

// TestJournalCostPriceLookupReentrancy is the executable form of the deadlock
// argument behind Journal.Cost's lock split: reg is arbitrary caller-supplied
// code, so it must never be invoked while j.mu is held. A PriceLookup that
// calls Len/Head/Cost on the very journal being priced must complete, not hang.
//
// The timeout is the point of the test. A regression that re-inlines the
// pricing loop under the lock deadlocks rather than misbehaves, so without the
// timeout this would wedge CI instead of failing it.
func TestJournalCostPriceLookupReentrancy(t *testing.T) {
	j := newGenJournal(t, 77, 30)
	if j.Len() == 0 {
		t.Fatal("generated journal is empty — the lookup would never be invoked")
	}

	reg := &reentrantPricing{j: j}
	done := make(chan CostReport, 1)
	go func() { done <- j.Cost(reg) }()

	select {
	case report := <-done:
		if reg.calls.Load() == 0 {
			t.Fatal("PriceLookup was never invoked; the test proves nothing about re-entrancy")
		}
		if len(report.ByModel) == 0 {
			t.Errorf("Cost returned no per-model breakdown: %+v", report)
		}
		if !report.Complete() {
			t.Errorf("Cost().Unpriced = %v, want empty — the re-entrant lookup prices every model", report.Unpriced)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Journal.Cost deadlocked: a re-entrant PriceLookup blocked for 10s, " +
			"which means reg is being invoked while j.mu is held")
	}
}

// TestJournalDerivedConcurrent hammers Cost and LastUsage against concurrent
// Append and Fork so `go test -race` has something to catch, and checks the
// readers always observe a self-consistent journal rather than a torn one.
func TestJournalDerivedConcurrent(t *testing.T) {
	var n atomic.Int64
	store := NewMemStore(
		WithStoreIDGen(func() string { return fmt.Sprintf("c-%06d", n.Add(1)) }),
		WithStoreClock(func() time.Time { return time.Unix(n.Load(), 0).UTC() }),
	)
	j, err := store.Create(context.Background(), "conc")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	root, err := j.Append(NewMetaEntry("/work/conc"))
	if err != nil {
		t.Fatalf("Append meta: %v", err)
	}

	const writers, perWriter, readers = 4, 40, 6
	stop := make(chan struct{})

	var forkPoints sync.Map // id -> struct{}, ids safe to fork onto
	forkPoints.Store(root.ID, struct{}{})

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := range writers {
		go func(w int) {
			defer wg.Done()
			for i := range perWriter {
				var e Entry
				switch i % 4 {
				case 0:
					e = NewMessageEntry(provider.AssistantText("m"),
						WithEntryModel("claude-sonnet-5"),
						WithEntryUsage(provider.Usage{InputTokens: 10 + i, OutputTokens: w + 1}))
				case 1:
					e = NewMessageEntry(provider.UserText("u"))
				case 2:
					e = NewCompactionEntry("summary", j.Head())
				default:
					// Fork onto a known-existing entry, exercising the branch
					// shape LastUsage's parent walk has to respect.
					if _, err := j.Fork(root.ID); err != nil {
						t.Errorf("writer %d Fork: %v", w, err)
					}
					continue
				}
				got, err := j.Append(e)
				if err != nil {
					t.Errorf("writer %d Append %d: %v", w, i, err)
					return
				}
				forkPoints.Store(got.ID, struct{}{})
			}
		}(w)
	}

	var rwg sync.WaitGroup
	rwg.Add(readers)
	for r := range readers {
		go func(r int) {
			defer rwg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				switch r % 3 {
				case 0:
					report := j.Cost(RegistryPricing{})
					if report.Usage.InputTokens < 0 || len(report.Unpriced) > 1 {
						t.Errorf("reader %d: implausible report %+v", r, report)
						return
					}
				case 1:
					if _, u, ok := j.LastUsage(); ok && u.InputTokens < 10 {
						t.Errorf("reader %d: LastUsage returned %+v, want a usage this test ever wrote", r, u)
						return
					}
				default:
					_ = j.Cost(nil)
					_, _, _ = j.LastUsage()
				}
			}
		}(r)
	}

	wg.Wait()
	close(stop)
	rwg.Wait()

	// The journal is quiescent now, so the derived values must agree exactly
	// with the reference implementations.
	assertDerivedMatchesReference(t, j, "post-concurrency")

	// The concurrently-built journal's own tail shape is whatever the writers
	// happened to interleave, which is not enough to pin the walk's stop rules:
	// with usage-bearing entries that dense, the walk early-exits before it ever
	// reaches a fork point or a compaction. So drive the journal deliberately
	// into each boundary shape on top of what the writers built, and assert the
	// concurrency-grown j.byID index still steers the walk correctly.
	mustAppend := func(e Entry) Entry {
		t.Helper()
		got, err := j.Append(e)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		return got
	}

	// Fork boundary: a usage-bearing entry, then a fork back to the root and a
	// usage-free entry on the new branch. The usage is now the APPEND-ORDER
	// predecessor of the branch but not an ancestor of it, so a walk that
	// followed append order would wrongly report it.
	mustAppend(NewMessageEntry(provider.AssistantText("pre-fork"),
		WithEntryModel("claude-sonnet-5"), WithEntryUsage(provider.Usage{InputTokens: 4242, OutputTokens: 17})))
	if _, err := j.Fork(root.ID); err != nil {
		t.Fatalf("Fork: %v", err)
	}
	mustAppend(NewMessageEntry(provider.UserText("post-fork")))
	assertDerivedMatchesReference(t, j, "post-concurrency fork boundary")
	if _, _, ok := j.LastUsage(); ok {
		t.Error("LastUsage ok = true on a branch forked back to the usage-free root")
	}

	// Compaction boundary: a usage-bearing entry, then a compaction carrying no
	// usage of its own. The compaction must hide the entry directly beneath it.
	mustAppend(NewMessageEntry(provider.AssistantText("pre-compaction"),
		WithEntryModel("claude-opus-4-8"), WithEntryUsage(provider.Usage{InputTokens: 909, OutputTokens: 11})))
	mustAppend(NewCompactionEntry("summary", j.Head()))
	assertDerivedMatchesReference(t, j, "post-concurrency compaction boundary")
	if _, _, ok := j.LastUsage(); ok {
		t.Error("LastUsage ok = true past a compaction boundary that carries no usage itself")
	}
}

// assertDerivedMatchesReference asserts a quiescent journal's Cost and
// LastUsage equal the reference implementations of the pre-refactor behavior.
func assertDerivedMatchesReference(t *testing.T, j *Journal, ctx string) {
	t.Helper()
	entries := j.Entries()
	wModel, wUsage, wOK := refLastUsage(entries)
	gModel, gUsage, gOK := j.LastUsage()
	assertLastUsageEqual(t, gModel, gUsage, gOK, wModel, wUsage, wOK, ctx)
	assertCostReportsEqual(t, j.Cost(RegistryPricing{}), refCost(entries, RegistryPricing{}), ctx)
}
