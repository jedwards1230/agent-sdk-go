package event_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

// TestUnmarshalRoundTrip asserts every event kind the package can marshal
// round-trips through MarshalJSON → Unmarshal back to a value deep-equal to the
// original — full coverage of the closed Event union, one case per kind. Each
// event is built via its constructor (seq 0, zero time), so the restored
// envelope matches without depending on seq/time reassignment; the additive
// fields not reachable through a constructor (TurnFinished.ContextWindow,
// ToolCallFinished.Edits, and the tool-call events' Agent) are set on the value
// to prove they survive too.
func TestUnmarshalRoundTrip(t *testing.T) {
	turnFinished := event.NewTurnFinishedCost(sid, "end_turn",
		provider.Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 3},
		&provider.Cost{USD: 0.42, InputUSD: 0.1, OutputUSD: 0.32})
	turnFinished.ContextWindow = 200_000

	toolFinished := event.NewToolCallFinishedSpill(sid, "tc-1",
		json.RawMessage(`{"path":"/tmp/x"}`), "wrote 3 bytes", false,
		[]string{"warning: clobbered"},
		"sessions/s/tc-1.log", 3, "abc123")
	toolFinished.Edits = []event.FileEdit{
		{Path: "new.go", NewText: "package main"},
		{Path: "old.go", OldText: "v1", NewText: "v2"},
	}
	// Agent is set at emit time (not through a constructor); set it here to prove
	// it survives the round trip on all three tool-call events.
	toolFinished.Agent = "researcher"

	toolStarted := event.NewToolCallStarted(sid, "tc-1", "edit", json.RawMessage(`{"a":1}`))
	toolStarted.Agent = "researcher"
	toolDelta := event.NewToolCallDelta(sid, "tc-1", "chunk")
	toolDelta.Agent = "researcher"

	cases := []struct {
		name string
		ev   event.Event
	}{
		{"session.created", event.NewSessionCreated(sid)},
		{"session.resumed", event.NewSessionResumed(sid)},
		{"session.forked", event.NewSessionForked(sid)},
		{"session.compacted", event.NewSessionCompacted(sid)},
		{"session.killed", event.NewSessionKilled(sid)},
		{"session.archived", event.NewSessionArchived(sid)},
		{"session.info", event.NewSessionInfoUpdated(sid, "Debug auth timeout")},
		{"session.config", event.NewConfigOptionsUpdated(sid, []event.ConfigOption{
			{
				ID: "model", Name: "Model", Category: "model", Kind: event.ConfigOptionSelect,
				SelectedValue: "opus",
				Values: []event.ConfigSelectValue{
					{Value: "opus", Name: "Opus"},
					{Value: "sonnet", Name: "Sonnet", Description: "faster"},
				},
			},
			{ID: "stream", Name: "Stream", Kind: event.ConfigOptionBoolean, Enabled: true},
		})},
		{"plan", event.NewPlanUpdated(sid, []event.PlanEntry{
			{Content: "Read the code", Priority: "high", Status: "completed"},
			{Content: "Write the fix", Priority: "medium", Status: "in_progress"},
		})},
		{"session.error", event.NewSessionError(sid, "boom", true)},
		{"turn.started", event.NewTurnStarted(sid)},
		{"turn.finished", turnFinished},
		{"message.started", event.NewMessageStarted(sid, event.MessageText)},
		{"message.delta", event.NewMessageDelta(sid, event.MessageReasoning, "thinking")},
		{"message.finished", event.NewMessageFinishedMeta(sid, event.MessageUser, "hello",
			map[string]string{"anthropic.signature": "sig"})},
		{"tool.call.started", toolStarted},
		{"tool.call.delta", toolDelta},
		{"tool.call.finished", toolFinished},
		{"permission.requested", event.NewPermissionRequested(sid, "p-1", "edit",
			map[string]any{"path": "/etc/hosts"}, []string{"rule-a", "rule-b"})},
		{"permission.resolved", event.NewPermissionResolved(sid, "p-1", event.VerdictAllow, "allow-edits")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.ev.Kind() != tc.name {
				t.Fatalf("test wiring: case %q holds a %q event", tc.name, tc.ev.Kind())
			}
			raw, err := json.Marshal(tc.ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got, err := event.Unmarshal(raw)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, tc.ev) {
				t.Errorf("round trip mismatch\n got: %#v\nwant: %#v\nwire: %s", got, tc.ev, raw)
			}
		})
	}
}

