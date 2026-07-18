package acp

import (
	"time"

	"github.com/jedwards1230/agent-sdk-go/event"
)

// ToSessionUpdate projects one [event.Event] to a session/update notification.
// ok is false when the event has no ACP projection:
//
//   - message.started / message.finished for agent text/reasoning: the client
//     already received the content via message.delta chunks
//     (agent_message_chunk / agent_thought_chunk); ACP has no separate
//     "message started/finished" signal, and re-sending the settled content
//     would duplicate it. The one exception is a settled
//     [event.MessageUser] message (below), which streams live — a user
//     prompt is never sent as deltas, so there is nothing to duplicate.
//   - tool.call.delta: ACP has no incremental tool-output chunk distinct from
//     a tool_call_update; a daemon that wants to stream tool output can emit
//     tool_call_update updates itself, but this projection does not synthesize
//     one per delta.
//   - session lifecycle (session.created/resumed/…) / turn.started /
//     permission.*: outside the session/update surface (permission.* projects
//     via [ToRequestPermission] instead). session.info is the one session.*
//     event that DOES project — to a session_info_update carrying the new
//     title (below).
//
// An [event.TurnFinished] projects to a usage_update carrying the tokens now in
// context, the model's total context-window size, and (when priced) the turn's
// cost — but ONLY when the model's context window is known
// (ev.ContextWindow > 0) AND the turn captured genuine usage (used > 0). A turn
// from an unregistered/faux model (window 0) has no window to report; a turn
// that terminated without real usage (max-turns cap, fail-closed, a partial or
// zero usage carried through the loop's error paths) still stamps the window but
// has no tokens to report — reporting used:0 alongside a full context window
// would make a client's meter read "0 / 200k" precisely when context is full or
// truncated. Both cases yield ok=false. This is additive: TurnFinished still
// independently drives [ToPromptResponse].
func ToSessionUpdate(sessionID string, e event.Event) (SessionNotification, bool) {
	switch ev := e.(type) {
	case event.MessageDelta:
		block := TextBlock(ev.Text)
		var update SessionUpdate
		if ev.MessageKind == event.MessageReasoning {
			update = AgentThoughtChunk{Content: block}
		} else {
			update = AgentMessageChunk{Content: block}
		}
		return SessionNotification{SessionID: sessionID, Update: update}, true

	case event.MessageFinished:
		if ev.MessageKind != event.MessageUser {
			return SessionNotification{}, false
		}
		// The user's own prompt turn has no deltas, so its settled
		// MessageFinished is the only signal a client gets — project it
		// directly, mirroring how ReplayNotifications folds a journaled user
		// message into a UserMessageChunk.
		return SessionNotification{
			SessionID: sessionID,
			Update:    UserMessageChunk{Content: TextBlock(ev.Content)},
		}, true

	case event.ToolCallStarted:
		return SessionNotification{
			SessionID: sessionID,
			Update: ToolCall{
				ToolCallID: ev.ID,
				Title:      ev.Name,
				ToolKind:   ToolKindOther,
				Status:     ToolCallStatusPending,
				RawInput:   ev.Input,
			},
		}, true

	case event.ToolCallFinished:
		status := ToolCallStatusCompleted
		if ev.IsError {
			status = ToolCallStatusFailed
		}
		fields := ToolCallUpdateFields{Status: status}
		// The pending ToolCall (from tool.call.started) carries only the
		// start-of-block seed; tool.call.finished carries the authoritative
		// assembled input. Surface it so a client reconciles a placeholder "{}"
		// with the real arguments.
		if len(ev.Input) > 0 {
			fields.RawInput = ev.Input
		}
		// A file-editing tool surfaces its change as a structured diff block a
		// client renders as before/after; the plain-text Result ("edited X") is
		// redundant with it, so the diffs replace it. A creation carries no
		// OldText (omitted from the wire). Other tools fall back to the text
		// Result block.
		switch {
		case len(ev.Edits) > 0:
			content := make([]ToolCallContent, 0, len(ev.Edits))
			for _, ed := range ev.Edits {
				content = append(content, ToolCallContentDiff{
					Path:    ed.Path,
					OldText: ed.OldText,
					NewText: ed.NewText,
				})
			}
			fields.Content = content
		case ev.Result != "":
			fields.Content = []ToolCallContent{
				ToolCallContentBlock{Content: TextBlock(ev.Result)},
			}
		}
		return SessionNotification{
			SessionID: sessionID,
			Update:    ToolCallUpdated{ToolCallID: ev.ID, Fields: fields},
		}, true

	case event.TurnFinished:
		// A faux/unregistered-model turn has no known context window; there is
		// nothing to report, so skip it.
		if ev.ContextWindow <= 0 {
			return SessionNotification{}, false
		}
		// used = the full prompt (input + cache-read) plus the generated output
		// ≈ the tokens now occupying the context. Clamp a (never-expected)
		// negative sum to 0 before the uint64 conversion.
		used := ev.Usage.InputTokens + ev.Usage.CacheReadTokens + ev.Usage.OutputTokens
		if used <= 0 {
			// No genuine usage captured. A real turn always consumes input
			// tokens, so used<=0 means this TurnFinished came from a path that
			// stamped the context window onto an empty/partial Usage (max-turns
			// cap, fail-closed, or a zeroed loop error path). Skip rather than
			// project a misleading used:0 against a full window.
			return SessionNotification{}, false
		}
		update := UsageUpdate{
			Used: uint64(used),
			Size: uint64(ev.ContextWindow),
		}
		if ev.Cost != nil {
			// NOTE: the UsageUpdate schema documents cost as "cumulative session
			// cost", but this maps the per-turn cost (ev.Cost.USD). The
			// divergence is known and intentional for now; surfacing a running
			// session total is a separate follow-up.
			update.Cost = &Cost{Amount: ev.Cost.USD, Currency: "USD"}
		}
		return SessionNotification{SessionID: sessionID, Update: update}, true

	case event.SessionInfoUpdated:
		// session.info carries the session's new title. updatedAt is the event's
		// own publish timestamp — the natural "changed at" the broker already
		// stamps — omitted when the event has not been through a broker (a zero
		// time).
		update := SessionInfoUpdate{Title: ev.Title}
		if t := ev.Time(); !t.IsZero() {
			update.UpdatedAt = t.UTC().Format(time.RFC3339Nano)
		}
		return SessionNotification{SessionID: sessionID, Update: update}, true

	default:
		return SessionNotification{}, false
	}
}

