package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
)

func TestSessionUpdateMarshal(t *testing.T) {
	tests := []struct {
		name   string
		update acp.SessionUpdate
		want   string
	}{
		{
			name:   "user_message_chunk",
			update: acp.UserMessageChunk{Content: acp.TextBlock("hi")},
			want:   `{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"hi"}}`,
		},
		{
			name:   "agent_message_chunk",
			update: acp.AgentMessageChunk{Content: acp.TextBlock("hi")},
			want:   `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`,
		},
		{
			name:   "agent_thought_chunk",
			update: acp.AgentThoughtChunk{Content: acp.TextBlock("thinking")},
			want:   `{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking"}}`,
		},
		{
			name: "session_info_update",
			update: acp.SessionInfoUpdate{
				Title:     "Debug authentication timeout",
				UpdatedAt: "2025-01-15T12:34:56Z",
			},
			want: `{"sessionUpdate":"session_info_update","title":"Debug authentication timeout",` +
				`"updatedAt":"2025-01-15T12:34:56Z"}`,
		},
		{
			name:   "session_info_update omits empty updatedAt",
			update: acp.SessionInfoUpdate{Title: "Fix the bug"},
			want:   `{"sessionUpdate":"session_info_update","title":"Fix the bug"}`,
		},
		{
			name: "plan",
			update: acp.Plan{Entries: []acp.PlanEntry{
				{Content: "Read the code", Priority: "high", Status: "completed"},
				{Content: "Write the fix", Priority: "medium", Status: "in_progress"},
			}},
			want: `{"sessionUpdate":"plan","entries":[` +
				`{"content":"Read the code","priority":"high","status":"completed"},` +
				`{"content":"Write the fix","priority":"medium","status":"in_progress"}]}`,
		},
		{
			name:   "plan empty clears (entries [])",
			update: acp.Plan{},
			want:   `{"sessionUpdate":"plan","entries":[]}`,
		},
		{
			name: "tool_call",
			update: acp.ToolCall{
				ToolCallID: "tc-1",
				Title:      "Read file",
				ToolKind:   acp.ToolKindRead,
				Status:     acp.ToolCallStatusPending,
				RawInput:   json.RawMessage(`{"path":"a.go"}`),
			},
			want: `{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"Read file",` +
				`"kind":"read","status":"pending","rawInput":{"path":"a.go"}}`,
		},
		{
			name: "tool_call minimal (no rawInput/content)",
			update: acp.ToolCall{
				ToolCallID: "tc-2",
				Title:      "Search",
				ToolKind:   acp.ToolKindSearch,
				Status:     acp.ToolCallStatusInProgress,
			},
			want: `{"sessionUpdate":"tool_call","toolCallId":"tc-2","title":"Search",` +
				`"kind":"search","status":"in_progress"}`,
		},
		{
			name: "tool_call_update",
			update: acp.ToolCallUpdated{
				ToolCallID: "tc-1",
				Fields: acp.ToolCallUpdateFields{
					Status: acp.ToolCallStatusCompleted,
					Content: []acp.ToolCallContent{
						acp.ToolCallContentBlock{Content: acp.TextBlock("done")},
					},
				},
			},
			want: `{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed",` +
				`"content":[{"type":"content","content":{"type":"text","text":"done"}}]}`,
		},
		{
			name: "tool_call_update minimal (only status)",
			update: acp.ToolCallUpdated{
				ToolCallID: "tc-3",
				Fields:     acp.ToolCallUpdateFields{Status: acp.ToolCallStatusFailed},
			},
			want: `{"sessionUpdate":"tool_call_update","toolCallId":"tc-3","status":"failed"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.update)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, got, tc.want)
		})
	}
}

func TestSessionNotificationMarshal(t *testing.T) {
	n := acp.SessionNotification{
		SessionID: "sess-1",
		Update:    acp.AgentMessageChunk{Content: acp.TextBlock("hi")},
	}
	got, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk",` +
		`"content":{"type":"text","text":"hi"}}}`
	assertJSONEqual(t, got, want)
}
