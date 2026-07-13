package event_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
)

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
