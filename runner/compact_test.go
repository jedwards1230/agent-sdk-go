package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/runner"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// textEvents scripts one plain (no tool call) completion, billing usage — the
// shape both an ordinary Prompt turn and the default summarizer's own call
// take against a [scriptedProvider].
func textEvents(text string, usage provider.Usage) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.StreamTextDelta, Text: text},
		{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: usage},
	}
}

// capturingProvider wraps a provider.Provider and records the LAST Request
// passed to Stream, so a test can assert on exactly what a call sent (e.g.
// which system prompt the compaction summarizer used).
type capturingProvider struct {
	provider.Provider
	lastReq provider.Request
}

func (p *capturingProvider) Stream(ctx context.Context, req provider.Request) (provider.StreamHandle, error) {
	p.lastReq = req
	return p.Provider.Stream(ctx, req)
}

// stubSummarizer is a deterministic runner.Summarizer test double: it makes no
// provider call, proving Options.Summarizer replaces the strategy wholesale
// rather than merely adding to it.
type stubSummarizer struct {
	calls int
	req   runner.SummarizeRequest
	want  runner.SummarizeResult
	err   error
}

func (s *stubSummarizer) Summarize(_ context.Context, req runner.SummarizeRequest) (runner.SummarizeResult, error) {
	s.calls++
	s.req = req
	return s.want, s.err
}

// summarizerFunc adapts a plain function to runner.Summarizer, for one-off
// strategies that need to do something to the context (cancel it) rather than
// return a canned result.
type summarizerFunc func(context.Context, runner.SummarizeRequest) (runner.SummarizeResult, error)

func (f summarizerFunc) Summarize(ctx context.Context, req runner.SummarizeRequest) (runner.SummarizeResult, error) {
	return f(ctx, req)
}

// drainingSummarizer is a runner.Summarizer that non-blockingly drains an event
// subscription from INSIDE Summarize and records everything it found, then
// returns want/err. It is what makes the ordering assertion sharp: whatever it
// read there was necessarily published before the summarizer ran, which no
// post-hoc read of the stream can establish (the broker assigns seq under a
// lock and delivers synchronously, so publish order is read order either way —
// but only reading from inside the call proves the publish HAPPENED first).
type drainingSummarizer struct {
	sub   *event.Subscription
	seen  []event.Event
	calls int
	want  runner.SummarizeResult
	err   error
}

func (s *drainingSummarizer) Summarize(_ context.Context, _ runner.SummarizeRequest) (runner.SummarizeResult, error) {
	s.calls++
	s.seen = append(s.seen, drainEvents(s.sub)...)
	return s.want, s.err
}

// drainEvents reads every event already delivered to sub, in publish order,
// without blocking. event.Broker.Publish delivers synchronously under its own
// lock, so once a publishing call has returned, everything it published is
// already in the subscription's buffer.
func drainEvents(sub *event.Subscription) []event.Event {
	var got []event.Event
	for {
		select {
		case ev := <-sub.C:
			got = append(got, ev)
		default:
			return got
		}
	}
}

// kindsOf lists the kinds of evs in order.
func kindsOf(evs []event.Event) []string {
	kinds := make([]string, 0, len(evs))
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind())
	}
	return kinds
}

// withoutCompactionProgress drops the two kinds this change added, leaving
// exactly what an embedder that consumes neither of them observes.
func withoutCompactionProgress(evs []event.Event) []event.Event {
	kept := make([]event.Event, 0, len(evs))
	for _, ev := range evs {
		switch ev.Kind() {
		case event.KindSessionCompactionStarted, event.KindSessionCompactionFailed:
			continue
		}
		kept = append(kept, ev)
	}
	return kept
}

