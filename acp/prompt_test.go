package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
)

func TestPromptResponseMarshal(t *testing.T) {
	tests := []struct {
		reason acp.StopReason
		want   string
	}{
		{acp.StopReasonEndTurn, `{"stopReason":"end_turn"}`},
		{acp.StopReasonMaxTokens, `{"stopReason":"max_tokens"}`},
		{acp.StopReasonMaxTurnRequests, `{"stopReason":"max_turn_requests"}`},
		{acp.StopReasonRefusal, `{"stopReason":"refusal"}`},
		{acp.StopReasonCancelled, `{"stopReason":"cancelled"}`},
	}
	for _, tc := range tests {
		t.Run(string(tc.reason), func(t *testing.T) {
			got, err := json.Marshal(acp.PromptResponse{StopReason: tc.reason})
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, got, tc.want)
		})
	}
}

func TestPromptRequestRoundTrip(t *testing.T) {
	want := acp.PromptRequest{
		SessionID: "sess-1",
		Prompt: []acp.ContentBlock{
			acp.TextBlock("look at "),
			acp.ResourceLink("file:///a.go", "a.go"),
		},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	wantJSON := `{"sessionId":"sess-1","prompt":[` +
		`{"type":"text","text":"look at "},` +
		`{"type":"resource_link","uri":"file:///a.go","name":"a.go"}]}`
	assertJSONEqual(t, data, wantJSON)

	var got acp.PromptRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, want.SessionID)
	}
	if len(got.Prompt) != len(want.Prompt) {
		t.Fatalf("Prompt length = %d, want %d", len(got.Prompt), len(want.Prompt))
	}
	for i := range got.Prompt {
		if got.Prompt[i] != want.Prompt[i] {
			t.Errorf("Prompt[%d] = %#v, want %#v", i, got.Prompt[i], want.Prompt[i])
		}
	}
}

func TestPromptRequestUnmarshalInvalidBlock(t *testing.T) {
	var req acp.PromptRequest
	err := json.Unmarshal([]byte(`{"sessionId":"s","prompt":[{"type":"bogus"}]}`), &req)
	if err == nil {
		t.Fatal("Unmarshal() error = nil, want error for unmodeled block type")
	}
}
