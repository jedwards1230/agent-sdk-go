package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

func TestReplayNotifications(t *testing.T) {
	const sid = "sess-1"

	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: []provider.ContentBlock{provider.TextBlock("you are a helpful agent")}},
		{Role: provider.RoleUser, Content: []provider.ContentBlock{provider.TextBlock("list files")}},
		{
			Role: provider.RoleAssistant,
			Content: []provider.ContentBlock{
				provider.ReasoningBlock("I should run ls"),
				provider.TextBlock(""), // empty text, skipped
				provider.TextBlock("running ls now"),
				provider.ToolUseBlock("tc-1", "bash", json.RawMessage(`{"cmd":"ls"}`)),
			},
		},
		{
			Role: provider.RoleUser,
			Content: []provider.ContentBlock{
				provider.ToolResultBlock("tc-1", "a.txt\nb.txt", false),
			},
		},
		{
			Role: provider.RoleAssistant,
			Content: []provider.ContentBlock{
				provider.ToolUseBlock("tc-2", "bash", json.RawMessage(`{"cmd":"rm -rf /"}`)),
			},
		},
		{
			Role: provider.RoleUser,
			Content: []provider.ContentBlock{
				provider.ToolResultBlock("tc-2", "permission denied", true),
			},
		},
	}

	got := acp.ReplayNotifications(sid, msgs)

	wantKinds := []string{
		"user_message_chunk",  // "list files"
		"agent_thought_chunk", // "I should run ls"
		"agent_message_chunk", // "running ls now"
		"tool_call",           // tc-1 started
		"tool_call_update",    // tc-1 finished ok
		"tool_call",           // tc-2 started
		"tool_call_update",    // tc-2 finished error
	}
	if len(got) != len(wantKinds) {
		t.Fatalf("len(got) = %d, want %d; got = %#v", len(got), len(wantKinds), got)
	}
	for i, n := range got {
		if n.SessionID != sid {
			t.Errorf("notification %d: SessionID = %q, want %q", i, n.SessionID, sid)
		}
		if n.Update.Kind() != wantKinds[i] {
			t.Errorf("notification %d: Kind() = %q, want %q", i, n.Update.Kind(), wantKinds[i])
		}
	}

	// Spot-check the ordering-sensitive tool call pair's wire shape.
	toolCallJSON, err := json.Marshal(got[3])
	if err != nil {
		t.Fatalf("Marshal(tool_call) error = %v", err)
	}
	assertJSONEqual(t, toolCallJSON,
		`{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1",`+
			`"title":"bash","kind":"other","status":"pending","rawInput":{"cmd":"ls"}}}`)

	toolCallUpdatedJSON, err := json.Marshal(got[4])
	if err != nil {
		t.Fatalf("Marshal(tool_call_update) error = %v", err)
	}
	assertJSONEqual(t, toolCallUpdatedJSON,
		`{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1",`+
			`"status":"completed","content":[{"type":"content","content":{"type":"text","text":"a.txt\nb.txt"}}]}}`)

	failedUpdateJSON, err := json.Marshal(got[6])
	if err != nil {
		t.Fatalf("Marshal(failed tool_call_update) error = %v", err)
	}
	assertJSONEqual(t, failedUpdateJSON,
		`{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-2",`+
			`"status":"failed","content":[{"type":"content","content":{"type":"text","text":"permission denied"}}]}}`)

	userChunkJSON, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("Marshal(user chunk) error = %v", err)
	}
	assertJSONEqual(t, userChunkJSON,
		`{"sessionId":"sess-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"list files"}}}`)
}

func TestReplayNotificationsSkipsImageAndEmptyBlocks(t *testing.T) {
	msgs := []provider.Message{
		{
			Role: provider.RoleAssistant,
			Content: []provider.ContentBlock{
				provider.ContentBlock{Type: provider.BlockImage, ImageRef: "img-1"},
				provider.ReasoningBlock(""),
			},
		},
	}
	got := acp.ReplayNotifications("sess-1", msgs)
	if len(got) != 0 {
		t.Fatalf("got %d notifications, want 0: %#v", len(got), got)
	}
}

func TestReplayNotificationsEmptyHistoryReturnsNonNil(t *testing.T) {
	got := acp.ReplayNotifications("sess-1", nil)
	if got == nil {
		t.Fatal("ReplayNotifications(nil) returned nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

func TestReplayNotificationsSkipsSystemMessages(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: []provider.ContentBlock{provider.TextBlock("system prompt")}},
	}
	got := acp.ReplayNotifications("sess-1", msgs)
	if len(got) != 0 {
		t.Fatalf("got %d notifications, want 0: %#v", len(got), got)
	}
}
