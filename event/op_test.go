package event_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
)

// TestSessionSetEffort_Marshal pins the wire shape of the session.set_effort op
// — the effort-axis parallel to session.set_model — so the {type, session_id,
// effort} envelope a client sends stays stable.
func TestSessionSetEffort_Marshal(t *testing.T) {
	op := event.SessionSetEffort{SessionID: "sess-1", Effort: "high"}

	if got, want := op.Kind(), event.OpSessionSetEffort; got != want {
		t.Fatalf("Kind() = %q, want %q", got, want)
	}

	b, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := map[string]any{
		"type":       "session.set_effort",
		"session_id": "sess-1",
		"effort":     "high",
	}
	if len(got) != len(want) {
		t.Fatalf("wire object = %v, want exactly the keys in %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("wire[%q] = %v, want %v", k, got[k], v)
		}
	}
}
