package event_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

// TestSessionInfoUpdatedMarshal asserts the additive session.info event carries
// its title on the wire, is must-deliver, and reports the right kind.
func TestSessionInfoUpdatedMarshal(t *testing.T) {
	ev := event.NewSessionInfoUpdated(sid, "Debug auth timeout")
	if ev.Kind() != event.KindSessionInfo {
		t.Errorf("Kind() = %q, want %q", ev.Kind(), event.KindSessionInfo)
	}
	if ev.Tier() != event.TierMustDeliver {
		t.Errorf("Tier() = %v, want must-deliver", ev.Tier())
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		Title     string `json:"title"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Type != event.KindSessionInfo {
		t.Errorf("type = %q, want %q", m.Type, event.KindSessionInfo)
	}
	if m.SessionID != sid {
		t.Errorf("session_id = %q, want %q", m.SessionID, sid)
	}
	if m.Title != "Debug auth timeout" {
		t.Errorf("title = %q, want %q", m.Title, "Debug auth timeout")
	}
}

// TestPlanUpdatedMarshal asserts the plan event carries its entries on the wire,
// is must-deliver, reports the right kind, and marshals an empty plan to an
// entries array rather than null.
func TestPlanUpdatedMarshal(t *testing.T) {
	t.Run("with entries", func(t *testing.T) {
		ev := event.NewPlanUpdated(sid, []event.PlanEntry{
			{Content: "Read the code", Priority: "high", Status: "completed"},
			{Content: "Write the fix", Priority: "medium", Status: "in_progress"},
		})
		if ev.Kind() != event.KindPlan {
			t.Errorf("Kind() = %q, want %q", ev.Kind(), event.KindPlan)
		}
		if ev.Tier() != event.TierMustDeliver {
			t.Errorf("Tier() = %v, want must-deliver", ev.Tier())
		}
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m struct {
			Type      string            `json:"type"`
			SessionID string            `json:"session_id"`
			Entries   []event.PlanEntry `json:"entries"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Type != event.KindPlan {
			t.Errorf("type = %q, want %q", m.Type, event.KindPlan)
		}
		if m.SessionID != sid {
			t.Errorf("session_id = %q, want %q", m.SessionID, sid)
		}
		if len(m.Entries) != 2 || m.Entries[0].Content != "Read the code" ||
			m.Entries[0].Priority != "high" || m.Entries[0].Status != "completed" ||
			m.Entries[1].Status != "in_progress" {
			t.Errorf("entries = %+v, want the two round-tripped entries", m.Entries)
		}
	})

	t.Run("empty plan marshals to []", func(t *testing.T) {
		raw, err := json.Marshal(event.NewPlanUpdated(sid, nil))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m struct {
			Entries json.RawMessage `json:"entries"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if string(m.Entries) != "[]" {
			t.Errorf("entries = %s, want []", m.Entries)
		}
	})
}

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