// StopReasonFor maps the loop's internal stop reason string
// ([event.TurnFinished.StopReason]) to an ACP [StopReason]. ok is false for
// "tool_use" (mid-turn, not a terminal reason ACP models) and "error" (ACP has
// no error stop reason; a session.error event carries that signal instead).
func StopReasonFor(stop string) (StopReason, bool) {
	switch stop {
	case "end_turn", "stop_sequence":
		return StopReasonEndTurn, true
	case "max_tokens":
		return StopReasonMaxTokens, true
	case "max_turns":
		return StopReasonMaxTurnRequests, true
	case "refusal":
		return StopReasonRefusal, true
	case "cancelled":
		return StopReasonCancelled, true
	default:
		return "", false
	}
}

// ToPromptResponse projects a [event.TurnFinished] to a session/prompt
// response. ok is false when the turn's stop reason has no ACP projection
// (see [StopReasonFor]); the caller should not send a response in that case.
func ToPromptResponse(e event.TurnFinished) (PromptResponse, bool) {
	reason, ok := StopReasonFor(e.StopReason)
	if !ok {
		return PromptResponse{}, false
	}
	return PromptResponse{StopReason: reason}, true
}

// ToRequestPermission builds a session/request_permission request for a tool
// call awaiting a client decision.
func ToRequestPermission(sessionID, toolCallID, toolName string, options []PermissionOption) RequestPermissionRequest {
	return RequestPermissionRequest{
		SessionID: sessionID,
		ToolCall:  ToolCallUpdate{ToolCallID: toolCallID, Fields: ToolCallUpdateFields{Title: toolName}},
		Options:   options,
	}
}
