package runner

import (
	"encoding/json"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// startedCall is a tool call announced by tool.call.started, held (in order)
// until turn.finished decides whether it executes and tool.call.finished
// supplies its result.
type startedCall struct {
	id    string
	name  string
	input json.RawMessage
}

// toolResult is one tool call's settled outcome — its result text and
// whether it errored — as tool.call.finished carries it.
type toolResult struct {
	content string
	isError bool
}

// turnAcc accumulates one model-call iteration's settled output across events
// and journals it as the SDK's verbatim-content-block entries: an assistant
// [session.NewMessageEntry] carrying the turn's reasoning, text, and tool_use
// blocks, and — when tools run — a [session.NewToolRoundEntry] carrying the
// matching tool_result blocks (which Fold projects back as a user message).
//
// Two correctness rules drive the flush timing:
//
//   - A kill can land after a turn's assistant text/reasoning has settled but
//     before a just-announced tool call finishes. Tools run only on a
//     StopToolUse stop; on any other stop reason (end_turn, cancelled, error)
//     the loop returns without executing them, so no tool.call.finished
//     arrives. There, the settled text/reasoning is flushed immediately and
//     the orphaned started-but-unexecuted calls are DROPPED — never journaled
//     as a tool_use, which without a matching tool_result would be a dangling
//     block that breaks the provider projection on resume.
//   - For a StopToolUse turn, the assistant message (with its tool_use blocks)
//     and the tool_result round are flushed together, only once every started
//     call has a result — so the journal never holds a tool_use without its
//     result.
type turnAcc struct {
	reasoning     strings.Builder
	text          strings.Builder
	reasoningMeta map[string]string // accumulated MessageFinished.Meta for reasoning content
	textMeta      map[string]string // accumulated MessageFinished.Meta for text content
	usage         provider.Usage
	started       []startedCall         // tool calls announced this turn, in order
	results       map[string]toolResult // tool.call.finished result by call id
	stop          string                // turn.finished stop reason
	finished      bool                  // turn.finished observed for this iteration
	msgFlushed    bool                  // assistant message entry already written
}

func newTurnAcc() *turnAcc {
	return &turnAcc{results: make(map[string]toolResult)}
}

// reset clears the accumulator for the next iteration.
func (a *turnAcc) reset() {
	a.reasoning.Reset()
	a.text.Reset()
	a.reasoningMeta = nil
	a.textMeta = nil
	a.usage = provider.Usage{}
	a.started = nil
	for id := range a.results {
		delete(a.results, id)
	}
	a.stop = ""
	a.finished = false
	a.msgFlushed = false
}

// mergeMeta merges src into dst (allocating dst on first use) and returns
// the result. A block-kind can finish more than once in a turn in principle,
// so entries are merged rather than overwritten; src's keys win on collision.
func mergeMeta(dst, src map[string]string) map[string]string {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]string, len(src))
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// assistantBlocks builds the assistant message's content blocks in provider
// order — reasoning, then text, then (only when includeToolUse) one tool_use
// block per announced call. Reasoning and text blocks carry any accumulated
// per-block Meta (e.g. an Anthropic reasoning signature), so it round-trips
// through the journal and is replayed verbatim on a later turn.
func (a *turnAcc) assistantBlocks(includeToolUse bool) []provider.ContentBlock {
	var blocks []provider.ContentBlock
	if s := a.reasoning.String(); s != "" {
		block := provider.ReasoningBlock(s)
		if len(a.reasoningMeta) > 0 {
			block.Meta = a.reasoningMeta
		}
		blocks = append(blocks, block)
	}
	if s := a.text.String(); s != "" {
		block := provider.TextBlock(s)
		if len(a.textMeta) > 0 {
			block.Meta = a.textMeta
		}
		blocks = append(blocks, block)
	}
	if includeToolUse {
		for _, c := range a.started {
			blocks = append(blocks, provider.ToolUseBlock(c.id, c.name, c.input))
		}
	}
	return blocks
}