// TestRunnerCompact drives two ordinary turns, compacts, and asserts: Fold
// afterward sees exactly the summary; the journal grew (never shrank) by one
// compaction entry carrying the summarizer's own model/usage; and the
// published session.compacted event carries the same boundary, count, model,
// usage, and summary text a renderer needs.
func TestRunnerCompact(t *testing.T) {
	ctx := context.Background()
	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		textEvents("one", provider.Usage{InputTokens: 1, OutputTokens: 1}),
		textEvents("two", provider.Usage{InputTokens: 1, OutputTokens: 1}),
		textEvents("condensed", provider.Usage{InputTokens: 40, OutputTokens: 8}),
	}}
	r := newCheckpointRunner(t, prov)

	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := r.Prompt(ctx, "second"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := len(r.Fold()); got != 4 {
		t.Fatalf("precondition: Fold() = %d messages, want 4", got)
	}
	before := entryIDs(readEntries(t, r))

	sub := r.EventsLive()
	defer sub.Close()

	if err := r.Compact(ctx, ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Fold now sees exactly the compaction summary.
	got := r.Fold()
	if len(got) != 1 {
		t.Fatalf("Fold() after Compact = %d messages, want 1: %+v", len(got), got)
	}
	if want := "condensed"; msgText(got[0]) != want {
		t.Errorf("Fold()[0] text = %q, want %q", msgText(got[0]), want)
	}

	// Additive: every prior entry is still on disk, plus exactly one more.
	after := entryIDs(readEntries(t, r))
	if len(after) != len(before)+1 {
		t.Fatalf("journal has %d entries after Compact, want %d (additive: every prior entry plus one compaction)", len(after), len(before)+1)
	}
	for i, id := range before {
		if after[i] != id {
			t.Errorf("entry %d id = %q after Compact, want %q — compaction must never remove or reorder entries", i, after[i], id)
		}
	}
	last := readEntries(t, r)[len(after)-1]
	if last.Type != session.EntryCompaction {
		t.Fatalf("last entry type = %q, want %q", last.Type, session.EntryCompaction)
	}
	payload, err := last.Compaction()
	if err != nil {
		t.Fatalf("Compaction(): %v", err)
	}
	if payload.Summary != "condensed" {
		t.Errorf("payload.Summary = %q, want condensed", payload.Summary)
	}
	if payload.ReplacesThrough != before[len(before)-1] {
		t.Errorf("payload.ReplacesThrough = %q, want the pre-compaction HEAD %q", payload.ReplacesThrough, before[len(before)-1])
	}
	if last.Model != testModel {
		t.Errorf("last.Model = %q, want %q", last.Model, testModel)
	}
	if last.Usage == nil || last.Usage.InputTokens != 40 || last.Usage.OutputTokens != 8 {
		t.Errorf("last.Usage = %+v, want the summarizer's usage {40 8}", last.Usage)
	}

	// The abandoned entries still count toward Cost — compaction does not
	// reclaim spend, same as Fork/Rewind.
	cost := r.Cost()
	if got, want := cost.Usage.InputTokens, 1+1+40; got != want {
		t.Errorf("Cost().Usage.InputTokens = %d, want %d (both turns plus the compaction call)", got, want)
	}

	// The compaction brackets itself: the start comes first, the terminal
	// second (see TestRunnerCompactPublishesStartBeforeSummarize for the
	// ordering proof).
	if _, ok := (<-sub.C).(event.SessionCompactionStarted); !ok {
		t.Fatalf("first event is not session.compaction_started")
	}
	ev, ok := (<-sub.C).(event.SessionCompacted)
	if !ok {
		t.Fatalf("second event is not session.compacted")
	}
	if ev.SessionID() != r.ID() {
		t.Errorf("session_id = %q, want %q", ev.SessionID(), r.ID())
	}
	if ev.ReplacesThrough != payload.ReplacesThrough {
		t.Errorf("ev.ReplacesThrough = %q, want %q", ev.ReplacesThrough, payload.ReplacesThrough)
	}
	if ev.MessagesCompacted != 4 {
		t.Errorf("ev.MessagesCompacted = %d, want 4", ev.MessagesCompacted)
	}
	if ev.Model != testModel {
		t.Errorf("ev.Model = %q, want %q", ev.Model, testModel)
	}
	if ev.Usage.InputTokens != 40 || ev.Usage.OutputTokens != 8 {
		t.Errorf("ev.Usage = %+v, want {40 8 ...}", ev.Usage)
	}
	if ev.Summary != "condensed" {
		t.Errorf("ev.Summary = %q, want condensed", ev.Summary)
	}
	if ev.Tier() != event.TierMustDeliver {
		t.Errorf("Tier() = %v, want must-deliver", ev.Tier())
	}
}

// TestRunnerCompactDefaultInstructions asserts Compact("") sends
// DefaultCompactionInstructions as the summarizer's system prompt, and that a
// caller-supplied instructions string overrides it — proving the prompt is
// data flowing through the seam, not a value baked into the call.
func TestRunnerCompactDefaultInstructions(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name         string
		instructions string
		want         string
	}{
		{name: "empty uses the default", instructions: "", want: runner.DefaultCompactionInstructions},
		{name: "custom instructions override", instructions: "focus only on the file list", want: "focus only on the file list"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &scriptedProvider{events: [][]provider.StreamEvent{
				textEvents("one", provider.Usage{InputTokens: 1, OutputTokens: 1}),
				textEvents("condensed", provider.Usage{InputTokens: 5, OutputTokens: 2}),
			}}
			capProv := &capturingProvider{Provider: inner}
			r := newCheckpointRunner(t, capProv)

			if err := r.Prompt(ctx, "first"); err != nil {
				t.Fatalf("Prompt: %v", err)
			}
			if err := r.Compact(ctx, tc.instructions); err != nil {
				t.Fatalf("Compact: %v", err)
			}
			if got := capProv.lastReq.System; got != tc.want {
				t.Errorf("summarizer system prompt = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRunnerCompactCustomSummarizer asserts Options.Summarizer replaces the
// ENTIRE strategy: the runner's own provider is never called for the
// compaction, and the stub's result — including a model/usage the runner
// never chose itself — lands verbatim in the journal and the event.
func TestRunnerCompactCustomSummarizer(t *testing.T) {
	ctx := context.Background()
	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		textEvents("one", provider.Usage{InputTokens: 1, OutputTokens: 1}),
	}}
	stub := &stubSummarizer{want: runner.SummarizeResult{
		Summary: "stubbed summary",
		Model:   "cheap-model",
		Usage:   provider.Usage{InputTokens: 3, OutputTokens: 1},
	}}
	r := newCheckpointRunner(t, prov, func(o *runner.Options) { o.Summarizer = stub })

	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := r.Compact(ctx, "custom instructions"); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if stub.calls != 1 {
		t.Fatalf("stub summarizer called %d times, want 1", stub.calls)
	}
	if prov.calls != 1 {
		t.Errorf("runner's own provider called %d times, want 1 (only the Prompt turn — the custom summarizer must replace the default's call entirely)", prov.calls)
	}
	if stub.req.Instructions != "custom instructions" {
		t.Errorf("stub received instructions %q, want %q", stub.req.Instructions, "custom instructions")
	}
	if len(stub.req.Messages) != 2 {
		t.Errorf("stub received %d messages, want 2 (the folded user+assistant turn)", len(stub.req.Messages))
	}

	entries := readEntries(t, r)
	last := entries[len(entries)-1]
	if last.Model != "cheap-model" {
		t.Errorf("last.Model = %q, want cheap-model", last.Model)
	}
	if last.Usage == nil || !last.Usage.Equal(stub.want.Usage) {
		t.Errorf("last.Usage = %+v, want %+v", last.Usage, stub.want.Usage)
	}
	if got := msgText(r.Fold()[0]); got != "stubbed summary" {
		t.Errorf("Fold()[0] = %q, want stubbed summary", got)
	}
}

// TestRunnerCompactNothingToCompact asserts a fresh session (nothing folded
// yet) refuses to compact with ErrNothingToCompact, and journals and
// publishes nothing.
func TestRunnerCompactNothingToCompact(t *testing.T) {
	ctx := context.Background()
	r := newCheckpointRunner(t, &scriptedProvider{})

	before := entryIDs(readEntries(t, r))
	sub := r.EventsLive()
	defer sub.Close()

	if err := r.Compact(ctx, ""); !errors.Is(err, runner.ErrNothingToCompact) {
		t.Fatalf("Compact() on an empty session err = %v, want ErrNothingToCompact", err)
	}

	if got := entryIDs(readEntries(t, r)); len(got) != len(before) {
		t.Errorf("journal has %d entries after a refused compact, want %d unchanged", len(got), len(before))
	}

	// Nothing was published: drive a sentinel and confirm it is first to
	// arrive — a session.compacted would be ahead of it.
	r.Emit(event.NewSessionKilled(r.ID()))
	select {
	case ev := <-sub.C:
		if _, ok := ev.(event.SessionCompacted); ok {
			t.Errorf("a refused compact published %T", ev)
		}
	default:
		t.Error("expected the sentinel event on the stream")
	}
}

// TestRunnerCompactSummarizerError asserts a Summarizer failure aborts
// Compact before anything is journaled or published.
func TestRunnerCompactSummarizerError(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("summarizer unavailable")
	stub := &stubSummarizer{err: wantErr}
	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		textEvents("one", provider.Usage{InputTokens: 1, OutputTokens: 1}),
	}}
	r := newCheckpointRunner(t, prov, func(o *runner.Options) { o.Summarizer = stub })

	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	before := entryIDs(readEntries(t, r))

	if err := r.Compact(ctx, ""); !errors.Is(err, wantErr) {
		t.Fatalf("Compact() err = %v, want it to wrap %v", err, wantErr)
	}
	if got := entryIDs(readEntries(t, r)); len(got) != len(before) {
		t.Errorf("journal has %d entries after a failed summarize, want %d unchanged", len(got), len(before))
	}
}

// TestRunnerCompactResume asserts a compacted session resumes correctly: the
// resumed runner's Fold matches the pre-close fold exactly (the summary, not
// the original transcript), and a further Prompt continues from that
// compacted context rather than the original one.
func TestRunnerCompactResume(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		textEvents("one", provider.Usage{InputTokens: 1, OutputTokens: 1}),
		textEvents("two", provider.Usage{InputTokens: 1, OutputTokens: 1}),
		textEvents("condensed", provider.Usage{InputTokens: 40, OutputTokens: 8}),
	}}
	r, err := runner.New(ctx, runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov, Tools: oneToolRegistry{}, IDGen: seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := r.Prompt(ctx, "second"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := r.Compact(ctx, ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	wantFold := foldText(r.Fold())
	id := r.ID()
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	prov2 := &scriptedProvider{events: [][]provider.StreamEvent{
		textEvents("continues", provider.Usage{InputTokens: 1, OutputTokens: 1}),
	}}
	r2, err := runner.Resume(ctx, id, runner.Options{
		Root: root, Cwd: cwd, Model: testModel,
		Provider: prov2, Tools: oneToolRegistry{},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer func() { _ = r2.Close() }()

	if got := foldText(r2.Fold()); got != wantFold {
		t.Errorf("resumed Fold() = %q, want the pre-close compacted fold %q", got, wantFold)
	}
	if got := len(r2.Fold()); got != 1 {
		t.Fatalf("resumed Fold() = %d messages, want 1 (the compaction summary — never the original 4-message transcript)", got)
	}

	// A further turn chains onto the compacted context, not the original one.
	if err := r2.Prompt(ctx, "third"); err != nil {
		t.Fatalf("Prompt after resume: %v", err)
	}
	if got, want := foldText(r2.Fold()), "user:condensed\nuser:third\nassistant:continues"; got != want {
		t.Errorf("Fold() after resumed Prompt = %q, want %q", got, want)
	}
}

// TestRunnerLastUsage asserts LastUsage reports nothing before any turn has
// run, reports the most recent turn's model/usage once one has, and — after
// Compact — reports the compaction call's own footprint instead of reaching
// past the new boundary into the original transcript.
func TestRunnerLastUsage(t *testing.T) {
	ctx := context.Background()
	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		textEvents("one", provider.Usage{InputTokens: 11, OutputTokens: 3}),
		textEvents("condensed", provider.Usage{InputTokens: 40, OutputTokens: 8}),
	}}
	r := newCheckpointRunner(t, prov)

	if _, _, ok := r.LastUsage(); ok {
		t.Error("LastUsage() ok = true before any turn, want false")
	}

	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	model, usage, ok := r.LastUsage()
	if !ok {
		t.Fatal("LastUsage() ok = false after a turn, want true")
	}
	if model != testModel {
		t.Errorf("model = %q, want %q", model, testModel)
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 3 {
		t.Errorf("usage = %+v, want {11 3 ...}", usage)
	}

	if err := r.Compact(ctx, ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	model, usage, ok = r.LastUsage()
	if !ok {
		t.Fatal("LastUsage() ok = false after Compact, want true (the compaction entry's own usage)")
	}
	if model != testModel {
		t.Errorf("model after Compact = %q, want %q", model, testModel)
	}
	if usage.InputTokens != 40 || usage.OutputTokens != 8 {
		t.Errorf("usage after Compact = %+v, want the summarizer's {40 8 ...}, not the original turn's", usage)
	}
}

// Publish takes no context, so a compaction that fails BECAUSE its context was
// cancelled can still deliver its terminal event. Asserted at compile time
// rather than in prose, since it is the premise
// TestRunnerCompactCancelledMidSummarizePublishesFailed depends on.
var _ func(*event.Broker, event.Event) = (*event.Broker).Publish

// headEntryID returns the runner's current journal HEAD as read off disk — the
// value Compact fixes as the compaction boundary.
func headEntryID(t *testing.T, r *runner.Runner) string {
	t.Helper()
	ids := entryIDs(readEntries(t, r))
	if len(ids) == 0 {
		t.Fatal("journal is empty; expected at least one entry")
	}
	return ids[len(ids)-1]
}

// TestCompactionEntryWireDiscriminator verifies, on the real wire, that a
// compaction entry's JSON line carries "type":"compaction" — the discriminator
// session.NewCompactionEntry sets and the substring
// TestRunnerCompactAppendFailurePublishesFailed injects a write fault on. A
// matcher that matched nothing would make that test pass vacuously, so the
// substring is checked against actual journal bytes here rather than assumed.
func TestCompactionEntryWireDiscriminator(t *testing.T) {
	const discriminator = `"type":"compaction"`

	ctx := context.Background()
	r := newCheckpointRunner(t, textTurns("one", "condensed"))
	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := r.Compact(ctx, ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	raw, err := os.ReadFile(r.JournalPath())
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	var matched []int
	for i, line := range lines {
		if strings.Contains(line, discriminator) {
			matched = append(matched, i)
		}
	}
	if len(matched) != 1 {
		t.Fatalf("%s matched %d of %d journal lines, want exactly 1 (the compaction entry):\n%s",
			discriminator, len(matched), len(lines), raw)
	}
	if got, want := matched[0], len(lines)-1; got != want {
		t.Errorf("%s matched line %d, want the last line %d — the fault injection must target the compaction append, not an earlier entry", discriminator, got, want)
	}
}

// TestRunnerCompactPublishesStartBeforeSummarize asserts the ORDER, not merely
// the presence, of the compaction start: the summarizer drains the event stream
// from inside its own Summarize call and must find the start already published
// there — and must NOT find any terminal event, which cannot exist yet. The
// terminal (session.compacted) arrives only after Summarize returns.
//
// It also pins the start's payload: the boundary is the pre-compaction HEAD
// (identical to what the terminal reports, which is what makes start→terminal
// correlation unambiguous) and the count is the pre-compaction folded size.
func TestRunnerCompactPublishesStartBeforeSummarize(t *testing.T) {
	ctx := context.Background()
	ds := &drainingSummarizer{want: runner.SummarizeResult{
		Summary: "stubbed summary",
		Model:   "cheap-model",
		Usage:   provider.Usage{InputTokens: 3, OutputTokens: 1},
	}}
	r := newCheckpointRunner(t, textTurns("one"), func(o *runner.Options) { o.Summarizer = ds })

	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	wantBoundary := headEntryID(t, r)
	wantMessages := len(r.Fold())
	if wantMessages != 2 {
		t.Fatalf("precondition: Fold() = %d messages, want 2", wantMessages)
	}

	sub := r.EventsLive()
	defer sub.Close()
	ds.sub = sub

	if err := r.Compact(ctx, ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if ds.calls != 1 {
		t.Fatalf("summarizer called %d times, want 1", ds.calls)
	}

	// What the summarizer saw BEFORE it ran: the start, and nothing else.
	if got, want := kindsOf(ds.seen), []string{event.KindSessionCompactionStarted}; !slices.Equal(got, want) {
		t.Fatalf("events visible from inside Summarize = %v, want %v — the start must be published before the summarizer call, and no terminal can exist yet", got, want)
	}
	started, ok := ds.seen[0].(event.SessionCompactionStarted)
	if !ok {
		t.Fatalf("event visible from inside Summarize is %T, want event.SessionCompactionStarted", ds.seen[0])
	}
	if started.SessionID() != r.ID() {
		t.Errorf("started.SessionID() = %q, want %q", started.SessionID(), r.ID())
	}
	if started.ReplacesThrough != wantBoundary {
		t.Errorf("started.ReplacesThrough = %q, want the pre-compaction HEAD %q", started.ReplacesThrough, wantBoundary)
	}
	if started.Messages != wantMessages {
		t.Errorf("started.Messages = %d, want %d", started.Messages, wantMessages)
	}
	if started.Tier() != event.TierMustDeliver {
		t.Errorf("started.Tier() = %v, want must-deliver", started.Tier())
	}

	// And after Summarize returned: exactly one terminal, carrying the same
	// boundary the start did.
	after := drainEvents(sub)
	if got, want := kindsOf(after), []string{event.KindSessionCompacted}; !slices.Equal(got, want) {
		t.Fatalf("events after Summarize returned = %v, want %v", got, want)
	}
	compacted, ok := after[0].(event.SessionCompacted)
	if !ok {
		t.Fatalf("terminal is %T, want event.SessionCompacted", after[0])
	}
	if compacted.ReplacesThrough != started.ReplacesThrough {
		t.Errorf("terminal ReplacesThrough = %q, want the start's %q — the boundary is the correlator", compacted.ReplacesThrough, started.ReplacesThrough)
	}
	if compacted.MessagesCompacted != started.Messages {
		t.Errorf("terminal MessagesCompacted = %d, want the start's Messages %d", compacted.MessagesCompacted, started.Messages)
	}
	if compacted.Seq() <= started.Seq() {
		t.Errorf("terminal seq = %d, start seq = %d; want the terminal strictly after", compacted.Seq(), started.Seq())
	}
}

// TestRunnerCompactSummarizerFailurePublishesFailed covers the second of the
// three post-start exits: a Summarizer error. The start must be followed by
// exactly one terminal — session.compaction_failed, never session.compacted —
// carrying the same correlation fields as the start plus the error text the
// caller got back, so a second attached client (which never saw the return
// value) can clear its indicator and say why.
func TestRunnerCompactSummarizerFailurePublishesFailed(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("summarizer unavailable")
	r := newCheckpointRunner(t, textTurns("one"),
		func(o *runner.Options) { o.Summarizer = &stubSummarizer{err: wantErr} })

	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	wantBoundary := headEntryID(t, r)
	before := entryIDs(readEntries(t, r))

	sub := r.EventsLive()
	defer sub.Close()

	compactErr := r.Compact(ctx, "")
	if !errors.Is(compactErr, wantErr) {
		t.Fatalf("Compact() err = %v, want it to wrap %v", compactErr, wantErr)
	}

	// Drive a sentinel so "exactly one terminal, and nothing after it" is
	// observable without waiting on a channel that would never fill.
	r.Emit(event.NewSessionKilled(r.ID()))
	got := drainEvents(sub)
	want := []string{
		event.KindSessionCompactionStarted,
		event.KindSessionCompactionFailed,
		event.KindSessionKilled,
	}
	if !slices.Equal(kindsOf(got), want) {
		t.Fatalf("published kinds = %v, want %v (start, exactly one terminal, then the sentinel)", kindsOf(got), want)
	}

	started := got[0].(event.SessionCompactionStarted)
	failed, ok := got[1].(event.SessionCompactionFailed)
	if !ok {
		t.Fatalf("terminal is %T, want event.SessionCompactionFailed", got[1])
	}
	if failed.SessionID() != r.ID() {
		t.Errorf("failed.SessionID() = %q, want %q", failed.SessionID(), r.ID())
	}
	if failed.ReplacesThrough != wantBoundary || failed.ReplacesThrough != started.ReplacesThrough {
		t.Errorf("failed.ReplacesThrough = %q, want the start's boundary %q", failed.ReplacesThrough, wantBoundary)
	}
	if failed.Messages != started.Messages {
		t.Errorf("failed.Messages = %d, want the start's %d", failed.Messages, started.Messages)
	}
	if failed.Error != compactErr.Error() {
		t.Errorf("failed.Error = %q, want the error Compact returned %q", failed.Error, compactErr.Error())
	}
	if failed.Tier() != event.TierMustDeliver {
		t.Errorf("failed.Tier() = %v, want must-deliver", failed.Tier())
	}

	// Nothing was journaled: a failed compaction leaves the session untouched.
	if after := entryIDs(readEntries(t, r)); !slices.Equal(after, before) {
		t.Errorf("journal = %v after a failed compaction, want it unchanged %v", after, before)
	}
}

// TestRunnerCompactAppendFailurePublishesFailed covers the third post-start
// exit — the one that is invisible to every other test: the summarizer
// succeeded, so the model call's cost was already spent, but the compaction
// entry failed to reach durable storage. Without a terminal here a client would
// latch "compacting" forever. The fault is injected by failing the write of the
// entry whose JSON carries "type":"compaction" (verified on the wire by
// TestCompactionEntryWireDiscriminator); the test cannot pass vacuously,
// because a matcher that matched nothing would make Compact succeed and the
// error assertion fail.
func TestRunnerCompactAppendFailurePublishesFailed(t *testing.T) {
	ctx := context.Background()
	r, store := newFailingRunner(t, `"type":"compaction"`, textTurns("one", "condensed"))
	id := r.ID()

	// The Prompt itself must succeed — otherwise the fault is landing on the
	// wrong entry and the compaction append is never even reached.
	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v (the write fault must target only the compaction entry)", err)
	}
	beforeEntries, beforeFold := journalOf(t, store, id)
	if len(beforeFold) != 2 {
		t.Fatalf("precondition: fold = %d messages, want 2", len(beforeFold))
	}

	sub := r.EventsLive()
	defer sub.Close()

	compactErr := r.Compact(ctx, "")
	if !errors.Is(compactErr, errWriteFailed) {
		t.Fatalf("Compact() err = %v, want it to wrap errWriteFailed — the compaction append must actually fail", compactErr)
	}

	r.Emit(event.NewSessionKilled(id))
	got := drainEvents(sub)
	want := []string{
		event.KindSessionCompactionStarted,
		event.KindSessionCompactionFailed,
		event.KindSessionKilled,
	}
	if !slices.Equal(kindsOf(got), want) {
		t.Fatalf("published kinds = %v, want %v (start, exactly one terminal, then the sentinel)", kindsOf(got), want)
	}

	started := got[0].(event.SessionCompactionStarted)
	failed, ok := got[1].(event.SessionCompactionFailed)
	if !ok {
		t.Fatalf("terminal is %T, want event.SessionCompactionFailed", got[1])
	}
	if failed.ReplacesThrough != started.ReplacesThrough || failed.Messages != started.Messages {
		t.Errorf("failed{%q,%d} does not correlate with started{%q,%d}",
			failed.ReplacesThrough, failed.Messages, started.ReplacesThrough, started.Messages)
	}
	if failed.Error != compactErr.Error() {
		t.Errorf("failed.Error = %q, want the error Compact returned %q", failed.Error, compactErr.Error())
	}
	if !strings.Contains(failed.Error, "append compaction entry") {
		t.Errorf("failed.Error = %q, want it to name the append failure", failed.Error)
	}

	// The session is untouched: the entry that failed to write is not in the
	// journal, and the folded context is what it was before.
	afterEntries, afterFold := journalOf(t, store, id)
	if len(afterEntries) != len(beforeEntries) {
		t.Errorf("journal has %d entries after a failed compaction append, want %d unchanged", len(afterEntries), len(beforeEntries))
	}
	if len(afterFold) != len(beforeFold) {
		t.Errorf("fold = %d messages after a failed compaction append, want %d unchanged", len(afterFold), len(beforeFold))
	}
}

// TestRunnerCompactCancelledMidSummarizePublishesFailed asserts a context
// cancelled DURING summarization routes to the failed terminal (it surfaces as
// an ordinary summarizer error) and that the terminal is still delivered
// afterwards — event.Broker.Publish takes no context, so cancellation cannot
// suppress the event that clears a client's indicator.
func TestRunnerCompactCancelledMidSummarizePublishesFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var summarizerCalls int
	cancelling := summarizerFunc(func(sctx context.Context, _ runner.SummarizeRequest) (runner.SummarizeResult, error) {
		summarizerCalls++
		cancel()
		return runner.SummarizeResult{}, sctx.Err()
	})
	r := newCheckpointRunner(t, textTurns("one"), func(o *runner.Options) { o.Summarizer = cancelling })

	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	sub := r.EventsLive()
	defer sub.Close()

	compactErr := r.Compact(ctx, "")
	if summarizerCalls != 1 {
		t.Fatalf("summarizer called %d times, want 1 (the cancellation must happen mid-summarize, not at the entry check)", summarizerCalls)
	}
	if !errors.Is(compactErr, context.Canceled) {
		t.Fatalf("Compact() err = %v, want it to wrap context.Canceled", compactErr)
	}
	if ctx.Err() == nil {
		t.Fatal("precondition: context is not cancelled")
	}

	got := drainEvents(sub)
	want := []string{event.KindSessionCompactionStarted, event.KindSessionCompactionFailed}
	if !slices.Equal(kindsOf(got), want) {
		t.Fatalf("published kinds = %v, want %v — a cancelled compaction must still publish its terminal", kindsOf(got), want)
	}
	failed, ok := got[1].(event.SessionCompactionFailed)
	if !ok {
		t.Fatalf("terminal is %T, want event.SessionCompactionFailed", got[1])
	}
	if failed.Error != compactErr.Error() {
		t.Errorf("failed.Error = %q, want %q", failed.Error, compactErr.Error())
	}
}

// TestRunnerCompactEarlyExitsPublishNoStart asserts the exits ABOVE the
// summarizer call publish nothing at all. Both happen before any long-running
// work and before the boundary is fixed, so there is no window to report — and
// a start there would be the one start-with-no-terminal case that is trivially
// avoidable.
func TestRunnerCompactEarlyExitsPublishNoStart(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T) (*runner.Runner, context.Context)
		wantErr error
	}{
		{
			name: "nothing to compact",
			setup: func(t *testing.T) (*runner.Runner, context.Context) {
				t.Helper()
				return newCheckpointRunner(t, &scriptedProvider{}), context.Background()
			},
			wantErr: runner.ErrNothingToCompact,
		},
		{
			name: "context already cancelled",
			setup: func(t *testing.T) (*runner.Runner, context.Context) {
				t.Helper()
				r := newCheckpointRunner(t, textTurns("one"))
				if err := r.Prompt(context.Background(), "first"); err != nil {
					t.Fatalf("Prompt: %v", err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return r, ctx
			},
			wantErr: context.Canceled,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, ctx := tc.setup(t)
			sub := r.EventsLive()
			defer sub.Close()

			if err := r.Compact(ctx, ""); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Compact() err = %v, want %v", err, tc.wantErr)
			}

			// A sentinel makes "nothing was published" observable: it must be
			// the very first event on the stream.
			r.Emit(event.NewSessionKilled(r.ID()))
			if got, want := kindsOf(drainEvents(sub)), []string{event.KindSessionKilled}; !slices.Equal(got, want) {
				t.Errorf("published kinds = %v, want %v — an exit above the summarizer call must publish nothing", got, want)
			}
		})
	}
}

// TestRunnerCompactLegacyConsumerSeesNoChange is the portability check: an
// embedder that consumes NEITHER new kind must observe exactly what it observed
// before they existed. Each case drops the two new kinds from what Compact
// published and pins the remainder against the pre-change contract — one
// session.compacted with unchanged payload on success, and NOTHING at all on
// either failure path — alongside Compact's return value and the journal it
// wrote. Any change to those (a reworded error, an extra published event, a
// different compaction entry) fails this test.
func TestRunnerCompactLegacyConsumerSeesNoChange(t *testing.T) {
	t.Run("success publishes only session.compacted", func(t *testing.T) {
		ctx := context.Background()
		r := newCheckpointRunner(t, textTurns("one", "condensed"))
		if err := r.Prompt(ctx, "first"); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		wantBoundary := headEntryID(t, r)
		before := entryIDs(readEntries(t, r))

		sub := r.EventsLive()
		defer sub.Close()

		if err := r.Compact(ctx, ""); err != nil {
			t.Fatalf("Compact: %v", err)
		}

		legacy := withoutCompactionProgress(drainEvents(sub))
		if got, want := kindsOf(legacy), []string{event.KindSessionCompacted}; !slices.Equal(got, want) {
			t.Fatalf("legacy consumer saw %v, want %v (exactly what Compact published before the new kinds existed)", got, want)
		}
		compacted := legacy[0].(event.SessionCompacted)
		if compacted.ReplacesThrough != wantBoundary || compacted.MessagesCompacted != 2 ||
			compacted.Model != testModel || compacted.Summary != "condensed" ||
			compacted.Usage.InputTokens != 1 || compacted.Usage.OutputTokens != 1 {
			t.Errorf("session.compacted = %+v, want the unchanged payload {%q 2 %q {1 1} condensed}", compacted, wantBoundary, testModel)
		}

		// The journal write is unchanged: exactly one new entry, and its
		// type/model/usage/payload are byte-identical to the expected line.
		after := readEntries(t, r)
		if len(after) != len(before)+1 {
			t.Fatalf("journal has %d entries after Compact, want %d", len(after), len(before)+1)
		}
		got := after[len(after)-1]
		payload, err := json.Marshal(session.CompactionPayload{Summary: "condensed", ReplacesThrough: wantBoundary})
		if err != nil {
			t.Fatalf("marshal expected payload: %v", err)
		}
		usage := provider.Usage{InputTokens: 1, OutputTokens: 1}
		wantLine, err := json.Marshal(session.Entry{
			ID: got.ID, Parent: got.Parent, Type: session.EntryCompaction, Time: got.Time,
			Model: testModel, Usage: &usage, Payload: payload,
		})
		if err != nil {
			t.Fatalf("marshal expected entry: %v", err)
		}
		gotLine, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal got entry: %v", err)
		}
		if string(gotLine) != string(wantLine) {
			t.Errorf("compaction entry =\n%s\nwant\n%s", gotLine, wantLine)
		}
	})

	t.Run("summarizer failure publishes nothing", func(t *testing.T) {
		ctx := context.Background()
		wantErr := errors.New("summarizer unavailable")
		r := newCheckpointRunner(t, textTurns("one"),
			func(o *runner.Options) { o.Summarizer = &stubSummarizer{err: wantErr} })
		if err := r.Prompt(ctx, "first"); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		before := entryIDs(readEntries(t, r))

		sub := r.EventsLive()
		defer sub.Close()

		err := r.Compact(ctx, "")
		if want := "runner: compact session " + r.ID() + ": summarizer unavailable"; err == nil || err.Error() != want {
			t.Fatalf("Compact() err = %v, want the unchanged message %q", err, want)
		}
		if !errors.Is(err, wantErr) {
			t.Errorf("Compact() err = %v, want it to still wrap %v", err, wantErr)
		}
		if got := withoutCompactionProgress(drainEvents(sub)); len(got) != 0 {
			t.Errorf("legacy consumer saw %v, want nothing (a failed compaction published nothing before the new kinds existed)", kindsOf(got))
		}
		if got := entryIDs(readEntries(t, r)); !slices.Equal(got, before) {
			t.Errorf("journal = %v, want it unchanged %v", got, before)
		}
	})

	t.Run("append failure publishes nothing", func(t *testing.T) {
		ctx := context.Background()
		r, store := newFailingRunner(t, `"type":"compaction"`, textTurns("one", "condensed"))
		id := r.ID()
		if err := r.Prompt(ctx, "first"); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		beforeEntries, _ := journalOf(t, store, id)

		sub := r.EventsLive()
		defer sub.Close()

		err := r.Compact(ctx, "")
		if !errors.Is(err, errWriteFailed) {
			t.Fatalf("Compact() err = %v, want it to wrap errWriteFailed", err)
		}
		if !strings.HasPrefix(err.Error(), "runner: append compaction entry: ") {
			t.Errorf("Compact() err = %q, want the unchanged %q prefix", err, "runner: append compaction entry: ")
		}
		if got := withoutCompactionProgress(drainEvents(sub)); len(got) != 0 {
			t.Errorf("legacy consumer saw %v, want nothing", kindsOf(got))
		}
		if afterEntries, _ := journalOf(t, store, id); len(afterEntries) != len(beforeEntries) {
			t.Errorf("journal has %d entries, want %d unchanged", len(afterEntries), len(beforeEntries))
		}
	})
}
