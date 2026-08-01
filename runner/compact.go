package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// DefaultCompactionInstructions is the instruction text [defaultSummarizer]
// uses as its system prompt when [Runner.Compact] is called with "" and
// [Options.Summarizer] is unset. It is a plain exported constant, not a
// string buried in the loop: an embedder can read it, log it, or build its
// own instructions by starting from it, and an embedder that installs a
// custom [Summarizer] need never see it at all.
const DefaultCompactionInstructions = `Summarize the conversation above so it can replace it as the context for continuing this work. Preserve: decisions made and their rationale, open questions or TODOs, the state of any files or tools touched, and anything the user explicitly asked to remember. Omit resolved back-and-forth and tool output already reflected in a decision. Write it as a terse briefing for whoever continues this session next, not a narrative retelling.`

// ErrNothingToCompact is returned by [Runner.Compact] when the session's
// current folded context is empty — a fresh session, or one whose only turns
// were already compacted or forked away. There is nothing to summarize.
var ErrNothingToCompact = errors.New("runner: nothing to compact")

// SummarizeRequest is the input to a [Summarizer]: the folded context being
// compacted away (exactly what [Runner.Fold] would return at the moment
// [Runner.Compact] was called), the model this runner is currently using (the
// default summarizer's own choice; an override is free to ignore it and use a
// different one), and the instructions Compact was called with — or
// [DefaultCompactionInstructions] when the caller passed "".
type SummarizeRequest struct {
	// Messages is the context being compacted away, oldest first.
	Messages []provider.Message
	// Model is the runner's current model, per [provider.Lookup] /
	// [provider.Resolve] conventions elsewhere in this package.
	Model string
	// Instructions guides what the summary should preserve. Never "" — see
	// [DefaultCompactionInstructions].
	Instructions string
}

// SummarizeResult is a [Summarizer]'s output.
type SummarizeResult struct {
	// Summary is the text that replaces Messages as the session's compacted
	// context — it becomes [session.CompactionPayload.Summary].
	Summary string
	// Model is the model that produced Summary, or "" when the strategy made
	// no model call (e.g. a deterministic non-LLM condenser). Independent of
	// Usage: a strategy could set one without the other, though the default
	// summarizer always sets both together or neither.
	Model string
	// Usage is the token usage the strategy spent producing Summary, or the
	// zero value when it made no model call. When the strategy calls a
	// model over exactly Messages (as the default summarizer does),
	// Usage.InputTokens (+ CacheReadTokens) is the closest available
	// measurement of how many tokens Messages actually cost — the provider
	// tokenized exactly that content to answer the call — which
	// [Runner.Compact] reports as the "before" figure on
	// [event.SessionCompacted] without a separate tokenizer dependency;
	// Usage.OutputTokens is the "after" figure (the size of Summary itself).
	Usage provider.Usage
}

// Summarizer produces a [Runner.Compact] summary. It is the seam that keeps
// compaction from hardcoding a prompt or a model choice: [Options.Summarizer]
// lets an embedder replace the ENTIRE strategy — not just the instructions
// text — with a different (cheaper/faster) model, a custom prompt template,
// an external summarization service, or a deterministic non-LLM condenser for
// tests. Nil [Options.Summarizer] uses [defaultSummarizer].
type Summarizer interface {
	// Summarize returns the summary for req.Messages. An error aborts
	// Compact before anything is journaled or published.
	Summarize(ctx context.Context, req SummarizeRequest) (SummarizeResult, error)
}

// defaultSummarizer is the [Summarizer] a Runner uses when
// [Options.Summarizer] is nil: one non-tool completion call against the
// runner's own bound provider, with req.Instructions as the system prompt and
// req.Messages as the conversation to condense. It never touches the
// runner's journal or broker directly — Compact does that with whatever this
// returns — so it is exercised here exactly like an embedder's own
// [Summarizer] would be.
type defaultSummarizer struct {
	provider provider.Provider
}

// Summarize implements [Summarizer].
func (d defaultSummarizer) Summarize(ctx context.Context, req SummarizeRequest) (SummarizeResult, error) {
	handle, err := d.provider.Stream(ctx, provider.Request{
		Model:    req.Model,
		System:   req.Instructions,
		Messages: req.Messages,
	})
	if err != nil {
		return SummarizeResult{}, fmt.Errorf("runner: compaction summarizer call: %w", err)
	}
	defer func() { _ = handle.Close() }()

	var summary strings.Builder
	var usage provider.Usage
	for se, err := range provider.Iter(handle) {
		if err != nil {
			return SummarizeResult{}, fmt.Errorf("runner: compaction summarizer stream: %w", err)
		}
		switch se.Type {
		case provider.StreamTextDelta:
			summary.WriteString(se.Text)
		case provider.StreamFinished:
			usage = se.Usage
		}
	}
	return SummarizeResult{Summary: summary.String(), Model: req.Model, Usage: usage}, nil
}