// consume drains sub until the broker closes it, journaling each iteration's
// settled output (see turnAcc). It runs on its own goroutine for the lifetime
// of the Runner; Close waits for it to finish draining before closing the
// journal, so a killed run's already-settled prefix is durable once Close
// returns.
func (r *Runner) consume(sub *event.Subscription) {
	defer close(r.journalDone)

	acc := newTurnAcc()
	for e := range sub.C {
		switch ev := e.(type) {
		case event.MessageFinished:
			switch ev.MessageKind {
			case event.MessageText:
				acc.text.WriteString(ev.Content)
				acc.textMeta = mergeMeta(acc.textMeta, ev.Meta)
			case event.MessageReasoning:
				acc.reasoning.WriteString(ev.Content)
				acc.reasoningMeta = mergeMeta(acc.reasoningMeta, ev.Meta)
			}

		case event.ToolCallStarted:
			acc.started = append(acc.started, startedCall{id: ev.ID, name: ev.Name, input: ev.Input})

		case event.ToolCallFinished:
			acc.results[ev.ID] = toolResult{content: ev.Result, isError: ev.IsError}
			r.maybeFlushToolTurn(acc)

		case event.TurnFinished:
			acc.usage = ev.Usage
			acc.stop = ev.StopReason
			acc.finished = true
			if ev.StopReason == string(provider.StopToolUse) {
				// Tools will run; wait for their results, then flush the
				// assistant message and the result round together.
				r.maybeFlushToolTurn(acc)
			} else {
				// No tools will run: flush the settled text/reasoning now,
				// dropping any orphaned announced calls.
				r.flushAssistant(acc, false)
				acc.reset()
			}
		}
	}

	// Belt-and-suspenders: settled text that never saw a turn.finished (an
	// out-of-band teardown) is persisted rather than dropped. No-op after a
	// normal reset or an already-flushed message.
	r.flushAssistant(acc, false)
}

// maybeFlushToolTurn flushes a StopToolUse iteration once every announced call
// has a result: the assistant message (text/reasoning + tool_use blocks) and
// then the tool_result round, after which it resets. It no-ops until the turn
// has finished and all results are in.
func (r *Runner) maybeFlushToolTurn(acc *turnAcc) {
	if !acc.finished || acc.stop != string(provider.StopToolUse) {
		return
	}
	if len(acc.results) < len(acc.started) {
		return // still waiting on tool results
	}
	r.flushAssistant(acc, true)
	r.flushRound(acc)
	acc.reset()
}

// flushAssistant appends the assistant message entry (reasoning + text, plus
// tool_use blocks when includeToolUse) at most once per turn. It no-ops when
// the message has no blocks or was already written.
func (r *Runner) flushAssistant(acc *turnAcc, includeToolUse bool) {
	if acc.msgFlushed {
		return
	}
	blocks := acc.assistantBlocks(includeToolUse)
	if len(blocks) == 0 {
		return
	}
	msg := provider.Message{Role: provider.RoleAssistant, Content: blocks}
	entry := session.NewMessageEntry(msg, session.WithEntryModel(r.model), session.WithEntryUsage(acc.usage))
	if _, err := r.journal.Append(entry); err != nil {
		r.setJournalWriteErr(err)
	}
	acc.msgFlushed = true
}

// flushRound appends the tool_result round for the turn's announced calls, in
// start order. Each result carries the error flag tool.call.finished reported,
// journaled verbatim.
func (r *Runner) flushRound(acc *turnAcc) {
	if len(acc.started) == 0 {
		return
	}
	blocks := make([]provider.ContentBlock, 0, len(acc.started))
	for _, c := range acc.started {
		res := acc.results[c.id]
		blocks = append(blocks, provider.ToolResultBlock(c.id, res.content, res.isError))
	}
	entry := session.NewToolRoundEntry(blocks, session.WithEntryModel(r.model))
	if _, err := r.journal.Append(entry); err != nil {
		r.setJournalWriteErr(err)
	}
}
