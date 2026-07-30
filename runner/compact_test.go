package runner_test

import (
	"context"
	"errors"
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

	ev, ok := (<-sub.C).(event.SessionCompacted)
	if !ok {
		t.Fatalf("first event is not session.compacted")
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
