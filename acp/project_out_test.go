package acp_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/acp"
	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

// turnFinishedWithWindow stamps a model context-window size onto a TurnFinished,
// as the loop/session emitters do via provider.Lookup.
func turnFinishedWithWindow(e event.TurnFinished, window int) event.TurnFinished {
	e.ContextWindow = window
	return e
}

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
			name:   "tool call finished ok -> tool_call_update completed with authoritative input",
			event:  event.NewToolCallFinished(sid, "tc-1", json.RawMessage(`{"cmd":"ls"}`), "3 files", false, nil),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call_update",` +
				`"toolCallId":"tc-1","status":"completed","rawInput":{"cmd":"ls"},` +
				`"content":[{"type":"content","content":{"type":"text","text":"3 files"}}]}}`,
		},
		{
			name:   "tool call finished with empty result and no input omits content and rawInput",
			event:  event.NewToolCallFinished(sid, "tc-1", nil, "", false, nil),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call_update",` +
				`"toolCallId":"tc-1","status":"completed"}}`,
		},
		{
			name: "tool call finished with file edit -> diff block replaces text result",
			event: event.ToolCallFinished{
				ID:     "tc-1",
				Input:  json.RawMessage(`{"path":"foo.go"}`),
				Result: "edited foo.go (1 replacement)",
				Edits:  []event.FileEdit{{Path: "foo.go", OldText: "old", NewText: "new"}},
			},
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call_update",` +
				`"toolCallId":"tc-1","status":"completed","rawInput":{"path":"foo.go"},` +
				`"content":[{"type":"diff","path":"foo.go","oldText":"old","newText":"new"}]}}`,
		},
		{
			name: "tool call finished with file creation -> diff block omits oldText",
			event: event.ToolCallFinished{
				ID:     "tc-1",
				Result: "wrote 3 bytes to new.go",
				Edits:  []event.FileEdit{{Path: "new.go", NewText: "abc"}},
			},
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call_update",` +
				`"toolCallId":"tc-1","status":"completed",` +
				`"content":[{"type":"diff","path":"new.go","newText":"abc"}]}}`,
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
			name:   "message finished reasoning has no projection",
			event:  event.NewMessageFinished(sid, event.MessageReasoning, "settled"),
			wantOK: false,
		},
		{
			name:   "message finished user -> user_message_chunk",
			event:  event.NewMessageFinished(sid, event.MessageUser, "hi"),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"user_message_chunk",` +
				`"content":{"type":"text","text":"hi"}}}`,
		},
		{
			name:   "message started user has no projection",
			event:  event.NewMessageStarted(sid, event.MessageUser),
			wantOK: false,
		},
		{
			name:   "turn started has no projection",
			event:  event.NewTurnStarted(sid),
			wantOK: false,
		},
		{
			name: "turn finished with window and cost -> usage_update",
			event: turnFinishedWithWindow(
				event.NewTurnFinishedCost(sid, "end_turn",
					provider.Usage{InputTokens: 100, CacheReadTokens: 20, OutputTokens: 30},
					&provider.Cost{USD: 0.42}),
				200_000),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"usage_update",` +
				`"used":150,"size":200000,"cost":{"amount":0.42,"currency":"USD"}}}`,
		},
		{
			name: "turn finished with window and no cost omits cost",
			event: turnFinishedWithWindow(
				event.NewTurnFinished(sid, "end_turn",
					provider.Usage{InputTokens: 100, OutputTokens: 30}),
				200_000),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"usage_update",` +
				`"used":130,"size":200000}}`,
		},
		{
			name:   "turn finished with zero context window has no projection",
			event:  event.NewTurnFinished(sid, "end_turn", provider.Usage{InputTokens: 100, OutputTokens: 30}),
			wantOK: false,
		},
		{
			// Max-turns cap / fail-closed paths stamp the context window onto a
			// zero-value Usage. A used:0 usage_update against a full window would
			// misreport "0 / 200k", so the projection skips instead.
			name: "turn finished with window but zero usage has no projection",
			event: turnFinishedWithWindow(
				event.NewTurnFinished(sid, "max_turns", provider.Usage{}),
				200_000),
			wantOK: false,
		},
		{
			name:   "session created has no projection",
			event:  event.NewSessionCreated(sid),
			wantOK: false,
		},
		{
			// A bare (unpublished) event has a zero Time, so updatedAt is omitted;
			// TestToSessionUpdateSessionInfoTimestamp covers the stamped case.
			name:   "session info updated -> session_info_update",
			event:  event.NewSessionInfoUpdated(sid, "Debug authentication timeout"),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"session_info_update",` +
				`"title":"Debug authentication timeout"}}`,
		},
		{
			name: "plan -> plan session/update",
			event: event.NewPlanUpdated(sid, []event.PlanEntry{
				{Content: "Read the code", Priority: "high", Status: "completed"},
				{Content: "Write the fix", Priority: "medium", Status: "in_progress"},
			}),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"plan","entries":[` +
				`{"content":"Read the code","priority":"high","status":"completed"},` +
				`{"content":"Write the fix","priority":"medium","status":"in_progress"}]}}`,
		},
		{
			name:   "empty plan -> plan with empty entries",
			event:  event.NewPlanUpdated(sid, nil),
			wantOK: true,
			wantJSON: `{"sessionId":"sess-1","update":{"sessionUpdate":"plan",` +
				`"entries":[]}}`,
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

// TestToSessionUpdateSessionInfoTimestamp asserts a session.info that has been
// through a broker projects updatedAt from the event's publish timestamp.
func TestToSessionUpdateSessionInfoTimestamp(t *testing.T) {
	fixed := time.Date(2025, 1, 15, 12, 34, 56, 0, time.UTC)
	b := event.NewBroker(event.WithClock(func() time.Time { return fixed }))
	defer b.Close()
	sub := b.Subscribe(event.FilterMustDeliver, 8)
	defer sub.Close()

	b.Publish(event.NewSessionInfoUpdated("sess-1", "Debug authentication timeout"))
	ev := <-sub.C

	got, ok := acp.ToSessionUpdate("sess-1", ev)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"sessionId":"sess-1","update":{"sessionUpdate":"session_info_update",` +
		`"title":"Debug authentication timeout","updatedAt":"2025-01-15T12:34:56Z"}}`
	assertJSONEqual(t, data, want)
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
