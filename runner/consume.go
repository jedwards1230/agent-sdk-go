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

// toolResult is one tool call's settled outcome — the authoritative input it
// ran with, its result text, and whether it errored — as tool.call.finished
// carries it. input is the complete assembled input, distinct from the
// tool.call.started seed (an empty "{}" when a provider streams the arguments);
// it is preferred over that seed when journaling the tool_use block.
type toolResult struct {
	input   json.RawMessage
	content string
	isError bool
}

// reasoningBlock is one settled reasoning message's content and its opaque
// per-block Meta (e.g. an Anthropic signature, or an OpenAI reasoning item id
// and encrypted_content). One is kept per reasoning MessageFinished so distinct
// reasoning items in a turn do not collapse into one block and lose Meta.
type reasoningBlock struct {
	text string
	meta map[string]string
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
	reasoningBlocks []reasoningBlock // one entry per settled reasoning message, in order
	text            strings.Builder
	textMeta        map[string]string // accumulated MessageFinished.Meta for text content
	usage           provider.Usage
	started         []startedCall         // tool calls announced this turn, in order
	results         map[string]toolResult // tool.call.finished result by call id
	stop            string                // turn.finished stop reason
	finished        bool                  // turn.finished observed for this iteration
	msgFlushed      bool                  // assistant message entry already written
}

func newTurnAcc() *turnAcc {
	return &turnAcc{results: make(map[string]toolResult)}
}

// reset clears the accumulator for the next iteration.
func (a *turnAcc) reset() {
	a.reasoningBlocks = nil
	a.text.Reset()
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
	// One reasoning block per settled reasoning message, in order. A block is
	// emitted even when its summary text is empty as long as it carries Meta —
	// an OpenAI reasoning item can stream no summary yet still carry the
	// encrypted_content / item_id Meta that buildInput must replay.
	for _, rb := range a.reasoningBlocks {
		if rb.text == "" && len(rb.meta) == 0 {
			continue
		}
		block := provider.ReasoningBlock(rb.text)
		if len(rb.meta) > 0 {
			block.Meta = rb.meta
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
			// tool.call.started carries only the start-of-block seed (an empty
			// "{}" for a streamed tool call); tool.call.finished carries the
			// authoritative assembled input. Prefer the latter when it arrived so
			// the journaled tool_use block holds the real arguments, not "{}".
			input := c.input
			if res, ok := a.results[c.id]; ok && len(res.input) > 0 {
				input = res.input
			}
			blocks = append(blocks, provider.ToolUseBlock(c.id, c.name, input))
		}
	}
	return blocks
}

// consume journals each iteration's settled output (see turnAcc) until the
// broker closes its subscription. It runs on its own goroutine for the lifetime
// of the Runner. It also services [Runner.awaitJournaled] barriers: when a
// Prompt finishes its run it sends a barrier, and consume drains every buffered
// event before acking, so the caller's next user-message append cannot reorder
// ahead of this run's assistant/tool entries. Close waits for it to finish
// draining before closing the journal, so a killed run's already-settled prefix
// is durable once Close returns.
func (r *Runner) consume(sub *event.Subscription) {
	defer close(r.journalDone)

	acc := newTurnAcc()
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				// Belt-and-suspenders: settled text that never saw a
				// turn.finished (an out-of-band teardown) is persisted rather
				// than dropped. No-op after a normal reset or an already-flushed
				// message.
				r.flushAssistant(acc, false)
				return
			}
			r.handleEvent(acc, e)

		case ack := <-r.barrier:
			// Drain everything already buffered before acking: the run's
			// publishes all completed before Prompt sent the barrier, so this
			// guarantees the run's turns are journaled by the time the ack fires.
			closed := r.drain(sub, acc)
			close(ack)
			if closed {
				r.flushAssistant(acc, false)
				return
			}
		}
	}
}

// handleEvent journals one settled event into the turn accumulator.
func (r *Runner) handleEvent(acc *turnAcc, e event.Event) {
	switch ev := e.(type) {
	case event.MessageFinished:
		switch ev.MessageKind {
		case event.MessageText:
			acc.text.WriteString(ev.Content)
			acc.textMeta = mergeMeta(acc.textMeta, ev.Meta)
		case event.MessageReasoning:
			// Each settled reasoning message is its own block. The loop already
			// merges a contiguous reasoning run into one MessageFinished, so a
			// second reasoning event in a turn is a distinct item whose Meta (an
			// OpenAI item id / encrypted_content) must not collapse into the
			// first item's.
			acc.reasoningBlocks = append(acc.reasoningBlocks, reasoningBlock{text: ev.Content, meta: mergeMeta(nil, ev.Meta)})
		}

	case event.ToolCallStarted:
		acc.started = append(acc.started, startedCall{id: ev.ID, name: ev.Name, input: ev.Input})

	case event.ToolCallFinished:
		acc.results[ev.ID] = toolResult{input: ev.Input, content: ev.Result, isError: ev.IsError}
		r.maybeFlushToolTurn(acc)

	case event.TurnFinished:
		acc.usage = ev.Usage
		acc.stop = ev.StopReason
		acc.finished = true
		if ev.StopReason == string(provider.StopToolUse) {
			// Tools will run; wait for their results, then flush the assistant
			// message and the result round together.
			r.maybeFlushToolTurn(acc)
		} else {
			// No tools will run: flush the settled text/reasoning now, dropping
			// any orphaned announced calls.
			r.flushAssistant(acc, false)
			acc.reset()
		}
	}
}

// drain journals every event currently buffered on sub without blocking,
// returning whether the subscription was closed (the broker shut down) while
// draining.
func (r *Runner) drain(sub *event.Subscription, acc *turnAcc) (closed bool) {
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				return true
			}
			r.handleEvent(acc, e)
		default:
			return false
		}
	}
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
