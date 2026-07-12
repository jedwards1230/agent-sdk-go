package faux_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/provider/faux"
)

// collect drains a stream into a slice of events.
func collect(t *testing.T, s provider.Stream) []provider.StreamEvent {
	t.Helper()
	var out []provider.StreamEvent
	for {
		e, err := s.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, e)
	}
}

// TestDefaultScriptOrder asserts the default script streams reasoning, then
// text, then usage, then stop.
func TestDefaultScriptOrder(t *testing.T) {
	p := faux.New(faux.Default())
	s, err := p.Stream(context.Background(), provider.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = s.Close() }()

	got := collect(t, s)
	wantTypes := []provider.StreamEventType{
		provider.StreamReasoning, provider.StreamReasoning,
		provider.StreamText, provider.StreamText, provider.StreamText,
		provider.StreamUsage,
		provider.StreamStop,
	}
	if len(got) != len(wantTypes) {
		t.Fatalf("got %d events, want %d", len(got), len(wantTypes))
	}
	for i, e := range got {
		if e.Type != wantTypes[i] {
			t.Errorf("event %d type = %d, want %d", i, e.Type, wantTypes[i])
		}
	}
	if last := got[len(got)-1]; last.StopReason != "end_turn" {
		t.Errorf("stop reason = %q, want end_turn", last.StopReason)
	}
}

// TestScriptExhausted asserts a second turn against a one-turn script errors.
func TestScriptExhausted(t *testing.T) {
	p := faux.New(faux.Script{Turns: []faux.Turn{{Text: []string{"hi"}, StopReason: "end_turn"}}})
	if _, err := p.Stream(context.Background(), provider.Request{}); err != nil {
		t.Fatalf("first Stream: %v", err)
	}
	if _, err := p.Stream(context.Background(), provider.Request{}); err == nil {
		t.Fatal("second Stream returned nil error, want script-exhausted error")
	}
}
