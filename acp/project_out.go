package acp

import "github.com/jedwards1230/agent-sdk-go/event"

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
//   - session.* / turn.started / permission.*: outside the session/update
//     surface (permission.* projects via [ToRequestPermission] instead).
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
		if ev.Result != "" {
			fields.Content = []ToolCallContent{
				ToolCallContentBlock{Content: TextBlock(ev.Result)},
			}
		}
		return SessionNotification{
			SessionID: sessionID,
			Update:    ToolCallUpdated{ToolCallID: ev.ID, Fields: fields},
		}, true

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
