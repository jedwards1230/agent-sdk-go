package session_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// entriesEqual compares two entry slices field by field, using time.Time's
// Equal (not reflect equality, which can trip over internal representation)
// for the Time field.
func entriesEqual(t *testing.T, got, want []session.Entry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, len(want) = %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.ID != w.ID || g.Parent != w.Parent || g.Type != w.Type || g.Model != w.Model {
			t.Errorf("entry %d: got %+v, want %+v", i, g, w)
		}
		if !g.Time.Equal(w.Time) {
			t.Errorf("entry %d: Time = %v, want %v", i, g.Time, w.Time)
		}
		if !bytes.Equal(g.Payload, w.Payload) {
			t.Errorf("entry %d: Payload = %s, want %s", i, g.Payload, w.Payload)
		}
		if !reflect.DeepEqual(g.Usage, w.Usage) {
			t.Errorf("entry %d: Usage = %+v, want %+v", i, g.Usage, w.Usage)
		}
	}
}

// TestJournalAppendReplayRoundTrip appends a mix of entries, closes the
// store, reopens via a fresh store, and asserts entries/HEAD/Fold all match.
func TestJournalAppendReplayRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	clock := newStepClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), time.Second)

	store1, err := session.NewFileStore(
		session.WithRoot(root),
		session.WithStoreIDGen(newCounterIDGen("e")),
		session.WithStoreClock(clock),
	)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	j, err := store1.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var appended []session.Entry
	for _, e := range []session.Entry{
		session.NewMessageEntry(provider.UserText("hello"), session.WithEntryUsage(provider.Usage{InputTokens: 3})),
		session.NewMessageEntry(provider.AssistantText("hi there"), session.WithEntryModel("m1"), session.WithEntryUsage(provider.Usage{OutputTokens: 5})),
		session.NewToolRoundEntry([]provider.ContentBlock{provider.ToolResultBlock("c1", "ok", false)}, session.WithEntryModel("m1")),
		session.NewCompactionEntry("everything so far", ""),
	} {
		got, err := j.Append(e)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		appended = append(appended, got)
	}

	wantHead := appended[len(appended)-1].ID
	if got := j.Head(); got != wantHead {
		t.Errorf("Head() = %q, want %q", got, wantHead)
	}
	wantFold := j.Fold()

	if err := store1.Close(); err != nil {
		t.Fatalf("store1.Close: %v", err)
	}

	store2, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	defer func() { _ = store2.Close() }()

	j2, err := store2.Open(ctx, j.ID())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	entriesEqual(t, j2.Entries(), appended)
	if got := j2.Head(); got != wantHead {
		t.Errorf("reopened Head() = %q, want %q", got, wantHead)
	}
	if got := j2.Fold(); !reflect.DeepEqual(got, wantFold) {
		t.Errorf("reopened Fold() = %+v, want %+v", got, wantFold)
	}
}

