package event_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

// TestTurnFinishedContextWindow asserts the additive ContextWindow field
// serializes as "context_window" when set and, being omitempty, leaves the
// payload unchanged (no key) when zero.
func TestTurnFinishedContextWindow(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		ev := event.NewTurnFinished(sid, "end_turn", provider.Usage{})
		ev.ContextWindow = 200_000
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got, want := m["context_window"], float64(200_000); got != want {
			t.Errorf("context_window = %v, want %v", got, want)
		}
	})

	t.Run("zero omitted", func(t *testing.T) {
		raw, err := json.Marshal(event.NewTurnFinished(sid, "end_turn", provider.Usage{}))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, ok := m["context_window"]; ok {
			t.Errorf("context_window present for zero value: %s", raw)
		}
	})
}

// TestToolCallFinishedEdits asserts the additive Edits field serializes as an
// "edits" array of {path, old_text?, new_text} when set — with old_text
// omitted for a creation — and, being omitempty, leaves the payload unchanged
// (no key) when nil.
func TestToolCallFinishedEdits(t *testing.T) {
	t.Run("set with creation omits old_text", func(t *testing.T) {
		ev := event.NewToolCallFinished(sid, "tc-1", nil, "wrote new.go", false, nil)
		ev.Edits = []event.FileEdit{{Path: "new.go", NewText: "abc"}}
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m struct {
			Edits []map[string]any `json:"edits"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(m.Edits) != 1 {
			t.Fatalf("edits len = %d, want 1: %s", len(m.Edits), raw)
		}
		if got := m.Edits[0]["path"]; got != "new.go" {
			t.Errorf("path = %v, want new.go", got)
		}
		if got := m.Edits[0]["new_text"]; got != "abc" {
			t.Errorf("new_text = %v, want abc", got)
		}
		if _, ok := m.Edits[0]["old_text"]; ok {
			t.Errorf("old_text present for a creation: %s", raw)
		}
	})

	t.Run("nil omitted", func(t *testing.T) {
		raw, err := json.Marshal(event.NewToolCallFinished(sid, "tc-1", nil, "ok", false, nil))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, ok := m["edits"]; ok {
			t.Errorf("edits present for nil value: %s", raw)
		}
	})
}

// TestMessageUserRoundTrip asserts MessageUser is just another MessageKind
// value: it round-trips through the MessageStarted/MessageDelta/MessageFinished
// JSON envelopes exactly like MessageText and MessageReasoning do, and
// MessageFinished{MessageUser} is must-deliver like every other message.finished.
func TestMessageUserRoundTrip(t *testing.T) {
	b := event.NewBroker(event.WithClock(fixedClock))
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 8)

	b.Publish(event.NewMessageStarted(sid, event.MessageUser))
	b.Publish(event.NewMessageDelta(sid, event.MessageUser, "hel"))
	b.Publish(event.NewMessageFinishedMeta(sid, event.MessageUser, "hello", map[string]string{"k": "v"}))

	tests := []struct {
		name string
		want map[string]any
	}{
		{
			name: "message.started",
			want: map[string]any{
				"type":       event.KindMessageStarted,
				"session_id": sid,
				"kind":       string(event.MessageUser),
			},
		},
		{
			name: "message.delta",
			want: map[string]any{
				"type":       event.KindMessageDelta,
				"session_id": sid,
				"kind":       string(event.MessageUser),
				"text":       "hel",
			},
		},
		{
			name: "message.finished",
			want: map[string]any{
				"type":       event.KindMessageFinished,
				"session_id": sid,
				"kind":       string(event.MessageUser),
				"content":    "hello",
				"meta":       map[string]any{"k": "v"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := <-sub.C
			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			for k, want := range tc.want {
				got := m[k]
				if !equalJSON(got, want) {
					t.Errorf("envelope[%q] = %v, want %v", k, got, want)
				}
			}
		})
	}

	if got := event.TierOf(event.KindMessageFinished); got != event.TierMustDeliver {
		t.Errorf("TierOf(message.finished) = %v, want %v (a settled user message must still be must-deliver)", got, event.TierMustDeliver)
	}
}

// equalJSON compares two values decoded from JSON (map[string]any, []any,
// float64, string, bool, nil), including nested maps.
func equalJSON(a, b any) bool {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok != bok {
		return false
	}
	if aok {
		if len(am) != len(bm) {
			return false
		}
		for k, v := range am {
			if !equalJSON(v, bm[k]) {
				return false
			}
		}
		return true
	}
	return a == b
}