// Compact replaces the session's history up to its current HEAD with a
// summary, so the next [Runner.Fold] — the next [Runner.Prompt], or a
// [Resume]d session — sees the summary in place of everything it replaces
// instead of the full transcript ([session.Journal.Fold] already stops at a
// compaction entry; this is its only producer).
//
// # What Compact does not do
//
// Compact carries no policy for WHEN to compact. There is no size threshold,
// no automatic trigger, and no wiring into [Runner.Prompt] or the loop: the
// SDK ships the seam, not the decision. An embedder decides — reading
// [Runner.LastUsage] (or a live turn-finished event off [Runner.Events],
// which carries the model's context-window size alongside the same usage) to
// judge pressure, or simply honoring an explicit user command — and calls
// Compact exactly as it would call [Runner.Checkpoint] or [Runner.Fork]:
// between turns, not from inside one.
//
// Because Compact always operates on whatever the journal's HEAD is AT THE
// MOMENT it is called, calling it right before the next Prompt (sized against
// the last known usage) and calling it right after a Prompt returns (sized
// against that turn's just-settled usage) are equally correct — there is no
// separate code path for either. The choice belongs to the caller, not to
// this method.
//
// A hook-based alternative — folding compaction into the loop's
// TransformContext family so it fires automatically inside a run — was
// considered and rejected: TransformContext rewrites the in-flight message
// list for ONE model call only, is never journaled, and does not survive
// [Resume]; making it durable and observable would mean giving the loop
// direct journal/broker access that today only the Runner's synchronous
// append path (and its awaitJournaled barrier) has, reintroducing exactly the
// ordering hazard that barrier exists to prevent. An explicit method matches
// every other structural seam this package already ships (Checkpoint, Fork,
// Rewind) and needed no new machinery.
//
// # Summarization is a seam, not a fixed prompt
//
// instructions guides what the summary preserves; "" uses
// [DefaultCompactionInstructions]. The actual summarization STRATEGY — model,
// prompt template, or whether a model is called at all — is
// [Options.Summarizer] ([Summarizer]), not a hardcoded call inside Compact:
// instructions is threaded to it as [SummarizeRequest.Instructions], and it is
// entirely up to the installed strategy what it does with that string.
//
// # Mechanics
//
// Compact drains the journaling handshake first (see awaitJournaled), so it
// summarizes and replaces exactly what a Prompt right now would fold — the
// same discipline [Runner.Checkpoint] and [Runner.Fork] already follow. It
// returns [ErrNothingToCompact] when the current folded context is empty.
//
// On success it appends a [session.EntryCompaction] carrying the summary (and,
// when the strategy made a model call, that call's own model/usage — see
// [session.WithEntryModel], [session.WithEntryUsage] — so a compaction's cost
// folds into [Runner.Cost] like any other turn instead of being invisible),
// then publishes a must-deliver [event.SessionCompacted] carrying the same
// boundary, model, usage, and summary text, so a client can render what
// happened without re-reading the journal.
//
// Compaction is additive, like [Runner.Fork]: nothing is deleted. The
// summarized entries remain on disk, still readable, and still count toward
// [Runner.Cost] — only the folded context changes.
//
// # Observability: the start event and its total outcome
//
// A summarization is a full model call that streams nothing and can run a
// minute or more, so Compact also publishes a must-deliver
// [event.SessionCompactionStarted] carrying the boundary and the message count
// — enough for a client that did NOT call Compact to know one is in flight.
//
// It is published IMMEDIATELY BEFORE the [Summarizer] call and not one line
// earlier. That boundary is load-bearing: every exit above it (a cancelled
// context, [ErrNothingToCompact]) happens before any long-running work and
// before the boundary is fixed, so there is no window to report and NOTHING is
// published; every exit below it publishes exactly one terminal event —
// [event.SessionCompacted] on success, [event.SessionCompactionFailed] when the
// summarizer errors, the compaction entry fails to append, or the summarizer
// leaves without returning at all, by panic (re-raised after the event goes
// out, never swallowed) or by runtime.Goexit. A client may therefore latch an
// indicator on the start and clear it on either terminal. What breaks that pair
// is not an exit path but a severed subscription — force-unsubscribed, or the
// broker closed out from under the compaction; see
// [event.SessionCompactionStarted], which documents both and the closed channel
// that signals them.
//
// Note that this publish is a new blocking point inside Compact: a must-deliver
// publish blocks up to the broker's bound on a subscriber whose buffer is full,
// and Compact now does that BEFORE the long summarizer call rather than only
// after it.
//
// Precondition, like Fork: do not call Compact while a Prompt — or another
// Compact — is in flight on another goroutine. A run still in progress goes on
// publishing turns that would land after the compaction boundary this call
// fixes, and two concurrent Compacts read the same HEAD and emit two
// indistinguishable start/terminal pairs carrying identical ReplacesThrough.
func (r *Runner) Compact(ctx context.Context, instructions string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.awaitJournaled()

	msgs := r.journal.Fold()
	if len(msgs) == 0 {
		return ErrNothingToCompact
	}
	if instructions == "" {
		instructions = DefaultCompactionInstructions
	}
	replacesThrough := r.journal.Head()

	// The start goes out here and not one line earlier: everything above is
	// cheap and can still decline to compact, and a start published there would
	// be a start with no terminal. From this point every exit publishes exactly
	// one terminal event.
	r.broker.Publish(event.NewSessionCompactionStarted(r.ID(), replacesThrough, len(msgs)))

	// Every path that publishes its own terminal sets terminal = true on the
	// line before it; this defer covers every OTHER way out of a function that
	// has already published a start. [Summarizer] is an embedder seam, i.e.
	// arbitrary third-party code, so leaving without returning is a real exit,
	// and a host that recovers at its own per-op boundary would latch
	// "compacting" forever. It RE-PANICS after publishing: swallowing an
	// embedder's panic would be a far worse behavior change than the missing
	// event it fixes, and Compact must stay inert for a caller that consumes
	// neither new kind.
	//
	// The flag is what makes this total; keying off recover() != nil would not
	// be. recover returns nil during a runtime.Goexit unwind, so a Summarizer
	// that Goexits — a test summarizer calling t.Fatal, or any library using
	// Goexit for control flow — would run this defer, see no panic value, and
	// publish nothing while Compact never returns. That is strictly worse than
	// the force-unsubscribe hole [event.SessionCompactionStarted] documents,
	// because no subscription is closed either: the latch would stick with no
	// signal of any kind.
	terminal := false
	defer func() {
		if terminal {
			return
		}
		p := recover()
		reason := "runner: compaction abandoned: the summarizer exited via runtime.Goexit without returning"
		if p != nil {
			reason = fmt.Sprintf("runner: compaction panicked: %v", p)
		}
		r.broker.Publish(event.NewSessionCompactionFailed(r.ID(), replacesThrough, len(msgs), reason))
		if p != nil {
			panic(p)
		}
	}()

	result, err := r.summarizer.Summarize(ctx, SummarizeRequest{
		Messages:     msgs,
		Model:        r.currentModel(),
		Instructions: instructions,
	})
	if err != nil {
		err = fmt.Errorf("runner: compact session %s: %w", r.ID(), err)
		terminal = true
		r.broker.Publish(event.NewSessionCompactionFailed(r.ID(), replacesThrough, len(msgs), err.Error()))
		return err
	}

	var entryOpts []session.EntryOpt
	if result.Model != "" {
		entryOpts = append(entryOpts, session.WithEntryModel(result.Model))
	}
	if !result.Usage.IsZero() {
		entryOpts = append(entryOpts, session.WithEntryUsage(result.Usage))
	}
	if _, err := r.journal.Append(session.NewCompactionEntry(result.Summary, replacesThrough, entryOpts...)); err != nil {
		err = fmt.Errorf("runner: append compaction entry: %w", err)
		terminal = true
		r.broker.Publish(event.NewSessionCompactionFailed(r.ID(), replacesThrough, len(msgs), err.Error()))
		return err
	}

	terminal = true
	r.broker.Publish(event.NewSessionCompacted(r.ID(), replacesThrough, len(msgs), result.Model, result.Usage, result.Summary))
	return nil
}

// LastUsage returns the model and token usage of the most recently completed
// turn in the session's CURRENT folded context — see
// [session.Journal.LastUsage]. Pair the model with [provider.Lookup] for a
// context-window size to build a pressure ratio, exactly as a live
// turn-finished event's ContextWindow is derived. A caller already tracking
// turn-finished events off [Runner.Events] has this live and does not need to
// poll here; LastUsage exists for the gap before the first one arrives in
// this process — most notably right after [Resume].
func (r *Runner) LastUsage() (model string, usage provider.Usage, ok bool) {
	return r.journal.LastUsage()
}