// TestJournalFoldLinearChain asserts a linear chain folds root→head in
// append order.
func TestJournalFoldLinearChain(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(
		session.WithRoot(t.TempDir()),
		session.WithStoreIDGen(newCounterIDGen("e")),
		session.WithStoreClock(newStepClock(time.Now(), time.Second)),
	)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	j, err := store.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, content := range []string{"one", "two", "three"} {
		if _, err := j.Append(session.NewMessageEntry(provider.UserText(content))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got := j.Fold()
	if len(got) != 3 {
		t.Fatalf("Fold() len = %d, want 3: %+v", len(got), got)
	}
	for i, want := range []string{"one", "two", "three"} {
		if got[i].Text() != want {
			t.Errorf("Fold()[%d].Text() = %q, want %q", i, got[i].Text(), want)
		}
	}
}

// TestJournalFoldSkipsMetaEntry asserts a leading [session.EntryMeta] entry
// (the root a runner.New-created journal writes first) contributes nothing to
// Fold: the folded output for a journal seeded with a meta entry is identical
// to one without it.
func TestJournalFoldSkipsMetaEntry(t *testing.T) {
	ctx := context.Background()
	newJournal := func(t *testing.T, withMeta bool) *session.Journal {
		t.Helper()
		store, err := session.NewFileStore(
			session.WithRoot(t.TempDir()),
			session.WithStoreIDGen(newCounterIDGen("e")),
			session.WithStoreClock(newStepClock(time.Now(), time.Second)),
		)
		if err != nil {
			t.Fatalf("NewFileStore: %v", err)
		}
		j, err := store.Create(ctx, "proj")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if withMeta {
			if _, err := j.Append(session.NewMetaEntry("/work/proj")); err != nil {
				t.Fatalf("Append meta: %v", err)
			}
		}
		for _, content := range []string{"one", "two", "three"} {
			if _, err := j.Append(session.NewMessageEntry(provider.UserText(content))); err != nil {
				t.Fatalf("Append: %v", err)
			}
		}
		return j
	}

	withMeta := newJournal(t, true)
	withoutMeta := newJournal(t, false)

	got, want := withMeta.Fold(), withoutMeta.Fold()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Fold() with a leading meta entry = %+v, want identical to without: %+v", got, want)
	}
	if len(got) != 3 {
		t.Fatalf("Fold() len = %d, want 3", len(got))
	}
}

// TestJournalFoldCompactionBoundary asserts a compaction entry truncates the
// fold, rendering its summary as the first (oldest) message — a user-role
// message re-entering context, per Fold's compaction rendering rule — and
// dropping everything before it.
func TestJournalFoldCompactionBoundary(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(
		session.WithRoot(t.TempDir()),
		session.WithStoreIDGen(newCounterIDGen("e")),
		session.WithStoreClock(newStepClock(time.Now(), time.Second)),
	)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	j, err := store.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := j.Append(session.NewMessageEntry(provider.UserText("old-1"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	old2, err := j.Append(session.NewMessageEntry(provider.AssistantText("old-2")))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := j.Append(session.NewCompactionEntry("everything before this", old2.ID)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := j.Append(session.NewMessageEntry(provider.UserText("new-1"))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got := j.Fold()
	if len(got) != 2 {
		t.Fatalf("Fold() len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Role != provider.RoleUser || got[0].Text() != "everything before this" {
		t.Errorf("Fold()[0] = %+v, want compaction summary first as a user-role message", got[0])
	}
	if got[1].Text() != "new-1" {
		t.Errorf("Fold()[1] = %+v, want new-1", got[1])
	}
}

// TestJournalForkBranch asserts Fork parents a new fork_point entry on an
// older entry, subsequent appends chain onto it, HEAD moves, Fold drops the
// abandoned branch while Entries retains everything.
func TestJournalForkBranch(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(
		session.WithRoot(t.TempDir()),
		session.WithStoreIDGen(newCounterIDGen("e")),
		session.WithStoreClock(newStepClock(time.Now(), time.Second)),
	)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	j, err := store.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	a, err := j.Append(session.NewMessageEntry(provider.UserText("a")))
	if err != nil {
		t.Fatalf("Append a: %v", err)
	}
	if _, err := j.Append(session.NewMessageEntry(provider.AssistantText("b"))); err != nil {
		t.Fatalf("Append b: %v", err)
	}
	if _, err := j.Append(session.NewMessageEntry(provider.UserText("c"))); err != nil {
		t.Fatalf("Append c: %v", err)
	}

	forkPoint, err := j.Fork(a.ID)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if forkPoint.Type != session.EntryForkPoint {
		t.Errorf("forkPoint.Type = %q, want %q", forkPoint.Type, session.EntryForkPoint)
	}
	if forkPoint.Parent != a.ID {
		t.Errorf("forkPoint.Parent = %q, want %q (a)", forkPoint.Parent, a.ID)
	}

	d, err := j.Append(session.NewMessageEntry(provider.AssistantText("d")))
	if err != nil {
		t.Fatalf("Append d: %v", err)
	}
	if d.Parent != forkPoint.ID {
		t.Errorf("d.Parent = %q, want %q (forkPoint)", d.Parent, forkPoint.ID)
	}
	if got := j.Head(); got != d.ID {
		t.Errorf("Head() = %q, want %q (d)", got, d.ID)
	}

	fold := j.Fold()
	if len(fold) != 2 || fold[0].Text() != "a" || fold[1].Text() != "d" {
		t.Fatalf("Fold() = %+v, want [a d] content", fold)
	}

	if got := j.Len(); got != 5 {
		t.Fatalf("Len() = %d, want 5 (a,b,c,forkPoint,d)", got)
	}
}

// TestJournalCostAggregation covers total/ByModel across a registered and an
// unregistered model priced through the real provider registry, a nil registry,
// and confirms cost counts usage on branches dropped from Fold by a fork. The
// registered model uses "claude-sonnet-5" so the expected cost can be computed
// straight from provider.Pricing.Cost, proving the aggregation matches the
// canonical pricing.
func TestJournalCostAggregation(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(
		session.WithRoot(t.TempDir()),
		session.WithStoreIDGen(newCounterIDGen("e")),
		session.WithStoreClock(newStepClock(time.Now(), time.Second)),
	)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	j, err := store.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const registered, unregistered = "claude-sonnet-5", "unregistered-model"
	a, err := j.Append(session.NewMessageEntry(provider.UserText("a"),
		session.WithEntryModel(registered), session.WithEntryUsage(provider.Usage{
			InputTokens: 1_000_000, OutputTokens: 500_000,
			CacheReadTokens: 2_000_000, CacheWriteTokens: 400_000,
		})))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := j.Append(session.NewMessageEntry(provider.AssistantText("b"),
		session.WithEntryModel(unregistered), session.WithEntryUsage(provider.Usage{InputTokens: 200, OutputTokens: 100}))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Fork away from "a" and append a new head; the dropped branch entry (b)
	// must still count toward Cost even though it's out of Fold.
	if _, err := j.Fork(a.ID); err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if _, err := j.Append(session.NewMessageEntry(provider.AssistantText("c"),
		session.WithEntryModel(registered), session.WithEntryUsage(provider.Usage{InputTokens: 10, OutputTokens: 20}))); err != nil {
		t.Fatalf("Append: %v", err)
	}

	report := j.Cost(session.RegistryPricing{})

	wantTotalUsage := provider.Usage{
		InputTokens: 1_000_000 + 200 + 10, OutputTokens: 500_000 + 100 + 20,
		CacheReadTokens: 2_000_000, CacheWriteTokens: 400_000,
	}
	if !report.Usage.Equal(wantTotalUsage) {
		t.Errorf("report.Usage = %+v, want %+v", report.Usage, wantTotalUsage)
	}

	// The registered model's summed usage, priced by the registry itself.
	sonnetUsage := provider.Usage{InputTokens: 1_000_000 + 10, OutputTokens: 500_000 + 20, CacheReadTokens: 2_000_000, CacheWriteTokens: 400_000}
	info, ok := provider.Lookup(registered)
	if !ok {
		t.Fatalf("provider.Lookup(%q) not registered — test premise broken", registered)
	}
	wantSonnetCost := info.Pricing.Cost(sonnetUsage)

	if got := report.ByModel[registered]; !got.Usage.Equal(sonnetUsage) || got.Cost != wantSonnetCost {
		t.Errorf("ByModel[%s] = %+v, want usage %+v cost %+v", registered, got, sonnetUsage, wantSonnetCost)
	}
	// Unregistered model: usage summed, cost zero.
	if got := report.ByModel[unregistered]; got.Usage.InputTokens != 200 || got.Usage.OutputTokens != 100 || got.Cost != (provider.Cost{}) {
		t.Errorf("ByModel[%s] = %+v, want usage summed and zero cost", unregistered, got)
	}
	// Total cost = only the registered model contributes.
	if report.Cost != wantSonnetCost {
		t.Errorf("report.Cost = %+v, want %+v (unregistered contributes 0)", report.Cost, wantSonnetCost)
	}

	// A custom PriceLookup is honored (injectability): double the sonnet rates.
	custom := fakePriceLookup{registered: provider.Pricing{Input: 6, Output: 30, CacheRead: 0.6, CacheWrite: 7.5}}
	if got := j.Cost(custom).ByModel[registered].Cost.USD; got <= wantSonnetCost.USD {
		t.Errorf("custom-priced USD = %v, want > registry USD %v", got, wantSonnetCost.USD)
	}

	// nil registry: tokens summed, cost zero everywhere.
	nilReport := j.Cost(nil)
	if !nilReport.Usage.Equal(wantTotalUsage) || nilReport.Cost != (provider.Cost{}) {
		t.Errorf("Cost(nil) = %+v, want usage summed with zero cost", nilReport)
	}
}

type fakePriceLookup map[string]provider.Pricing

func (f fakePriceLookup) Pricing(model string) (provider.Pricing, bool) {
	p, ok := f[model]
	return p, ok
}

// TestJournalConcurrentAppendRace exercises concurrent Append alongside
// concurrent Entries/Fold reads under -race, and checks the resulting chain
// is well-formed (no lost or misordered writes).
func TestJournalConcurrentAppendRace(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(session.WithRoot(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	j, err := store.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const writers = 6
	const perWriter = 15
	var wg sync.WaitGroup

	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := j.Append(session.NewMessageEntry(provider.UserText("x"))); err != nil {
					t.Errorf("writer %d Append %d: %v", w, i, err)
				}
			}
		}(w)
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = j.Entries()
			}
		}
	}()
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = j.Fold()
			}
		}
	}()

	wg.Wait()
	close(stop)
	readers.Wait()

	entries := j.Entries()
	if got, want := len(entries), writers*perWriter; got != want {
		t.Fatalf("Len() = %d, want %d", got, want)
	}
	if entries[0].Parent != "" {
		t.Errorf("entries[0].Parent = %q, want root (\"\")", entries[0].Parent)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Parent != entries[i-1].ID {
			t.Fatalf("entries[%d].Parent = %q, want %q (entries[%d].ID)", i, entries[i].Parent, entries[i-1].ID, i-1)
		}
	}
}

// TestJournalMetaRoundTrip asserts every content block's Meta (e.g. an
// Anthropic reasoning signature attached to a reasoning or tool_use block)
// survives verbatim through the journal: marshal to disk, a fresh
// FileStore.Open reading it back, and Fold projecting the stored entries
// back to []provider.Message. This is finding 4's regression: the old
// string-flattened MessagePayload/ToolRoundPayload structurally dropped
// Meta.
func TestJournalMetaRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	store1, err := session.NewFileStore(
		session.WithRoot(root),
		session.WithStoreIDGen(newCounterIDGen("e")),
		session.WithStoreClock(newStepClock(time.Now(), time.Second)),
	)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	j, err := store1.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	reasoning := provider.ReasoningBlock("thinking it through")
	reasoning.Meta = map[string]string{"anthropic.signature": "sig-abc123"}
	toolUse := provider.ToolUseBlock("call-1", "read_file", json.RawMessage(`{"path":"a.go"}`))
	toolUse.Meta = map[string]string{"anthropic.signature": "sig-tooluse"}
	assistantMsg := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.ContentBlock{reasoning, toolUse},
	}
	if _, err := j.Append(session.NewMessageEntry(assistantMsg)); err != nil {
		t.Fatalf("Append message: %v", err)
	}

	toolResult := provider.ToolResultBlock("call-1", "file contents", false)
	toolResult.Meta = map[string]string{"openai.item_id": "item-42"}
	if _, err := j.Append(session.NewToolRoundEntry([]provider.ContentBlock{toolResult})); err != nil {
		t.Fatalf("Append tool round: %v", err)
	}

	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("store1.Close: %v", err)
	}

	store2, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	defer func() { _ = store2.Close() }()

	reopened, err := store2.Open(ctx, j.ID())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	got := reopened.Fold()
	want := []provider.Message{
		assistantMsg,
		{Role: provider.RoleUser, Content: []provider.ContentBlock{toolResult}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Fold() after reopen = %+v, want %+v (Meta must round-trip verbatim)", got, want)
	}
}
