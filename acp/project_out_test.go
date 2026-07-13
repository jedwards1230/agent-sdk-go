package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

func TestToSessionUpdate(t *testing.T) {
	const sid = "sess-1"

	tests := []struct {
		name     string
		event    event.Event
		wantOK   bool
		wantJSON string
	}{
		{
			name:   "message delta text -> agent_message_chunk",
			event:  event.NewMessageDelta(sid, event.MessageText, "hi"),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk",` +
				`"content":{"type":"text","text":"hi"}}}`,
		},
		{
			name:   "message delta reasoning -> agent_thought_chunk",
			event:  event.NewMessageDelta(sid, event.MessageReasoning, "thinking"),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"agent_thought_chunk",` +
				`"content":{"type":"text","text":"thinking"}}}`,
		},
		{
			name:   "tool call started -> tool_call",
			event:  event.NewToolCallStarted(sid, "tc-1", "bash", json.RawMessage(`{"cmd":"ls"}`)),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1",` +
				`"title":"bash","kind":"other","status":"pending","rawInput":{"cmd":"ls"}}}`,
		},
		{
			name:   "tool call started with no input",
			event:  event.NewToolCallStarted(sid, "tc-2", "ls", nil),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-2",` +
				`"title":"ls","kind":"other","status":"pending"}}`,
		},
		{
			name:   "tool call finished ok -> tool_call_update completed",
			event:  event.NewToolCallFinished(sid, "tc-1", "3 files", false, nil),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call_update",` +
				`"toolCallId":"tc-1","status":"completed",` +
				`"content":[{"type":"content","content":{"type":"text","text":"3 files"}}]}}`,
		},
		{
			name:   "tool call finished with empty result omits content",
			event:  event.NewToolCallFinished(sid, "tc-1", "", false, nil),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call_update",` +
				`"toolCallId":"tc-1","status":"completed"}}`,
		},
		{
			name:   "message started has no projection",
			event:  event.NewMessageStarted(sid, event.MessageText),
			wantOK: false,
		},
		{
			name:   "message finished has no projection",
			event:  event.NewMessageFinished(sid, event.MessageText, "settled"),
			wantOK: false,
		},
		{
			name:   "turn started has no projection",
			event:  event.NewTurnStarted(sid),
			wantOK: false,
		},
		{
			name:   "session created has no projection",
			event:  event.NewSessionCreated(sid),
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := acp.ToSessionUpdate(sid, tc.event)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, data, tc.wantJSON)
		})
	}
}

func TestToSessionUpdateToolCallFinishedError(t *testing.T) {
	// event.ToolCallFinished.IsError is added by the sibling m2-core branch
	// (not yet on this branch's base as of writing); this exercises the
	// error -> failed status mapping once merged. Built via struct literal
	// rather than the NewToolCallFinished constructor, since m2-core's
	// constructor signature isn't known yet — only the field name is
	// guaranteed (event.ToolCallFinished.IsError bool).
	ev := event.ToolCallFinished{ID: "tc-1", Result: "boom", Diagnostics: []string{"stack trace"}, IsError: true}
	got, ok := acp.ToSessionUpdate("sess-1", ev)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	upd, ok := got.Update.(acp.ToolCallUpdated)
	if !ok {
		t.Fatalf("Update type = %T, want acp.ToolCallUpdated", got.Update)
	}
	if upd.Fields.Status != acp.ToolCallStatusFailed {
		t.Errorf("Status = %q, want %q", upd.Fields.Status, acp.ToolCallStatusFailed)
	}
}

func TestStopReasonFor(t *testing.T) {
	tests := []struct {
		stop   string
		want   acp.StopReason
		wantOK bool
	}{
		{"end_turn", acp.StopReasonEndTurn, true},
		{"stop_sequence", acp.StopReasonEndTurn, true},
		{"max_tokens", acp.StopReasonMaxTokens, true},
		{"max_turns", acp.StopReasonMaxTurnRequests, true},
		{"refusal", acp.StopReasonRefusal, true},
		{"cancelled", acp.StopReasonCancelled, true},
		{"tool_use", "", false},
		{"error", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.stop, func(t *testing.T) {
			got, ok := acp.StopReasonFor(tc.stop)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("StopReasonFor(%q) = (%q, %v), want (%q, %v)", tc.stop, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestToPromptResponse(t *testing.T) {
	usage := provider.Usage{InputTokens: 10, OutputTokens: 5}

	t.Run("mapped stop reason", func(t *testing.T) {
		e := event.NewTurnFinished("sess-1", "end_turn", usage)
		got, ok := acp.ToPromptResponse(e)
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if got.StopReason != acp.StopReasonEndTurn {
			t.Errorf("StopReason = %q, want %q", got.StopReason, acp.StopReasonEndTurn)
		}
	})

	t.Run("unmapped stop reason", func(t *testing.T) {
		e := event.NewTurnFinished("sess-1", "tool_use", usage)
		_, ok := acp.ToPromptResponse(e)
		if ok {
			t.Fatal("ok = true, want false for tool_use")
		}
	})
}

func TestToRequestPermissionOut(t *testing.T) {
	opts := []acp.PermissionOption{{OptionID: "opt-1", Name: "Allow", Kind: acp.PermissionAllowOnce}}
	req := acp.ToRequestPermission("sess-1", "tc-1", "Run tests", opts)
	if req.SessionID != "sess-1" || req.ToolCall.ToolCallID != "tc-1" || req.ToolCall.Fields.Title != "Run tests" {
		t.Errorf("unexpected request: %#v", req)
	}
	if len(req.Options) != 1 || req.Options[0].OptionID != "opt-1" {
		t.Errorf("unexpected options: %#v", req.Options)
	}
}
