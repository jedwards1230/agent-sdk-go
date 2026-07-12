package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// TestCassetteToolTurn replays a recorded, realistic Responses-API turn
// (reasoning summary + assistant text + a function call, with the full set of
// lifecycle/part-boundary events) and asserts the normalized event sequence and
// usage. The cassette is committed testdata captured from the wire shape; it is
// served verbatim so the parser is exercised against real event ordering.
func TestCassetteToolTurn(t *testing.T) {
	payload, err := os.ReadFile("testdata/responses_tool_turn.sse")
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	defer srv.Close()

	p := New("gpt-5", provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk"}}, WithBaseURL(srv.URL))
	h, err := p.Stream(context.Background(), provider.Request{
		System:   "You are a helpful assistant.",
		Messages: []provider.Message{provider.UserText("what's the weather in Paris?")},
		Tools: []provider.ToolSpec{{
			Name:        "get_weather",
			Description: "Get the current weather for a city",
			InputSchema: []byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
		}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = h.Close() }()

	var (
		reasoning, text, toolArgs string
		toolStart, toolEnd        *provider.ToolCall
		fin                       provider.StreamEvent
	)
	for ev, err := range provider.Iter(h) {
		if err != nil {
			t.Fatalf("iter: %v", err)
		}
		switch ev.Type {
		case provider.StreamReasoningDelta:
			reasoning += ev.Text
			// The reasoning item's id is journaled on every reasoning delta.
			if ev.Meta[metaItemID] != "rs_1" {
				t.Errorf("reasoning delta Meta[%s] = %q, want rs_1", metaItemID, ev.Meta[metaItemID])
			}
		case provider.StreamTextDelta:
			text += ev.Text
		case provider.StreamToolCallStart:
			toolStart = ev.Tool
		case provider.StreamToolCallDelta:
			toolArgs += ev.Tool.Delta
		case provider.StreamToolCallEnd:
			toolEnd = ev.Tool
		case provider.StreamFinished:
			fin = ev
		}
	}

	if reasoning != "The user wants the weather. I'll call the tool." {
		t.Errorf("reasoning = %q", reasoning)
	}
	if text != "Let me check the weather for you." {
		t.Errorf("text = %q", text)
	}
	if toolStart == nil || toolStart.ID != "call_weather_01" || toolStart.Name != "get_weather" {
		t.Errorf("tool start = %+v", toolStart)
	}
	if toolArgs != `{"city":"Paris"}` {
		t.Errorf("assembled tool args = %q", toolArgs)
	}
	if toolEnd == nil || string(toolEnd.Input) != `{"city":"Paris"}` {
		t.Errorf("tool end input = %+v", toolEnd)
	}
	if fin.StopReason != provider.StopToolUse {
		t.Errorf("stop = %q, want tool_use", fin.StopReason)
	}

	// Usage: 320 input incl. 256 cached -> 64 uncached input + 256 cache-read.
	want := provider.Usage{
		InputTokens:     64,
		OutputTokens:    48,
		CacheReadTokens: 256,
		Raw: map[string]int{
			"input_tokens": 320, "output_tokens": 48, "total_tokens": 368,
			"cached_tokens": 256, "reasoning_tokens": 32,
		},
	}
	if !fin.Usage.Equal(want) {
		t.Errorf("usage = %+v, want %+v", fin.Usage, want)
	}

	// Sanity: the turn priced through the model registry.
	if cost, ok := provider.CostOf("gpt-5", fin.Usage); !ok || cost.USD <= 0 {
		t.Errorf("expected a positive priced cost, got %+v ok=%v", cost, ok)
	}
}