// TestUnmarshalRestoresSeqAndTime asserts Unmarshal restores the envelope's seq
// and publish time verbatim — the fields the broker assigns at publish — not
// just the payload. It publishes through a broker so both carry real,
// non-zero values.
func TestUnmarshalRestoresSeqAndTime(t *testing.T) {
	b := event.NewBroker(event.WithClock(fixedClock))
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 4)

	b.Publish(event.NewMessageDelta(sid, event.MessageText, "hi"))
	published := <-sub.C
	if published.Seq() == 0 {
		t.Fatal("precondition: broker assigned seq 0")
	}

	raw, err := json.Marshal(published)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := event.Unmarshal(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Seq() != published.Seq() {
		t.Errorf("seq = %d, want %d", got.Seq(), published.Seq())
	}
	if !got.Time().Equal(published.Time()) {
		t.Errorf("time = %v, want %v", got.Time(), published.Time())
	}
	if got.Time().IsZero() {
		t.Error("time was not restored (zero)")
	}
	if want := fixedClock(); !got.Time().Equal(want) {
		t.Errorf("time = %v, want %v", got.Time(), want)
	}
}

// TestUnmarshalUnknownKind asserts an unrecognized "type" — a kind a newer
// producer might emit — returns an error matching ErrUnknownKind (so a
// forward-compatible consumer can skip-and-continue) whose message names the
// kind, and returns a nil event.
func TestUnmarshalUnknownKind(t *testing.T) {
	got, err := event.Unmarshal([]byte(`{"type":"session.teleported","session_id":"s"}`))
	if got != nil {
		t.Errorf("event = %#v, want nil", got)
	}
	if !errors.Is(err, event.ErrUnknownKind) {
		t.Fatalf("err = %v, want wrapping ErrUnknownKind", err)
	}
	if want := "session.teleported"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want message naming %q", err, want)
	}
}

// TestUnmarshalMalformed asserts malformed input is an ordinary decode error,
// NOT ErrUnknownKind — a consumer must not mistake corruption for a
// forward-compatible skip. Covers invalid JSON, an unparseable time, and a wrong
// JSON type for a payload field.
func TestUnmarshalMalformed(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"not json", `{not json`},
		{"truncated", `{"type":"turn.started"`},
		{"bad time", `{"type":"turn.started","session_id":"s","time":"tuesday"}`},
		{"wrong field type", `{"type":"session.info","session_id":"s","title":123}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := event.Unmarshal([]byte(tc.data))
			if err == nil {
				t.Fatalf("err = nil, want a decode error (got event %#v)", got)
			}
			if got != nil {
				t.Errorf("event = %#v, want nil on error", got)
			}
			if errors.Is(err, event.ErrUnknownKind) {
				t.Errorf("err = %v, want an ordinary decode error, not ErrUnknownKind", err)
			}
		})
	}
}

// TestUnmarshalEmptySliceNormalization documents the one non-identity edge in
// the round trip: session.config and plan marshal a nil Options/Entries slice
// as [] (so a client can tell a cleared set from an absent field — see their
// MarshalJSON), and Unmarshal faithfully restores that wire form as an empty,
// non-nil slice. The value is not deep-equal to the nil-slice original, but it
// re-marshals identically, which is the property the wire actually guarantees.
func TestUnmarshalEmptySliceNormalization(t *testing.T) {
	for _, ev := range []event.Event{
		event.NewConfigOptionsUpdated(sid, nil),
		event.NewPlanUpdated(sid, nil),
	} {
		t.Run(ev.Kind(), func(t *testing.T) {
			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got, err := event.Unmarshal(raw)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			reraw, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("re-marshal: %v", err)
			}
			if string(reraw) != string(raw) {
				t.Errorf("re-marshal = %s, want %s", reraw, raw)
			}
		})
	}
}
