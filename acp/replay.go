package acp

import "github.com/jedwards1230/agent-sdk-go/provider"

// ReplayNotifications projects a session's folded conversation history
// (provider messages) into the ordered session/update notifications an ACP
// agent MUST replay on session/load (ACP v1: "the Agent MUST replay the
// entire conversation to the Client in the form of session/update
// notifications"). It mirrors the live projection in [ToSessionUpdate] so a
// replayed transcript is shaped identically to a streamed one.
//
// System messages are skipped (not part of the visible transcript), as are
// empty text/reasoning blocks and image blocks (M2 placeholder, unmodeled).
// The returned slice is always non-nil.
func ReplayNotifications(sessionID string, msgs []provider.Message) []SessionNotification {
	notifications := make([]SessionNotification, 0, len(msgs))

	for _, msg := range msgs {
		if msg.Role == provider.RoleSystem {
			continue
		}

		for _, b := range msg.Content {
			switch b.Type {
			case provider.BlockText:
				if b.Text == "" {
					continue
				}
				var update SessionUpdate
				if msg.Role == provider.RoleUser {
					update = UserMessageChunk{Content: TextBlock(b.Text)}
				} else {
					update = AgentMessageChunk{Content: TextBlock(b.Text)}
				}
				notifications = append(notifications, SessionNotification{SessionID: sessionID, Update: update})

			case provider.BlockReasoning:
				if b.Text == "" {
					continue
				}
				notifications = append(notifications, SessionNotification{
					SessionID: sessionID,
					Update:    AgentThoughtChunk{Content: TextBlock(b.Text)},
				})

			case provider.BlockToolUse:
				notifications = append(notifications, SessionNotification{
					SessionID: sessionID,
					Update: ToolCall{
						ToolCallID: b.ToolUseID,
						Title:      b.ToolName,
						ToolKind:   ToolKindOther,
						Status:     ToolCallStatusPending,
						RawInput:   b.ToolInput,
					},
				})

			case provider.BlockToolResult:
				status := ToolCallStatusCompleted
				if b.IsError {
					status = ToolCallStatusFailed
				}
				fields := ToolCallUpdateFields{Status: status}
				if b.ToolResult != "" {
					fields.Content = []ToolCallContent{
						ToolCallContentBlock{Content: TextBlock(b.ToolResult)},
					}
				}
				notifications = append(notifications, SessionNotification{
					SessionID: sessionID,
					Update:    ToolCallUpdated{ToolCallID: b.ToolUseID, Fields: fields},
				})

			case provider.BlockImage:
				// M2 placeholder; unmodeled on the ACP side.
			}
		}
	}

	return notifications
}
